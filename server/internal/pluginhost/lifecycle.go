package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/rpcbridge"
	"github.com/jackc/pgx/v5"
	mmplugin "github.com/mattermost/mattermost/server/public/plugin"
)

var (
	ErrPluginNotFound = errors.New("plugin is not installed")
	ErrPluginExists   = errors.New("plugin is already installed")
	ErrPluginBusy     = errors.New("plugin install or replacement is in progress")
)

const pluginInstallFinalizeTimeout = 2 * time.Minute

type InstallResult struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Enabled  bool   `json:"enabled"`
	Runtime  string `json:"runtime"`
	SHA256   string `json:"sha256"`
	Replaced bool   `json:"replaced"`
}

// Install accepts an ordinary Mattermost plugin tar.gz, validates it in a
// private staging tree, atomically swaps the live directory and activates it.
// Existing installations are restored if activation of an upgrade fails.
func (h *Host) Install(ctx context.Context, archive io.Reader, actorID string, replace bool) (*InstallResult, error) {
	if h.db == nil || h.mmEnv == nil {
		return nil, errors.New("managed plugin runtime is unavailable")
	}
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.runtimePoisoned.Load() {
		return nil, ErrPluginRuntimeStuck
	}

	bundleFile, err := os.CreateTemp(h.dir, ".moyro-plugin-upload-")
	if err != nil {
		return nil, fmt.Errorf("stage plugin upload: %w", err)
	}
	bundlePath := bundleFile.Name()
	defer os.Remove(bundlePath)
	hash := sha256.New()
	limit := DefaultBundleLimits().MaxBundleBytes
	written, copyErr := io.Copy(io.MultiWriter(bundleFile, hash), io.LimitReader(archive, limit+1))
	closeErr := bundleFile.Close()
	if copyErr != nil {
		return nil, fmt.Errorf("read plugin upload: %w", copyErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close plugin upload: %w", closeErr)
	}
	if written > limit {
		return nil, fmt.Errorf("plugin bundle exceeds %d bytes", limit)
	}
	bundleFile, err = os.Open(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("reopen plugin upload: %w", err)
	}
	staged, err := ExtractBundleToStaging(ctx, h.dir, bundleFile)
	_ = bundleFile.Close()
	if err != nil {
		return nil, err
	}
	defer staged.Cleanup()

	id := staged.Manifest.ID
	target, err := securePluginPath(h.dir, id)
	if err != nil || filepath.Dir(target) != filepath.Clean(h.dir) {
		return nil, fmt.Errorf("invalid plugin id %q", id)
	}
	old, existed := h.plugin(id)
	if existed && !replace {
		return nil, ErrPluginExists
	}
	targetExists := false
	if _, statErr := os.Lstat(target); statErr == nil {
		targetExists = true
		if !replace {
			return nil, ErrPluginExists
		}
	} else if !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("inspect current plugin: %w", statErr)
	}
	// Once the durable rollback marker is created, client disconnects must not
	// cancel the commit/rollback path. Values are retained for tracing, but the
	// finalization has its own strict upper bound.
	operationCtx, cancelOperation := context.WithTimeout(context.WithoutCancel(ctx), pluginInstallFinalizeTimeout)
	defer cancelOperation()

	if old != nil {
		if err := old.closeDispatchAdmission(operationCtx); err != nil {
			old.openDispatchAdmission()
			return nil, fmt.Errorf("prepare plugin replacement: %w", err)
		}
	}
	journal, err := h.beginInstallTransaction(operationCtx, id, targetExists)
	if err != nil {
		if errors.Is(err, ErrPluginRuntimeStuck) {
			// The database could not determine whether the durable rollback
			// marker committed. Keep the old instance hidden and require a
			// restart instead of exposing it beside an unresolved journal.
			return nil, fmt.Errorf("prepare plugin install: %w", err)
		}
		if old != nil {
			old.openDispatchAdmission()
		}
		return nil, fmt.Errorf("prepare plugin install: %w", err)
	}
	backup := ""
	if journal.BackupName != "" {
		backup = filepath.Join(h.dir, journal.BackupName)
	}
	var candidate *Plugin
	rollback := func(cause error) error {
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), pluginInstallFinalizeTimeout)
		defer cancelRollback()
		if candidate != nil {
			h.deactivatePlugin(rollbackCtx, candidate)
			if drainErr := candidate.closeDispatchAdmission(rollbackCtx); drainErr != nil {
				h.runtimePoisoned.Store(true)
				return errors.Join(
					ErrPluginRuntimeStuck,
					cause,
					fmt.Errorf("drain replacement plugin API calls: %w", drainErr),
				)
			}
			if h.mmEnv != nil {
				h.mmEnv.RemovePlugin(id)
			}
			h.unregister(id)
		}
		if restoreErr := h.restoreInstallFiles(rollbackCtx, journal); restoreErr != nil {
			h.runtimePoisoned.Store(true)
			return errors.Join(
				ErrPluginRuntimeStuck,
				cause,
				fmt.Errorf("rollback plugin files: %w", restoreErr),
			)
		}
		if restoreErr := h.restoreInstallDatabase(rollbackCtx, journal); restoreErr != nil {
			h.runtimePoisoned.Store(true)
			return errors.Join(
				ErrPluginRuntimeStuck,
				cause,
				fmt.Errorf("rollback plugin data: %w", restoreErr),
			)
		}
		if old != nil {
			h.register(old)
			if old.runtimeSnapshot().enabled {
				if activateErr := h.activatePlugin(rollbackCtx, old); activateErr != nil {
					return errors.Join(cause, fmt.Errorf("rollback plugin reactivation: %w", activateErr))
				}
			}
			old.openDispatchAdmission()
		}
		return cause
	}
	// The first snapshot makes a crash during OnDeactivate recoverable. The
	// old instance is already hidden from new dispatch; Deactivate drains and
	// tears down its RPC process, then a transactional refresh captures any
	// final configuration/KV writes made by in-flight work or OnDeactivate.
	if existed {
		h.deactivatePlugin(operationCtx, old)
		if err := old.closeDispatchAdmission(operationCtx); err != nil {
			return nil, rollback(fmt.Errorf("drain previous plugin API calls: %w", err))
		}
	}
	if err := h.refreshInstallSnapshot(operationCtx, journal); err != nil {
		return nil, rollback(err)
	}

	if targetExists {
		if err := h.setInstallPhase(operationCtx, journal, installPhaseMovingOld); err != nil {
			return nil, rollback(err)
		}
		if err := os.Rename(target, backup); err != nil {
			return nil, rollback(fmt.Errorf("backup current plugin: %w", err))
		}
		if err := syncDirectory(h.dir); err != nil {
			return nil, rollback(fmt.Errorf("sync current plugin backup: %w", err))
		}
		if err := h.setInstallPhase(operationCtx, journal, installPhaseOldBackedUp); err != nil {
			return nil, rollback(err)
		}
	}

	if err := h.setInstallPhase(operationCtx, journal, installPhasePromoting); err != nil {
		return nil, rollback(err)
	}
	if err := os.Rename(staged.PluginDir, target); err != nil {
		return nil, rollback(fmt.Errorf("activate plugin directory: %w", err))
	}
	if err := syncDirectory(h.dir); err != nil {
		return nil, rollback(fmt.Errorf("sync plugin directory activation: %w", err))
	}
	candidate = &Plugin{
		Manifest: staged.Manifest, Dir: target, State: "installed",
		Runtime: RuntimeMattermostV1, Enabled: true,
	}
	candidate.transitioning.Store(true)
	h.register(candidate)
	checksum := hex.EncodeToString(hash.Sum(nil))
	// A new plugin needs a row before OnActivate so configuration/KV calls can
	// succeed. During replacement, keep the old row intact until activation has
	// succeeded; this makes a failed upgrade restore the prior metadata too.
	if !existed {
		if err := h.persistPlugin(operationCtx, candidate, checksum, actorID); err != nil {
			return nil, rollback(fmt.Errorf("persist plugin installation: %w", err))
		}
	}
	if err := h.activatePlugin(operationCtx, candidate); err != nil {
		candidate.dispatch.mu.Lock()
		candidate.State = "failed"
		candidate.Error = err.Error()
		candidate.dispatch.mu.Unlock()
		if errors.Is(err, ErrPluginRuntimeStuck) {
			// The upstream activation goroutine is still running. Mutating the
			// target or snapshot concurrently would be unsafe, so leave the
			// durable marker intact and let startup perform recovery.
			return nil, fmt.Errorf("activate plugin %s: %w", id, err)
		}
		return nil, rollback(fmt.Errorf("activate plugin %s: %w", id, err))
	}
	if existed {
		if err := h.persistPlugin(operationCtx, candidate, checksum, actorID); err != nil {
			return nil, rollback(fmt.Errorf("persist plugin installation: %w", err))
		}
	}
	if err := h.persistState(operationCtx, candidate); err != nil {
		return nil, rollback(fmt.Errorf("persist plugin activation: %w", err))
	}
	// The extracted tree and both parent-directory renames are durable before
	// this marker deletion. Its deletion is the linearization point: while the
	// marker exists startup rolls back; after it is gone startup keeps the new
	// plugin even if cleanup was interrupted.
	if err := h.commitInstallTransaction(operationCtx, journal); err != nil {
		if errors.Is(err, ErrPluginRuntimeStuck) {
			// The commit point cannot be reconciled. Neither rollback nor
			// publication is safe until startup inspects the durable marker.
			return nil, err
		}
		return nil, rollback(err)
	}
	candidate.openDispatchAdmission()
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			h.logger.Warn("committed plugin backup cleanup failed", "plugin", id, "path", backup, "err", err)
		} else if err := syncDirectory(h.dir); err != nil {
			h.logger.Warn("committed plugin backup directory sync failed", "plugin", id, "err", err)
		}
	}
	status := candidate.runtimeSnapshot()
	return &InstallResult{
		ID: id, Version: candidate.Manifest.Version, State: status.state, Enabled: status.enabled,
		Runtime: candidate.Runtime, SHA256: checksum, Replaced: existed || targetExists,
	}, nil
}

