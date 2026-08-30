package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/files"
	"github.com/hkjang/moyro/server/internal/rpcbridge"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/ws"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	mmplugin "github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/shared/mlog"
)

const (
	RuntimeMoyroV1      = "moyro_v1"
	RuntimeMattermostV1 = "mattermost_v1"
	pluginDrainTimeout  = 30 * time.Second
)

type Plugin struct {
	Manifest *Manifest
	Dir      string
	State    string // installed, running, failed
	Runtime  string
	Enabled  bool
	Error    string
	client   pluginClient
	// transitioning keeps a candidate private until its durable install
	// marker has been committed. Internal activation APIs can still resolve
	// the candidate through Host.plugin.
	transitioning atomic.Bool
	// apiClosed is distinct from transitioning: activation deliberately runs
	// while transitioning is true and must be able to call the Mattermost API.
	// Once replacement/disable closes dispatch admission, however, callbacks
	// from that old process must not start new database or event mutations.
	apiClosed atomic.Bool
	// apiDeactivating is a narrow exception for synchronous OnDeactivate
	// cleanup (for example releasing a cluster KV lease or disabling a bot).
	// It is cleared immediately when the hook returns; stale asynchronous
	// callbacks remain rejected once teardown has completed.
	apiDeactivating atomic.Bool
	// apiActivating similarly permits OnActivate to register commands, use KV,
	// and ensure bots when re-enabling a generation whose normal admission was
	// closed by Disable. Dispatch remains hidden through transitioning.
	apiActivating atomic.Bool
	configuration sync.Mutex
	dispatch      pluginDispatchState
	commandsMu    sync.RWMutex
	commands      map[string]struct{}
	mmHooks       mmplugin.Hooks
	mmImplemented map[string]struct{}
}

// pluginDispatchState binds every admitted call to one concrete plugin
// generation. A replacement first closes admission and waits for in-flight
// leases to drain before tearing down that generation. In particular, an old
// request can never look up a same-ID candidate in Mattermost's Environment.
type pluginDispatchState struct {
	mu       sync.Mutex
	inFlight int
	drained  chan struct{}
}

type pluginDispatchLease struct {
	plugin  *Plugin
	runtime string
	client  pluginClient
	hooks   mmplugin.Hooks
}

type pluginRuntimeSnapshot struct {
	state   string
	enabled bool
	err     string
	client  pluginClient
	hooks   mmplugin.Hooks
}

type pluginClient interface {
	OnActivate(context.Context) error
	OnDeactivate(context.Context) error
	Close() error
	MessageWillBePosted(context.Context, rpcbridge.PostEvent) (*rpcbridge.Decision, error)
	MessageHasBeenPosted(context.Context, rpcbridge.PostEvent) error
	ExecuteCommand(context.Context, rpcbridge.CommandArgs) (*rpcbridge.CommandReply, error)
}

func (p *Plugin) runtimeSnapshot() pluginRuntimeSnapshot {
	if p == nil {
		return pluginRuntimeSnapshot{}
	}
	p.dispatch.mu.Lock()
	defer p.dispatch.mu.Unlock()
	return pluginRuntimeSnapshot{
		state: p.State, enabled: p.Enabled, err: p.Error,
		client: p.client, hooks: p.mmHooks,
	}
}

func (p *Plugin) acquireDispatch() (*pluginDispatchLease, bool) {
	if p == nil {
		return nil, false
	}
	p.dispatch.mu.Lock()
	defer p.dispatch.mu.Unlock()
	if p.transitioning.Load() || p.State != "running" || !p.Enabled {
		return nil, false
	}
	if p.dispatch.inFlight == 0 {
		p.dispatch.drained = make(chan struct{})
	}
	p.dispatch.inFlight++
	return &pluginDispatchLease{
		plugin: p, runtime: p.Runtime, client: p.client, hooks: p.mmHooks,
	}, true
}

