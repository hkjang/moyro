package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/moddle/moddle/server/internal/rpcbridge"
)

type Plugin struct {
	Manifest *Manifest
	Dir      string
	State    string // installed, running, failed
	client   *rpcbridge.Client
}

type Host struct {
	dir     string
	logger  *slog.Logger
	mu      sync.RWMutex
	plugins map[string]*Plugin
	order   []string
}

func New(dir string, logger *slog.Logger) *Host {
	return &Host{dir: dir, logger: logger, plugins: map[string]*Plugin{}, order: []string{}}
}

func (h *Host) LoadAll(ctx context.Context) error {
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pluginDir := filepath.Join(h.dir, e.Name())
		if err := h.loadOne(ctx, pluginDir); err != nil {
			h.logger.Warn("plugin load failed", "dir", pluginDir, "err", err)
		}
	}
	return nil
}

func (h *Host) loadOne(ctx context.Context, dir string) error {
	m, err := LoadManifest(dir)
	if err != nil {
		return err
	}
	p := &Plugin{Manifest: m, Dir: dir, State: "installed"}

	if m.Server != nil {
		exec := m.ExecutablePath(dir, runtime.GOOS, runtime.GOARCH)
		if exec == "" {
			return fmt.Errorf("plugin %s: no executable for %s-%s", m.ID, runtime.GOOS, runtime.GOARCH)
		}
		client, err := rpcbridge.Launch(ctx, exec, h.logger.With("plugin", m.ID))
		if err != nil {
			p.State = "failed"
			h.register(p)
			return err
		}
		p.client = client
		p.State = "running"
		if err := client.OnActivate(ctx); err != nil {
			h.logger.Warn("OnActivate", "plugin", m.ID, "err", err)
		}
	}

	h.register(p)
	h.logger.Info("plugin loaded", "id", m.ID, "version", m.Version, "state", p.State)
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

func (h *Host) List() []map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]map[string]any, 0, len(h.plugins))
	for _, id := range h.order {
		p := h.plugins[id]
		if p == nil {
			continue
		}
		raw, _ := json.Marshal(p.Manifest)
		var mani map[string]any
		_ = json.Unmarshal(raw, &mani)
		out = append(out, map[string]any{
			"id":       p.Manifest.ID,
			"version":  p.Manifest.Version,
			"state":    p.State,
			"manifest": mani,
		})
	}
	return out
}

func (h *Host) runningPlugins() []*Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	plugins := make([]*Plugin, 0, len(h.plugins))
	for _, id := range h.order {
		p := h.plugins[id]
		if p != nil && p.State == "running" && p.client != nil {
			plugins = append(plugins, p)
		}
	}
	return plugins
}

// MessageWillBePosted threads a post JSON through every running plugin's
// hook. If a plugin rejects the post, iteration stops and (nil, true, reason)
// is returned. Otherwise the (possibly modified) post JSON is returned.
func (h *Host) MessageWillBePosted(ctx context.Context, post []byte) ([]byte, bool, string) {
	plugins := h.runningPlugins()
	current := post
	for _, p := range plugins {
		decision, err := p.client.MessageWillBePosted(ctx, rpcbridge.PostEvent{Post: current})
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
	for _, p := range plugins {
		if err := p.client.MessageHasBeenPosted(ctx, rpcbridge.PostEvent{Post: post}); err != nil {
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
	for _, p := range plugins {
		reply, err := p.client.ExecuteCommand(ctx, rpcbridge.CommandArgs{
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

func (h *Host) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range h.order {
		p := h.plugins[id]
		if p == nil {
			continue
		}
		if p.client != nil {
			_ = p.client.OnDeactivate(context.Background())
			_ = p.client.Close()
		}
	}
}