func (h *Host) Enable(ctx context.Context, id string) (*Plugin, error) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.runtimePoisoned.Load() {
		return nil, ErrPluginRuntimeStuck
	}
	p, ok := h.plugin(id)
	if !ok {
		return nil, ErrPluginNotFound
	}
	if err := p.closeDispatchAdmission(ctx); err != nil {
		p.openDispatchAdmission()
		return nil, err
	}
	p.dispatch.mu.Lock()
	p.Enabled = true
	p.dispatch.mu.Unlock()
	if err := h.activatePlugin(ctx, p); err != nil {
		p.dispatch.mu.Lock()
		p.State = "failed"
		p.Error = err.Error()
		p.dispatch.mu.Unlock()
		h.persistState(ctx, p)
		p.openDispatchAdmission()
		return nil, err
	}
	if err := h.persistState(ctx, p); err != nil {
		p.openDispatchAdmission()
		return nil, err
	}
	p.openDispatchAdmission()
	return p, nil
}

func (h *Host) Disable(ctx context.Context, id string) (*Plugin, error) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.runtimePoisoned.Load() {
		return nil, ErrPluginRuntimeStuck
	}
	p, ok := h.plugin(id)
	if !ok {
		return nil, ErrPluginNotFound
	}
	if err := p.closeDispatchAdmission(ctx); err != nil {
		p.openDispatchAdmission()
		return nil, err
	}
	h.deactivatePlugin(ctx, p)
	if err := p.closeDispatchAdmission(ctx); err != nil {
		if p.runtimeSnapshot().enabled {
			_ = h.activatePlugin(ctx, p)
		}
		p.openDispatchAdmission()
		return nil, err
	}
	p.dispatch.mu.Lock()
	p.Enabled = false
	p.State = "installed"
	p.Error = ""
	p.dispatch.mu.Unlock()
	if err := h.persistState(ctx, p); err != nil {
		p.openDispatchAdmission()
		return nil, err
	}
	p.openDispatchAdmission()
	return p, nil
}