func (h *Host) acquirePluginAPICall(pluginID string, generation *Plugin) (*pluginDispatchLease, bool) {
	if h == nil || generation == nil || h.runtimePoisoned.Load() ||
		(generation.apiClosed.Load() && !generation.apiDeactivating.Load() && !generation.apiActivating.Load()) {
		return nil, false
	}
	// Hold the registry read lock through admission so a same-ID candidate
	// cannot replace this generation between validation and refcounting.
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.plugins[pluginID] != generation {
		return nil, false
	}
	generation.dispatch.mu.Lock()
	defer generation.dispatch.mu.Unlock()
	if generation.apiClosed.Load() && !generation.apiDeactivating.Load() && !generation.apiActivating.Load() {
		return nil, false
	}
	if generation.dispatch.inFlight == 0 {
		generation.dispatch.drained = make(chan struct{})
	}
	generation.dispatch.inFlight++
	return &pluginDispatchLease{plugin: generation, runtime: generation.Runtime}, true
}

func (lease *pluginDispatchLease) release() {
	if lease == nil || lease.plugin == nil {
		return
	}
	p := lease.plugin
	p.dispatch.mu.Lock()
	if p.dispatch.inFlight > 0 {
		p.dispatch.inFlight--
		if p.dispatch.inFlight == 0 && p.dispatch.drained != nil {
			close(p.dispatch.drained)
			p.dispatch.drained = nil
		}
	}
	p.dispatch.mu.Unlock()
	lease.plugin = nil
}

func releasePluginDispatches(leases []*pluginDispatchLease) {
	for _, lease := range leases {
		lease.release()
	}
}

func (p *Plugin) closeDispatchAdmission(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.apiClosed.Store(true)
	p.transitioning.Store(true)
	p.dispatch.mu.Lock()
	if p.dispatch.inFlight == 0 {
		p.dispatch.mu.Unlock()
		return nil
	}
	drained := p.dispatch.drained
	p.dispatch.mu.Unlock()
	drainCtx, cancel := context.WithTimeout(ctx, pluginDrainTimeout)
	defer cancel()
	select {
	case <-drained:
		return nil
	case <-drainCtx.Done():
		return fmt.Errorf("%w: drain plugin %s calls: %v", ErrPluginBusy, p.Manifest.ID, drainCtx.Err())
	}
}

func (p *Plugin) openDispatchAdmission() {
	if p != nil {
		p.apiClosed.Store(false)
		p.transitioning.Store(false)
	}
}

func (p *Plugin) registerCommand(trigger string) {
	if p == nil {
		return
	}
	trigger = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trigger, "/")))
	if trigger == "" {
		return
	}
	p.commandsMu.Lock()
	if p.commands == nil {
		p.commands = make(map[string]struct{})
	}
	p.commands[trigger] = struct{}{}
	p.commandsMu.Unlock()
}

func (p *Plugin) acceptsCommand(trigger string) bool {
	if p == nil {
		return false
	}
	trigger = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trigger, "/")))
	p.commandsMu.RLock()
	_, ok := p.commands[trigger]
	p.commandsMu.RUnlock()
	return ok
}

type Host struct {
	dir          string
	webappDir    string
	logger       *slog.Logger
	db           *store.DB
	secrets      *secrets.Manager
	mmEnv        *mmplugin.Environment
	mmLogger     *mlog.Logger
	mu           sync.RWMutex
	lifecycle    sync.Mutex
	plugins      map[string]*Plugin
	order        []string
	postCommands *postcommand.Service
	files        *files.Service
	events       interface{ Broadcast(ws.Event) }
	audit        interface {
		LogAsync(actorID, action, target string, payload map[string]any)
	}

	activationTimeout     time.Duration
	activationStopTimeout time.Duration
	runtimePoisoned       atomic.Bool
}

// BindApplicationServices connects plugin mutations to the same command,
// storage, websocket, and audit services used by REST. It is called once by
// the HTTP composition root before installed plugins are activated.
func (h *Host) BindApplicationServices(postCommands *postcommand.Service, fileService *files.Service, events interface{ Broadcast(ws.Event) }, auditService *audit.Service) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.postCommands = postCommands
	h.files = fileService
	h.events = events
	h.audit = auditService
	h.mu.Unlock()
}

func New(dir string, logger *slog.Logger) *Host {
	if logger == nil {
		logger = slog.Default()
	}
	return &Host{
		dir: dir, logger: logger, plugins: map[string]*Plugin{}, order: []string{},
		activationTimeout: 30 * time.Second, activationStopTimeout: 10 * time.Second,
	}
}

// NewWithRuntime enables the durable manager and the Mattermost binary ABI in
// addition to Moyro's native SDK. The legacy New constructor remains useful
// for isolated native-host tests and embedders without PostgreSQL.
func NewWithRuntime(dir string, db *store.DB, secretManager *secrets.Manager, logger *slog.Logger) (*Host, error) {
	h := New(dir, logger)
	h.db = db
	h.secrets = secretManager
	h.webappDir = filepath.Join(dir, ".webapp")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create plugin directory: %w", err)
	}
	if err := os.MkdirAll(h.webappDir, 0o750); err != nil {
		return nil, fmt.Errorf("create plugin webapp directory: %w", err)
	}
	mmLogger, err := mlog.NewLogger()
	if err != nil {
		return nil, fmt.Errorf("create Mattermost plugin logger: %w", err)
	}
	h.mmLogger = mmLogger
	h.mmEnv, err = mmplugin.NewEnvironment(
		func(manifest *mmmodel.Manifest) mmplugin.API {
			generation, _ := h.plugin(manifest.Id)
			return &mattermostAPI{
				host: h, db: h.db, pluginID: manifest.Id,
				generation: generation,
				logger:     h.logger.With("runtime", RuntimeMattermostV1),
			}
		},
		nil,
		h.dir,
		h.webappDir,
		mmLogger,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create Mattermost plugin runtime: %w", err)
	}
	return h, nil
}

func (h *Host) LoadAll(ctx context.Context) error {
	// Resolve every interrupted install before looking at live directories.
	// This prevents a new executable from ever being paired with the previous
	// database runtime/configuration after an unclean shutdown.
	if err := h.recoverInstallTransactions(ctx); err != nil {
		return err
	}
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		pluginDir := filepath.Join(h.dir, e.Name())
		if err := h.loadOne(ctx, pluginDir); err != nil {
			h.logger.Warn("plugin load failed", "dir", pluginDir, "err", err)
			if h.runtimePoisoned.Load() {
				return err
			}
		}
	}
	return nil
}

func (h *Host) loadOne(ctx context.Context, dir string) error {
	m, err := LoadManifest(dir)
	if err != nil {
		return err
	}
	if filepath.Base(filepath.Clean(dir)) != m.ID {
		return fmt.Errorf("plugin directory %q does not match manifest id %q", filepath.Base(dir), m.ID)
	}
	runtimeKind, enabled, err := h.loadRuntimeMetadata(ctx, m.ID)
	if err != nil {
		return err
	}
	p := &Plugin{
		Manifest: m, Dir: dir, State: "installed", Runtime: runtimeKind,
		Enabled: enabled,
	}
	p.transitioning.Store(true)
	h.register(p)
	if err := h.persistPlugin(ctx, p, "", ""); err != nil {
		return err
	}
	if !enabled {
		p.openDispatchAdmission()
		h.logger.Info("plugin loaded disabled", "id", m.ID, "version", m.Version, "runtime", runtimeKind)
		return nil
	}

	if err := h.activatePlugin(ctx, p); err != nil {
		p.dispatch.mu.Lock()
		p.State = "failed"
		p.Error = err.Error()
		p.dispatch.mu.Unlock()
		h.persistState(ctx, p)
		return err
	}

	h.persistState(ctx, p)
	p.openDispatchAdmission()
	status := p.runtimeSnapshot()
	h.logger.Info("plugin loaded", "id", m.ID, "version", m.Version, "state", status.state, "runtime", runtimeKind)
	return nil
}

func (h *Host) register(p *Plugin) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.plugins[p.Manifest.ID]; !exists {
		h.order = append(h.order, p.Manifest.ID)
	}
	h.plugins[p.Manifest.ID] = p
}