func (h *Host) Delete(ctx context.Context, id string) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.runtimePoisoned.Load() {
		return ErrPluginRuntimeStuck
	}
	p, ok := h.plugin(id)
	if !ok {
		return ErrPluginNotFound
	}
	quarantine, err := os.MkdirTemp(h.dir, ".moyro-plugin-delete-")
	if err != nil {
		return err
	}
	if err := os.Remove(quarantine); err != nil {
		return err
	}
	if err := p.closeDispatchAdmission(ctx); err != nil {
		p.openDispatchAdmission()
		return err
	}
	wasEnabled := p.runtimeSnapshot().enabled
	h.deactivatePlugin(ctx, p)
	if err := p.closeDispatchAdmission(ctx); err != nil {
		if wasEnabled {
			_ = h.activatePlugin(ctx, p)
		}
		p.openDispatchAdmission()
		return err
	}
	if err := os.Rename(p.Dir, quarantine); err != nil {
		if wasEnabled {
			_ = h.activatePlugin(ctx, p)
		}
		p.openDispatchAdmission()
		return fmt.Errorf("quarantine plugin: %w", err)
	}
	if h.db != nil {
		if _, err := h.db.Pool.Exec(ctx, `DELETE FROM plugins WHERE id=$1`, id); err != nil {
			_ = os.Rename(quarantine, p.Dir)
			if wasEnabled {
				_ = h.activatePlugin(ctx, p)
			}
			p.openDispatchAdmission()
			return err
		}
	}
	if h.mmEnv != nil {
		h.mmEnv.RemovePlugin(id)
	}
	h.mu.Lock()
	delete(h.plugins, id)
	for index, pluginID := range h.order {
		if pluginID == id {
			h.order = append(h.order[:index], h.order[index+1:]...)
			break
		}
	}
	h.mu.Unlock()
	return os.RemoveAll(quarantine)
}