func (h *Host) unregister(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.plugins, id)
	for index, pluginID := range h.order {
		if pluginID == id {
			h.order = append(h.order[:index], h.order[index+1:]...)
			break
		}
	}
}

func (h *Host) List() []map[string]any {
	h.mu.RLock()
	plugins := make([]*Plugin, 0, len(h.plugins))
	for _, id := range h.order {
		p := h.plugins[id]
		if p == nil || p.transitioning.Load() {
			continue
		}
		plugins = append(plugins, p)
	}
	h.mu.RUnlock()
	out := make([]map[string]any, 0, len(plugins))
	for _, p := range plugins {
		status := p.runtimeSnapshot()
		raw, _ := json.Marshal(p.Manifest)
		var mani map[string]any
		_ = json.Unmarshal(raw, &mani)
		out = append(out, map[string]any{
			"id":       p.Manifest.ID,
			"version":  p.Manifest.Version,
			"state":    status.state,
			"enabled":  status.enabled,
			"runtime":  p.Runtime,
			"error":    status.err,
			"manifest": mani,
		})
	}
	return out
}

func (h *Host) plugin(id string) (*Plugin, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.plugins[id]
	return p, ok
}

func (h *Host) runningPlugins() []*pluginDispatchLease {
	if h.runtimePoisoned.Load() {
		return nil
	}
	h.mu.RLock()
	candidates := make([]*Plugin, 0, len(h.plugins))
	for _, id := range h.order {
		p := h.plugins[id]
		if p != nil {
			candidates = append(candidates, p)
		}
	}
	h.mu.RUnlock()
	plugins := make([]*pluginDispatchLease, 0, len(candidates))
	for _, p := range candidates {
		lease, ok := p.acquireDispatch()
		if !ok {
			continue
		}
		plugins = append(plugins, lease)
	}
	if h.runtimePoisoned.Load() {
		releasePluginDispatches(plugins)
		return nil
	}
	return plugins
}

// MessageWillBePosted threads a post JSON through every running plugin's
// hook. If a plugin rejects the post, iteration stops and (nil, true, reason)
// is returned. Otherwise the (possibly modified) post JSON is returned.
func (h *Host) MessageWillBePosted(ctx context.Context, post []byte) ([]byte, bool, string) {
	plugins := h.runningPlugins()
	defer releasePluginDispatches(plugins)
	current := post
	for _, lease := range plugins {
		p := lease.plugin
		if lease.runtime == RuntimeMattermostV1 {
			hooks, ok := h.mattermostHooks(lease, "MessageWillBePosted")
			if !ok {
				continue
			}
			var mmPost mmmodel.Post
			if err := json.Unmarshal(current, &mmPost); err != nil {
				h.logger.Warn("decode post for Mattermost plugin", "plugin", p.Manifest.ID, "err", err)
				continue
			}
			next, reason := hooks.MessageWillBePosted(&mmplugin.Context{}, &mmPost)
			if reason != "" {
				return nil, true, reason
			}
			if next != nil {
				raw, err := json.Marshal(next)
				if err != nil {
					h.logger.Warn("encode post from Mattermost plugin", "plugin", p.Manifest.ID, "err", err)
					continue
				}
				current = raw
			}
			continue
		}
		if lease.client == nil {
			continue
		}
		decision, err := lease.client.MessageWillBePosted(ctx, rpcbridge.PostEvent{Post: current})
		if err != nil {
			h.logger.Warn("MessageWillBePosted", "plugin", p.Manifest.ID, "err", err)
			continue
		}
		if decision == nil {
			continue
		}
		if decision.Rejected {
			return nil, true, decision.Reason
		}
		if len(decision.Post) > 0 {
			current = decision.Post
		}
	}
	return current, false, ""
}

// MessageHasBeenPosted fires the notification hook on every running plugin.
// Errors are logged but not propagated; the post has already been persisted.
func (h *Host) MessageHasBeenPosted(ctx context.Context, post []byte) {
	plugins := h.runningPlugins()
	defer releasePluginDispatches(plugins)
	for _, lease := range plugins {
		p := lease.plugin
		if lease.runtime == RuntimeMattermostV1 {
			hooks, ok := h.mattermostHooks(lease, "MessageHasBeenPosted")
			if !ok {
				continue
			}
			var mmPost mmmodel.Post
			if err := json.Unmarshal(post, &mmPost); err != nil {
				h.logger.Warn("decode posted message for Mattermost plugin", "plugin", p.Manifest.ID, "err", err)
				continue
			}
			hooks.MessageHasBeenPosted(&mmplugin.Context{}, &mmPost)
			continue
		}
		if lease.client == nil {
			continue
		}
		if err := lease.client.MessageHasBeenPosted(ctx, rpcbridge.PostEvent{Post: post}); err != nil {
			h.logger.Warn("MessageHasBeenPosted", "plugin", p.Manifest.ID, "err", err)
		}
	}
}

// ExecuteCommand offers the command to each running plugin in registration
// order. The first plugin that sets Handled=true wins; subsequent plugins
// don't see the command. Returns (reply, true, nil) on hit, (nil, false, nil)
// if no plugin handled it, or (nil, false, err) on RPC failure.
func (h *Host) ExecuteCommand(ctx context.Context, trigger, arg, channelID, userID string) (*rpcbridge.CommandReply, bool, error) {
	plugins := h.runningPlugins()
	defer releasePluginDispatches(plugins)
	for _, lease := range plugins {
		p := lease.plugin
		if lease.runtime == RuntimeMattermostV1 {
			if !p.acceptsCommand(trigger) {
				continue
			}
			hooks, ok := h.mattermostHooks(lease, "ExecuteCommand")
			if !ok {
				continue
			}
			commandText := "/" + strings.TrimPrefix(strings.TrimSpace(trigger), "/")
			if argument := strings.TrimSpace(arg); argument != "" {
				commandText += " " + argument
			}
			response, appErr := hooks.ExecuteCommand(&mmplugin.Context{}, &mmmodel.CommandArgs{
				Command:   commandText,
				ChannelId: channelID,
				UserId:    userID,
			})
			if appErr != nil {
				h.logger.Warn("ExecuteCommand", "plugin", p.Manifest.ID, "err", appErr)
				continue
			}
			if response != nil {
				return &rpcbridge.CommandReply{
					Handled: true, Text: response.Text,
					ResponseType: response.ResponseType,
				}, true, nil
			}
			continue
		}
		if lease.client == nil {
			continue
		}
		reply, err := lease.client.ExecuteCommand(ctx, rpcbridge.CommandArgs{
			Trigger:   trigger,
			Arg:       arg,
			ChannelID: channelID,
			UserID:    userID,
		})
		if err != nil {
			// An RPC error from a plugin shouldn't poison the whole
			// command; log and skip so the next plugin gets a chance.
			h.logger.Warn("ExecuteCommand", "plugin", p.Manifest.ID, "err", err)
			continue
		}
		if reply != nil && reply.Handled {
			return reply, true, nil
		}
	}
	return nil, false, nil
}

func (h *Host) mattermostHooks(lease *pluginDispatchLease, hookName string) (mmplugin.Hooks, bool) {
	if lease == nil || lease.plugin == nil || lease.runtime != RuntimeMattermostV1 || lease.hooks == nil {
		return nil, false
	}
	hooks := lease.hooks
	if _, ok := lease.plugin.mmImplemented[hookName]; ok {
		return hooks, true
	}
	return nil, false
}