func (h *Host) activatePlugin(ctx context.Context, p *Plugin) error {
	if p == nil {
		return ErrPluginNotFound
	}
	status := p.runtimeSnapshot()
	if status.state == "running" {
		return nil
	}
	if p.Runtime == RuntimeMattermostV1 {
		p.apiActivating.Store(true)
		err := func() error {
			defer p.apiActivating.Store(false)
			return h.activateMattermostPlugin(ctx, p)
		}()
		if err != nil {
			return err
		}
		var hooks mmplugin.Hooks
		implementedHooks := map[string]struct{}{}
		if p.Manifest.Server != nil {
			var err error
			hooks, err = h.mmEnv.HooksForPlugin(p.Manifest.ID)
			if err != nil {
				h.deactivateMattermostEnvironmentPlugin(p)
				return fmt.Errorf("bind Mattermost plugin generation: %w", err)
			}
			implemented, err := hooks.Implemented()
			if err != nil {
				h.deactivateMattermostEnvironmentPlugin(p)
				return fmt.Errorf("inventory Mattermost plugin hooks: %w", err)
			}
			for _, name := range implemented {
				implementedHooks[name] = struct{}{}
			}
		}
		p.dispatch.mu.Lock()
		p.mmHooks = hooks
		p.mmImplemented = implementedHooks
		p.State, p.Error = "running", ""
		p.dispatch.mu.Unlock()
		return nil
	}
	if p.Manifest.Server == nil {
		p.dispatch.mu.Lock()
		p.State, p.Error = "running", ""
		p.dispatch.mu.Unlock()
		return nil
	}
	executable := p.Manifest.ExecutablePath(p.Dir, runtime.GOOS, runtime.GOARCH)
	if executable == "" {
		return fmt.Errorf("no executable for %s-%s", runtime.GOOS, runtime.GOARCH)
	}
	client, err := rpcbridge.Launch(ctx, executable, h.logger.With("plugin", p.Manifest.ID))
	if err != nil {
		return err
	}
	if err := client.OnActivate(ctx); err != nil {
		_ = client.Close()
		return err
	}
	p.dispatch.mu.Lock()
	p.client = client
	p.State, p.Error = "running", ""
	p.dispatch.mu.Unlock()
	return nil
}
func (h *Host) deactivatePlugin(ctx context.Context, p *Plugin) {
	if p == nil {
		return
	}
	p.dispatch.mu.Lock()
	if p.State != "running" {
		p.dispatch.mu.Unlock()
		return
	}
	runtimeKind := p.Runtime
	client := p.client
	p.client = nil
	p.mmHooks = nil
	p.mmImplemented = nil
	p.State = "installed"
	p.dispatch.mu.Unlock()
	if runtimeKind == RuntimeMattermostV1 {
		if h.mmEnv != nil {
			h.deactivateMattermostEnvironmentPlugin(p)
		}
	} else if client != nil {
		_ = client.OnDeactivate(ctx)
		_ = client.Close()
	}
}

func (h *Host) deactivateMattermostEnvironmentPlugin(p *Plugin) {
	if h == nil || h.mmEnv == nil || p == nil || p.Manifest == nil {
		return
	}
	p.apiDeactivating.Store(true)
	defer p.apiDeactivating.Store(false)
	h.mmEnv.Deactivate(p.Manifest.ID)
}

func (h *Host) loadRuntimeMetadata(ctx context.Context, id string) (string, bool, error) {
	if h.db == nil {
		return RuntimeMoyroV1, true, nil
	}
	var runtimeKind string
	var enabled bool
	err := h.db.Pool.QueryRow(ctx, `SELECT runtime_kind, enabled FROM plugins WHERE id=$1`, id).Scan(&runtimeKind, &enabled)
	if err == pgx.ErrNoRows {
		return RuntimeMoyroV1, true, nil
	}
	if err != nil {
		return "", false, err
	}
	if runtimeKind == "" {
		runtimeKind = RuntimeMoyroV1
	}
	return runtimeKind, enabled, nil
}

func (h *Host) persistPlugin(ctx context.Context, p *Plugin, checksum, actorID string) error {
	if h.db == nil || p == nil {
		return nil
	}
	raw, err := json.Marshal(p.Manifest)
	if err != nil {
		return err
	}
	status := p.runtimeSnapshot()
	now := time.Now().UnixMilli()
	_, err = h.db.Pool.Exec(ctx, `
		INSERT INTO plugins
			(id,version,state,manifest,create_at,update_at,enabled,runtime_kind,
			 bundle_sha256,last_error,installed_by,installed_at,activated_at)
		VALUES ($1,$2,$3,$4,$5::BIGINT,$5::BIGINT,$6,$7,$8,$9,$10,$5::BIGINT,
		        CASE WHEN $3::TEXT='running' THEN $5::BIGINT ELSE 0::BIGINT END)
		ON CONFLICT (id) DO UPDATE SET
			version=EXCLUDED.version, state=EXCLUDED.state,
			manifest=EXCLUDED.manifest, update_at=EXCLUDED.update_at,
			enabled=EXCLUDED.enabled, runtime_kind=EXCLUDED.runtime_kind,
			bundle_sha256=CASE WHEN EXCLUDED.bundle_sha256='' THEN plugins.bundle_sha256 ELSE EXCLUDED.bundle_sha256 END,
			last_error=EXCLUDED.last_error,
			installed_by=CASE WHEN EXCLUDED.installed_by='' THEN plugins.installed_by ELSE EXCLUDED.installed_by END,
			installed_at=CASE WHEN EXCLUDED.bundle_sha256='' THEN plugins.installed_at ELSE EXCLUDED.installed_at END,
			activated_at=CASE WHEN EXCLUDED.state='running' THEN EXCLUDED.update_at ELSE plugins.activated_at END
	`, p.Manifest.ID, p.Manifest.Version, status.state, raw, now, status.enabled, p.Runtime, checksum, status.err, actorID)
	return err
}