// ServePluginHTTP dispatches a request after the HTTP layer has authenticated
// it, stripped the /plugins/{id} prefix and replaced Mattermost-User-ID with a
// trusted value. The plugin itself remains responsible for endpoint-level
// authorization, matching the Mattermost contract.
func (h *Host) ServePluginHTTP(w http.ResponseWriter, r *http.Request, pluginID string) {
	if h.runtimePoisoned.Load() {
		http.Error(w, "plugin runtime requires restart", http.StatusServiceUnavailable)
		return
	}
	p, ok := h.plugin(pluginID)
	if !ok {
		http.Error(w, "plugin is not running", http.StatusNotFound)
		return
	}
	lease, ok := p.acquireDispatch()
	if !ok {
		http.Error(w, "plugin is not running", http.StatusNotFound)
		return
	}
	defer lease.release()
	if lease.runtime == RuntimeMattermostV1 {
		hooks, implemented := h.mattermostHooks(lease, "ServeHTTP")
		if !implemented {
			http.Error(w, "plugin does not provide HTTP routes", http.StatusNotFound)
			return
		}
		hooks.ServeHTTP(&mmplugin.Context{}, w, r)
		return
	}
	type nativeHTTPClient interface {
		ServeHTTP(context.Context, rpcbridge.ServeHTTPReq) (*rpcbridge.ServeHTTPResp, error)
	}
	client, ok := lease.client.(nativeHTTPClient)
	if !ok {
		http.Error(w, "plugin does not provide HTTP routes", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid plugin request body", http.StatusBadRequest)
		return
	}
	headers := make(map[string]string, len(r.Header))
	for key, values := range r.Header {
		headers[key] = strings.Join(values, ",")
	}
	response, err := client.ServeHTTP(r.Context(), rpcbridge.ServeHTTPReq{
		Method: r.Method, Path: r.URL.RequestURI(), Headers: headers, Body: body,
	})
	if err != nil {
		h.logger.Warn("plugin HTTP call failed", "plugin", pluginID, "err", err)
		http.Error(w, "plugin request failed", http.StatusBadGateway)
		return
	}
	for key, value := range response.Headers {
		if isHopByHopHeader(key) {
			continue
		}
		w.Header().Set(key, value)
	}
	status := response.Status
	if status < 100 || status > 599 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(response.Body)
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

type WebappBundle struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	URL     string `json:"url"`
}

func (h *Host) WebappBundles() []WebappBundle {
	plugins := h.runningPlugins()
	defer releasePluginDispatches(plugins)
	bundles := []WebappBundle{}
	for _, lease := range plugins {
		p := lease.plugin
		if p.Manifest.Webapp == nil {
			continue
		}
		bundles = append(bundles, WebappBundle{
			ID: p.Manifest.ID, Version: p.Manifest.Version, URL: "/plugins/" + p.Manifest.ID + "/webapp.js",
		})
	}
	return bundles
}

// OpenWebappBundle pins the selected plugin generation until the caller has
// finished serving the already-open file. This prevents a same-ID replacement
// from swapping the path between authorization and open.
func (h *Host) OpenWebappBundle(pluginID string) (*os.File, os.FileInfo, func(), bool) {
	if h.runtimePoisoned.Load() {
		return nil, nil, nil, false
	}
	p, ok := h.plugin(pluginID)
	if !ok || p.Manifest.Webapp == nil {
		return nil, nil, nil, false
	}
	lease, ok := p.acquireDispatch()
	if !ok {
		return nil, nil, nil, false
	}
	path, err := securePluginPath(p.Dir, p.Manifest.Webapp.BundlePath)
	if err != nil {
		lease.release()
		return nil, nil, nil, false
	}
	file, err := os.Open(path)
	if err != nil {
		lease.release()
		return nil, nil, nil, false
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		lease.release()
		return nil, nil, nil, false
	}
	return file, info, lease.release, true
}

func (h *Host) Shutdown() {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.mu.RLock()
	plugins := make([]*Plugin, 0, len(h.plugins))
	for _, id := range h.order {
		p := h.plugins[id]
		if p != nil {
			plugins = append(plugins, p)
		}
	}
	h.mu.RUnlock()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	for _, p := range plugins {
		if err := p.closeDispatchAdmission(shutdownCtx); err != nil {
			h.runtimePoisoned.Store(true)
			h.logger.Error("plugin shutdown could not drain calls", "plugin", p.Manifest.ID, "err", err)
			continue
		}
		h.deactivatePlugin(shutdownCtx, p)
	}
	if h.mmEnv != nil && !h.runtimePoisoned.Load() {
		h.mmEnv.Shutdown()
	}
	if h.mmLogger != nil && !h.runtimePoisoned.Load() {
		_ = h.mmLogger.Shutdown()
	}
}