func (h *Host) persistState(ctx context.Context, p *Plugin) error {
	if h.db == nil || p == nil {
		return nil
	}
	status := p.runtimeSnapshot()
	now := time.Now().UnixMilli()
	_, err := h.db.Pool.Exec(ctx, `
		UPDATE plugins SET state=$2, enabled=$3, last_error=$4, update_at=$5,
		activated_at=CASE WHEN $2='running' THEN $5 ELSE activated_at END
		WHERE id=$1
	`, p.Manifest.ID, status.state, status.enabled, status.err, now)
	return err
}

func (h *Host) loadConfiguration(pluginID string) (map[string]any, error) {
	defaults := h.configurationDefaults(pluginID)
	if h.db == nil {
		return defaults, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), pluginAPITimeout)
	defer cancel()
	var keyID string
	var nonce, ciphertext []byte
	err := h.db.Pool.QueryRow(ctx, `
		SELECT config_key_id, config_nonce, config_ciphertext FROM plugins WHERE id=$1
	`, pluginID).Scan(&keyID, &nonce, &ciphertext)
	if err != nil {
		return nil, err
	}
	if keyID == "" || len(ciphertext) == 0 {
		return defaults, nil
	}
	if h.secrets == nil {
		return nil, errors.New("plugin configuration encryption is unavailable")
	}
	plain, err := h.secrets.Decrypt(pluginConfigContext(pluginID), keyID, nonce, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt plugin configuration: %w", err)
	}
	var saved map[string]any
	if err := json.Unmarshal(plain, &saved); err != nil {
		return nil, fmt.Errorf("decode plugin configuration: %w", err)
	}
	for key, value := range saved {
		defaults[key] = value
	}
	return defaults, nil
}

func (h *Host) Configuration(pluginID string) (map[string]any, json.RawMessage, error) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	if h.runtimePoisoned.Load() {
		return nil, nil, ErrPluginRuntimeStuck
	}
	p, ok := h.plugin(pluginID)
	if !ok {
		return nil, nil, ErrPluginNotFound
	}
	if p.transitioning.Load() {
		return nil, nil, ErrPluginBusy
	}
	config, err := h.loadConfiguration(pluginID)
	if err != nil {
		return nil, nil, err
	}
	return config, append(json.RawMessage(nil), p.Manifest.SettingsSchema...), nil
}

func (h *Host) UpdateConfiguration(ctx context.Context, pluginID string, config map[string]any) error {
	h.lifecycle.Lock()
	if h.runtimePoisoned.Load() {
		h.lifecycle.Unlock()
		return ErrPluginRuntimeStuck
	}
	p, ok := h.plugin(pluginID)
	if !ok {
		h.lifecycle.Unlock()
		return ErrPluginNotFound
	}
	p.configuration.Lock()
	var lease *pluginDispatchLease
	if p.Runtime == RuntimeMattermostV1 {
		lease, _ = p.acquireDispatch()
	}
	mutation, err := h.persistConfigurationMutation(ctx, pluginID, config, false)
	h.lifecycle.Unlock()
	if err != nil {
		if lease != nil {
			lease.release()
		}
		p.configuration.Unlock()
		return err
	}
	// Never hold the lifecycle mutex while calling untrusted plugin code. The
	// generation lease prevents replacement/deletion from overtaking the hook
	// and any compensating database restore.
	applyErr, completed := h.notifyConfigurationChangeWithLease(ctx, lease)
	if applyErr != nil {
		restoreErr := h.restoreConfigurationMutation(context.WithoutCancel(ctx), mutation)
		if restoreErr != nil {
			h.runtimePoisoned.Store(true)
			if completed && lease != nil {
				lease.release()
			}
			p.configuration.Unlock()
			return errors.Join(
				ErrPluginRuntimeStuck,
				applyErr,
				fmt.Errorf("restore previous plugin configuration: %w", restoreErr),
			)
		}
		if !completed {
			h.runtimePoisoned.Store(true)
			p.configuration.Unlock()
			return errors.Join(
				ErrPluginRuntimeStuck,
				applyErr,
				errors.New("previous plugin configuration was restored but its live reapply is unsafe after hook timeout"),
			)
		}
		reapplyErr, reapplyCompleted := h.notifyConfigurationChangeWithLease(context.WithoutCancel(ctx), lease)
		if reapplyErr != nil {
			h.runtimePoisoned.Store(true)
		}
		if reapplyCompleted && lease != nil {
			lease.release()
		}
		p.configuration.Unlock()
		if reapplyErr != nil {
			return errors.Join(
				ErrPluginRuntimeStuck,
				applyErr,
				fmt.Errorf("reapply previous plugin configuration: %w", reapplyErr),
			)
		}
		return applyErr
	}
	if completed && lease != nil {
		lease.release()
	}
	p.configuration.Unlock()
	return nil
}

func (h *Host) updateConfiguration(ctx context.Context, pluginID string, config map[string]any, allowTransition bool) error {
	// SavePluginConfig is callable by the plugin itself, including from
	// OnActivate and OnConfigurationChange. It is persistence-only: recursively
	// notifying the same generation would deadlock the RPC callback and differs
	// from the plugin-owned in-memory update semantics. Administrator-initiated
	// updates use UpdateConfiguration above, which owns apply/rollback/reapply.
	_, err := h.persistConfiguration(ctx, pluginID, config, allowTransition)
	return err
}

func (h *Host) persistConfiguration(ctx context.Context, pluginID string, config map[string]any, allowTransition bool) (*Plugin, error) {
	mutation, err := h.persistConfigurationMutation(ctx, pluginID, config, allowTransition)
	if err != nil {
		return nil, err
	}
	return mutation.plugin, nil
}

type pluginConfigurationEnvelope struct {
	keyID      string
	nonce      []byte
	ciphertext []byte
	updateAt   int64
}

type pluginConfigurationMutation struct {
	plugin   *Plugin
	pluginID string
	previous pluginConfigurationEnvelope
	current  pluginConfigurationEnvelope
}

func (h *Host) persistConfigurationMutation(ctx context.Context, pluginID string, config map[string]any, allowTransition bool) (*pluginConfigurationMutation, error) {
	p, ok := h.plugin(pluginID)
	if !ok {
		return nil, ErrPluginNotFound
	}
	if p.transitioning.Load() && !allowTransition {
		return nil, ErrPluginBusy
	}
	if h.db == nil || h.secrets == nil {
		return nil, errors.New("plugin configuration persistence is unavailable")
	}
	if config == nil {
		config = map[string]any{}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode plugin configuration: %w", err)
	}
	keyID, nonce, ciphertext, err := h.secrets.Encrypt(pluginConfigContext(pluginID), raw)
	if err != nil {
		return nil, err
	}
	tx, err := h.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	mutation := &pluginConfigurationMutation{
		plugin: p, pluginID: pluginID,
		current: pluginConfigurationEnvelope{
			keyID: keyID, nonce: append([]byte(nil), nonce...), ciphertext: append([]byte(nil), ciphertext...),
		},
	}
	if err := tx.QueryRow(ctx, `
		SELECT config_key_id,config_nonce,config_ciphertext,update_at
		FROM plugins WHERE id=$1 FOR UPDATE
	`, pluginID).Scan(
		&mutation.previous.keyID,
		&mutation.previous.nonce,
		&mutation.previous.ciphertext,
		&mutation.previous.updateAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPluginNotFound
		}
		return nil, err
	}
	mutation.current.updateAt = time.Now().UnixMilli()
	tag, err := tx.Exec(ctx, `
		UPDATE plugins SET config_key_id=$2, config_nonce=$3, config_ciphertext=$4, update_at=$5
		WHERE id=$1
	`, pluginID, mutation.current.keyID, mutation.current.nonce, mutation.current.ciphertext, mutation.current.updateAt)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrPluginNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return mutation, nil
}

func (h *Host) restoreConfigurationMutation(ctx context.Context, mutation *pluginConfigurationMutation) error {
	if h.db == nil || mutation == nil {
		return errors.New("plugin configuration rollback is unavailable")
	}
	rollbackCtx, cancel := context.WithTimeout(ctx, pluginAPITimeout)
	defer cancel()
	tag, err := h.db.Pool.Exec(rollbackCtx, `
		UPDATE plugins
		SET config_key_id=$2,config_nonce=$3,config_ciphertext=$4,update_at=$5
		WHERE id=$1
		  AND config_key_id=$6
		  AND config_nonce IS NOT DISTINCT FROM $7::BYTEA
		  AND config_ciphertext IS NOT DISTINCT FROM $8::BYTEA
	`, mutation.pluginID,
		mutation.previous.keyID, mutation.previous.nonce, mutation.previous.ciphertext, mutation.previous.updateAt,
		mutation.current.keyID, mutation.current.nonce, mutation.current.ciphertext,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("configuration changed concurrently or plugin row is missing")
	}
	return nil
}

func (h *Host) notifyConfigurationChange(ctx context.Context, p *Plugin) error {
	lease, ok := p.acquireDispatch()
	if !ok {
		return nil
	}
	err, completed := h.notifyConfigurationChangeWithLease(ctx, lease)
	if completed {
		lease.release()
	}
	return err
}

// notifyConfigurationChangeWithLease leaves a completed lease owned by the
// caller so a failed hook can be rolled back before lifecycle replacement is
// allowed to proceed. On timeout, the hook goroutine releases the lease only
// after the untrusted call actually returns.
func (h *Host) notifyConfigurationChangeWithLease(ctx context.Context, lease *pluginDispatchLease) (error, bool) {
	if lease == nil || lease.runtime != RuntimeMattermostV1 {
		return nil, true
	}
	hookCtx, cancel := context.WithTimeout(ctx, pluginAPITimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		if hooks, implemented := h.mattermostHooks(lease, "OnConfigurationChange"); implemented {
			done <- hooks.OnConfigurationChange()
			return
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("apply plugin configuration: %w", err), true
		}
	case <-hookCtx.Done():
		go func() {
			<-done
			lease.release()
		}()
		return fmt.Errorf("apply plugin configuration: %w", hookCtx.Err()), false
	}
	return nil, true
}

func (h *Host) configurationDefaults(pluginID string) map[string]any {
	defaults := map[string]any{}
	p, ok := h.plugin(pluginID)
	if !ok || len(p.Manifest.SettingsSchema) == 0 {
		return defaults
	}
	var schema struct {
		Settings []struct {
			Key     string `json:"key"`
			Default any    `json:"default"`
		} `json:"settings"`
	}
	if json.Unmarshal(p.Manifest.SettingsSchema, &schema) != nil {
		return defaults
	}
	for _, setting := range schema.Settings {
		if strings.TrimSpace(setting.Key) != "" && setting.Default != nil {
			defaults[setting.Key] = setting.Default
		}
	}
	return defaults
}

func pluginConfigContext(pluginID string) string { return "plugins/" + pluginID + "/configuration" }

func securePluginPath(root, relative string) (string, error) {
	if relative == "" || strings.ContainsRune(relative, '\x00') || filepath.IsAbs(relative) {
		return "", errors.New("invalid plugin path")
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("plugin path escapes root")
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(filepath.Clean(root), target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("plugin path escapes root")
	}
	return target, nil
}
