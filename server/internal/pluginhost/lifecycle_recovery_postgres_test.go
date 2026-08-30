package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/store"
	mmplugin "github.com/mattermost/mattermost/server/public/plugin"
)

func TestLoadAllRecoversInterruptedInstallTransaction(t *testing.T) {
	for _, test := range []struct {
		name       string
		backupOld  bool
		promoteNew bool
		mutateDB   bool
	}{
		{
			name: "durable marker before filesystem mutation",
		},
		{
			name:      "old directory moved to journal backup",
			backupOld: true,
		},
		{
			name:       "new directory promoted while marker remains",
			backupOld:  true,
			promoteNew: true,
		},
		{
			name:       "new metadata configuration and KV written before commit",
			backupOld:  true,
			promoteNew: true,
			mutateDB:   true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newPluginRuntimeTestDB(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := store.Migrate(ctx, db); err != nil {
				t.Fatalf("migrate plugin recovery schema: %v", err)
			}

			manager, err := secrets.New(bytes.Repeat([]byte{0x65}, secrets.MasterKeySize))
			if err != nil {
				t.Fatalf("create secret manager: %v", err)
			}
			pluginRoot := t.TempDir()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			seedHost, err := NewWithRuntime(pluginRoot, db, manager, logger)
			if err != nil {
				t.Fatalf("create seed plugin host: %v", err)
			}

			const pluginID = "com.example.recovery"
			target := filepath.Join(pluginRoot, pluginID)

			oldManifest := writeRecoveryWebPlugin(t, target, pluginID, "1.0.0", "old")
			oldPlugin := &Plugin{
				Manifest: oldManifest,
				Dir:      target,
				State:    "running",
				Runtime:  RuntimeMattermostV1,
				Enabled:  true,
			}
			if err := seedHost.persistPlugin(ctx, oldPlugin, "old-sha256", "test-admin"); err != nil {
				t.Fatalf("persist old plugin metadata: %v", err)
			}
			seedHost.register(oldPlugin)
			if err := seedHost.UpdateConfiguration(ctx, pluginID, map[string]any{"Stable": "before"}); err != nil {
				t.Fatalf("persist old plugin configuration: %v", err)
			}
			seedAPI := &mattermostAPI{host: seedHost, db: db, pluginID: pluginID, generation: oldPlugin, logger: logger}
			if appErr := seedAPI.KVSet("stable", []byte("before")); appErr != nil {
				t.Fatalf("persist old plugin KV: %v", appErr)
			}

			journal, err := seedHost.beginInstallTransaction(ctx, pluginID, true)
			if err != nil {
				t.Fatalf("begin durable install transaction: %v", err)
			}
			for table, want := range map[string]int{
				"plugin_install_transactions":     1,
				"plugin_install_plugin_snapshots": 1,
				"plugin_install_kv_snapshots":     1,
			} {
				key := "transaction_id"
				if table == "plugin_install_transactions" {
					key = "id"
				}
				var count int
				if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+key+"=$1", journal.ID).Scan(&count); err != nil {
					t.Fatalf("count durable snapshot rows in %s: %v", table, err)
				}
				if count != want {
					t.Fatalf("%s rows for transaction = %d, want %d", table, count, want)
				}
			}
			backup := filepath.Join(pluginRoot, journal.BackupName)

			if test.backupOld {
				if err := os.Rename(target, backup); err != nil {
					t.Fatalf("inject old-directory backup phase: %v", err)
				}
			}
			if test.promoteNew {
				newManifest := writeRecoveryWebPlugin(t, target, pluginID, "2.0.0", "new")
				if test.mutateDB {
					newPlugin := &Plugin{
						Manifest: newManifest, Dir: target, State: "running",
						Runtime: RuntimeMattermostV1, Enabled: true,
					}
					seedHost.register(newPlugin)
					if err := seedHost.persistPlugin(ctx, newPlugin, "new-sha256", "test-admin"); err != nil {
						t.Fatalf("persist tentative replacement metadata: %v", err)
					}
					if err := seedHost.UpdateConfiguration(ctx, pluginID, map[string]any{"Stable": "corrupted", "UpgradeOnly": true}); err != nil {
						t.Fatalf("persist tentative replacement configuration: %v", err)
					}
					tentativeAPI := &mattermostAPI{
						host: seedHost, db: db, pluginID: pluginID,
						generation: newPlugin, logger: logger,
					}
					if appErr := tentativeAPI.KVDeleteAll(); appErr != nil {
						t.Fatalf("clear old KV during tentative activation: %v", appErr)
					}
					if appErr := tentativeAPI.KVSet("upgrade-only", []byte("new")); appErr != nil {
						t.Fatalf("persist tentative replacement KV: %v", appErr)
					}
				}
			}
			seedHost.Shutdown()

			recovered, err := NewWithRuntime(pluginRoot, db, manager, logger)
			if err != nil {
				t.Fatalf("create recovery host: %v", err)
			}
			t.Cleanup(recovered.Shutdown)
			if err := recovered.LoadAll(ctx); err != nil {
				t.Fatalf("LoadAll recovery: %v", err)
			}

			loaded, ok := recovered.plugin(pluginID)
			if !ok {
				t.Fatalf("recovered plugin %q is not registered", pluginID)
			}
			if loaded.Manifest.Version != "1.0.0" || loaded.State != "running" {
				t.Errorf("recovered plugin = version %q state %q, want version 1.0.0 running", loaded.Manifest.Version, loaded.State)
			}
			manifest, err := LoadManifest(target)
			if err != nil {
				t.Fatalf("load recovered live manifest: %v", err)
			}
			if manifest.Version != "1.0.0" {
				t.Errorf("live manifest version = %q, want 1.0.0", manifest.Version)
			}

			var databaseVersion, databaseSHA string
			if err := db.Pool.QueryRow(ctx, `SELECT version,bundle_sha256 FROM plugins WHERE id=$1`, pluginID).Scan(&databaseVersion, &databaseSHA); err != nil {
				t.Fatalf("read recovered metadata: %v", err)
			}
			if databaseVersion != "1.0.0" || databaseSHA != "old-sha256" {
				t.Errorf("database metadata = version %q sha %q, want version 1.0.0 sha old-sha256", databaseVersion, databaseSHA)
			}
			configuration, _, err := recovered.Configuration(pluginID)
			if err != nil {
				t.Fatalf("read recovered configuration: %v", err)
			}
			if configuration["Stable"] != "before" || configuration["UpgradeOnly"] != nil {
				t.Errorf("recovered configuration = %#v, want only Stable=before", configuration)
			}
			recoveredAPI := &mattermostAPI{host: recovered, db: db, pluginID: pluginID, generation: loaded, logger: logger}
			if value, appErr := recoveredAPI.KVGet("stable"); appErr != nil || string(value) != "before" {
				t.Errorf("recovered stable KV = %q, err %v; want before", value, appErr)
			}
			if value, appErr := recoveredAPI.KVGet("upgrade-only"); appErr != nil || value != nil {
				t.Errorf("recovered upgrade-only KV = %q, err %v; want absent", value, appErr)
			}
			if _, statErr := os.Lstat(backup); !os.IsNotExist(statErr) {
				t.Errorf("recovery left backup %q: %v", backup, statErr)
			}
			for _, table := range []string{
				"plugin_install_transactions",
				"plugin_install_plugin_snapshots",
				"plugin_install_kv_snapshots",
			} {
				var count int
				key := "transaction_id"
				if table == "plugin_install_transactions" {
					key = "id"
				}
				err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+key+"=$1", journal.ID).Scan(&count)
				if err != nil {
					t.Fatalf("count cleanup rows in %s: %v", table, err)
				}
				if count != 0 {
					t.Errorf("%s retained %d rows for committed recovery transaction", table, count)
				}
			}
		})
	}
}

func TestSuccessfulReplacementCleansInstallTransaction(t *testing.T) {
	db, ctx, host := newReplacementFailureHarness(t)
	const pluginID = "com.example.replacement-commit"
	installRecoveryBasePlugin(t, ctx, host, pluginID)

	result, err := host.Install(ctx, bytes.NewReader(webPluginBundle(t, pluginID, "2.0.0", "new")), "test-admin", true)
	if err != nil {
		t.Fatalf("replace plugin: %v", err)
	}
	if result.Version != "2.0.0" || result.State != "running" {
		t.Fatalf("replacement result = version %q state %q, want 2.0.0 running", result.Version, result.State)
	}
	for _, table := range []string{"plugin_install_transactions", "plugin_install_plugin_snapshots", "plugin_install_kv_snapshots"} {
		var count int
		query := "SELECT count(*) FROM " + table
		if err := db.Pool.QueryRow(ctx, query).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("successful replacement left %d rows in %s", count, table)
		}
	}
	matches, err := filepath.Glob(filepath.Join(host.dir, pluginBackupPrefix+"*"))
	if err != nil {
		t.Fatalf("glob replacement backups: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("successful replacement left backup paths: %v", matches)
	}
}

func TestFailedReplacementRestoresConfigurationAndKV(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper plugin executable uses a POSIX shell wrapper")
	}
	db, ctx, host := newReplacementFailureHarness(t)
	const pluginID = "com.example.rollback-state"

	installRecoveryBasePlugin(t, ctx, host, pluginID)
	if err := host.UpdateConfiguration(ctx, pluginID, map[string]any{"Stable": "before"}); err != nil {
		t.Fatalf("store old plugin configuration: %v", err)
	}
	generation, ok := host.plugin(pluginID)
	if !ok {
		t.Fatal("old plugin generation is missing")
	}
	api := &mattermostAPI{host: host, db: db, pluginID: pluginID, generation: generation, logger: slog.Default()}
	if appErr := api.KVSet("stable", []byte("before")); appErr != nil {
		t.Fatalf("store old plugin KV: %v", appErr)
	}

	replacement := replacementFailureBundle(t, pluginID, "mutate_state")
	if _, err := host.Install(ctx, bytes.NewReader(replacement), "test-admin", true); err == nil || !strings.Contains(err.Error(), "injected activation failure") {
		t.Fatalf("replacement error = %v, want injected activation failure", err)
	}

	configuration, _, err := host.Configuration(pluginID)
	if err != nil {
		t.Fatalf("read configuration after rollback: %v", err)
	}
	if got := configuration["Stable"]; got != "before" {
		t.Errorf("Stable configuration after rollback = %#v, want before", got)
	}
	if _, exists := configuration["UpgradeOnly"]; exists {
		t.Errorf("failed replacement left UpgradeOnly configuration: %#v", configuration)
	}
	stable, appErr := api.KVGet("stable")
	if appErr != nil {
		t.Fatalf("read stable KV after rollback: %v", appErr)
	}
	if string(stable) != "before" {
		t.Errorf("stable KV after rollback = %q, want before", stable)
	}
	upgradeOnly, appErr := api.KVGet("upgrade-only")
	if appErr != nil {
		t.Fatalf("read upgrade-only KV after rollback: %v", appErr)
	}
	if upgradeOnly != nil {
		t.Errorf("failed replacement left upgrade-only KV = %q", upgradeOnly)
	}

	loaded, ok := host.plugin(pluginID)
	if !ok || loaded.Manifest.Version != "1.0.0" || loaded.State != "running" {
		t.Errorf("old plugin was not reactivated: %#v", loaded)
	}
}

func TestCanceledUploadRequestDoesNotCancelReplacementRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper plugin executable uses a POSIX shell wrapper")
	}
	db, baseCtx, host := newReplacementFailureHarness(t)
	const pluginID = "com.example.rollback-cancel"
	installRecoveryBasePlugin(t, baseCtx, host, pluginID)

	marker := filepath.Join(t.TempDir(), "activation-ready")
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	canceled := make(chan struct{})
	go func() {
		defer close(canceled)
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-deadline.C:
				return
			case <-ticker.C:
				if _, err := os.Stat(marker); err == nil {
					cancelRequest()
					return
				}
			}
		}
	}()

	replacement := replacementFailureBundle(t, pluginID, "wait_for_cancel:"+marker)
	_, err := host.Install(requestCtx, bytes.NewReader(replacement), "test-admin", true)
	<-canceled
	if !errors.Is(requestCtx.Err(), context.Canceled) {
		t.Fatal("replacement activation did not reach the request-cancellation checkpoint")
	}
	if err == nil || !strings.Contains(err.Error(), "injected activation failure") {
		t.Fatalf("replacement error = %v, want activation failure after request cancellation", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("request cancellation escaped durable finalization: %v", err)
	}
	loaded, ok := host.plugin(pluginID)
	if !ok || loaded.Manifest.Version != "1.0.0" || loaded.State != "running" {
		t.Fatalf("old plugin was not restored after request cancellation: %#v", loaded)
	}
	var journals int
	if err := db.Pool.QueryRow(baseCtx, `SELECT count(*) FROM plugin_install_transactions WHERE plugin_id=$1`, pluginID).Scan(&journals); err != nil {
		t.Fatalf("count install journals: %v", err)
	}
	if journals != 0 {
		t.Fatalf("request cancellation left %d install journals", journals)
	}
}

func TestHangingActivationIsTerminatedAndRolledBack(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bounded Mattermost plugin process termination requires Linux")
	}
	db, ctx, host := newReplacementFailureHarness(t)
	const pluginID = "com.example.activation-timeout"
	installRecoveryBasePlugin(t, ctx, host, pluginID)
	host.activationTimeout = 300 * time.Millisecond
	host.activationStopTimeout = 5 * time.Second

	started := time.Now()
	_, err := host.Install(ctx, bytes.NewReader(replacementFailureBundle(t, pluginID, "hang")), "test-admin", true)
	if err == nil || !strings.Contains(err.Error(), "activation exceeded") {
		t.Fatalf("hanging replacement error = %v, want bounded activation timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("hanging activation took %s, want bounded shutdown", elapsed)
	}
	loaded, ok := host.plugin(pluginID)
	if !ok || loaded.Manifest.Version != "1.0.0" || loaded.State != "running" {
		t.Fatalf("old plugin was not restored after activation timeout: %#v", loaded)
	}
	var journals int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM plugin_install_transactions WHERE plugin_id=$1`, pluginID).Scan(&journals); err != nil {
		t.Fatalf("count install journals: %v", err)
	}
	if journals != 0 {
		t.Fatalf("activation timeout left %d install journals", journals)
	}
}

func TestReplacementWaitsForOldGenerationBeforeCandidateActivation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Mattermost helper activation and bounded process management require Linux")
	}
	_, ctx, host := newReplacementFailureHarness(t)
	const pluginID = "com.example.replacement-drain"
	installRecoveryBasePlugin(t, ctx, host, pluginID)

	old, ok := host.plugin(pluginID)
	if !ok {
		t.Fatalf("old plugin %q was not registered", pluginID)
	}
	lease, ok := old.acquireDispatch()
	if !ok {
		t.Fatal("old plugin generation did not admit a dispatch")
	}
	leaseReleased := false
	defer func() {
		if !leaseReleased {
			lease.release()
		}
	}()

	activationMarker := filepath.Join(t.TempDir(), "candidate-activated")
	replacement := replacementFailureBundle(t, pluginID, "mark_success:"+activationMarker)
	type installOutcome struct {
		result *InstallResult
		err    error
	}
	installed := make(chan installOutcome, 1)
	go func() {
		result, err := host.Install(ctx, bytes.NewReader(replacement), "test-admin", true)
		installed <- installOutcome{result: result, err: err}
	}()

	deadline := time.NewTimer(10 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for !old.transitioning.Load() {
		select {
		case outcome := <-installed:
			t.Fatalf("replacement completed before closing old admission: result=%#v err=%v", outcome.result, outcome.err)
		case <-deadline.C:
			t.Fatal("replacement did not close old generation admission")
		case <-ticker.C:
		}
	}
	if _, err := os.Stat(activationMarker); !os.IsNotExist(err) {
		t.Fatalf("candidate activated while old generation lease was held: %v", err)
	}
	select {
	case outcome := <-installed:
		t.Fatalf("replacement completed while old generation lease was held: result=%#v err=%v", outcome.result, outcome.err)
	default:
	}

	lease.release()
	leaseReleased = true
	select {
	case outcome := <-installed:
		if outcome.err != nil {
			t.Fatalf("replacement failed after old generation drained: %v", outcome.err)
		}
		if outcome.result == nil || outcome.result.Version != "2.0.0" || outcome.result.State != "running" {
			t.Fatalf("replacement result = %#v, want running version 2.0.0", outcome.result)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("replacement did not resume after old generation drained")
	}
	if _, err := os.Stat(activationMarker); err != nil {
		t.Fatalf("candidate activation marker was not created: %v", err)
	}
}

func TestFailedReplacementSurfacesRollbackError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper plugin executable uses a POSIX shell wrapper")
	}
	_, ctx, host := newReplacementFailureHarness(t)
	const pluginID = "com.example.rollback-error"
	installRecoveryBasePlugin(t, ctx, host, pluginID)

	replacement := replacementFailureBundle(t, pluginID, "remove_backup")
	_, err := host.Install(ctx, bytes.NewReader(replacement), "test-admin", true)
	if err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "injected activation failure") {
		t.Errorf("replacement error lost activation failure: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "rollback") {
		t.Errorf("replacement error did not surface rollback failure: %v", err)
	}
}

// TestReplacementFailurePluginProcess is executed as the server component of
// a generated Mattermost plugin bundle. In the ordinary parent test process it
// returns immediately.
func TestReplacementFailurePluginProcess(t *testing.T) {
	mode := os.Getenv("MOYRO_REPLACEMENT_HELPER_MODE")
	if mode == "" {
		return
	}
	mmplugin.ClientMain(&replacementFailurePlugin{mode: mode})
}

type replacementFailurePlugin struct {
	mmplugin.MattermostPlugin
	mode string
}

func (p *replacementFailurePlugin) OnActivate() error {
	if marker, ok := strings.CutPrefix(p.mode, "mark_success:"); ok {
		if err := os.WriteFile(marker, []byte("activated"), 0o600); err != nil {
			return fmt.Errorf("write successful activation marker: %w", err)
		}
		return nil
	}
	switch p.mode {
	case "mutate_state":
		if appErr := p.API.SavePluginConfig(map[string]any{
			"Stable": "corrupted", "UpgradeOnly": true,
		}); appErr != nil {
			return fmt.Errorf("mutate configuration: %s", appErr.Error())
		}
		if appErr := p.API.KVDeleteAll(); appErr != nil {
			return fmt.Errorf("delete old KV: %s", appErr.Error())
		}
		if appErr := p.API.KVSet("upgrade-only", []byte("new")); appErr != nil {
			return fmt.Errorf("write upgrade KV: %s", appErr.Error())
		}
	case "remove_backup":
		bundlePath, err := p.API.GetBundlePath()
		if err != nil {
			return fmt.Errorf("get bundle path: %w", err)
		}
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(bundlePath), ".moyro-plugin-backup-*"))
		if err != nil {
			return fmt.Errorf("find rollback backup: %w", err)
		}
		if len(matches) == 0 {
			return errors.New("rollback backup was not created")
		}
		for _, match := range matches {
			if err := os.RemoveAll(match); err != nil {
				return fmt.Errorf("remove rollback backup: %w", err)
			}
		}
	case "hang":
		select {}
	default:
		if marker, ok := strings.CutPrefix(p.mode, "wait_for_cancel:"); ok {
			if err := os.WriteFile(marker, []byte("ready"), 0o600); err != nil {
				return fmt.Errorf("write activation marker: %w", err)
			}
			time.Sleep(500 * time.Millisecond)
			break
		}
		return fmt.Errorf("unknown helper mode %q", p.mode)
	}
	return errors.New("injected activation failure")
}

func newReplacementFailureHarness(t *testing.T) (*store.DB, context.Context, *Host) {
	t.Helper()
	db := newPluginRuntimeTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate replacement failure schema: %v", err)
	}
	manager, err := secrets.New(bytes.Repeat([]byte{0x72}, secrets.MasterKeySize))
	if err != nil {
		t.Fatalf("create secret manager: %v", err)
	}
	host, err := NewWithRuntime(t.TempDir(), db, manager, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("create plugin host: %v", err)
	}
	t.Cleanup(host.Shutdown)
	return db, ctx, host
}

func installRecoveryBasePlugin(t *testing.T, ctx context.Context, host *Host, pluginID string) {
	t.Helper()
	bundle := webPluginBundle(t, pluginID, "1.0.0", "old")
	result, err := host.Install(ctx, bytes.NewReader(bundle), "test-admin", false)
	if err != nil {
		t.Fatalf("install old plugin: %v", err)
	}
	if result.State != "running" {
		t.Fatalf("old plugin state = %q, want running", result.State)
	}
}

func webPluginBundle(t *testing.T, pluginID, version, marker string) []byte {
	t.Helper()
	manifest, err := json.Marshal(map[string]any{
		"id":      pluginID,
		"name":    "Recovery test plugin",
		"version": version,
		"webapp":  map[string]string{"bundle_path": "web/main.js"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return makeBundle(t,
		dirEntry(pluginID+"/"),
		fileEntry(pluginID+"/plugin.json", manifest),
		dirEntry(pluginID+"/web/"),
		fileEntry(pluginID+"/web/main.js", []byte("window.__recoveryMarker="+fmt.Sprintf("%q", marker)+";")),
	)
}

func replacementFailureBundle(t *testing.T, pluginID, mode string) []byte {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	script := "#!/bin/sh\nMOYRO_REPLACEMENT_HELPER_MODE=" + shellQuote(mode) + " exec " +
		shellQuote(executable) + " -test.run='^TestReplacementFailurePluginProcess$'\n"
	executablePath := "server/plugin"
	manifest, err := json.Marshal(map[string]any{
		"id":      pluginID,
		"name":    "Failing replacement test plugin",
		"version": "2.0.0",
		"server": map[string]any{
			"executables": map[string]string{runtime.GOOS + "-" + runtime.GOARCH: executablePath},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return makeBundle(t,
		dirEntry(pluginID+"/"),
		fileEntry(pluginID+"/plugin.json", manifest),
		dirEntry(pluginID+"/server/"),
		fileEntry(pluginID+"/"+executablePath, []byte(script)),
	)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writeRecoveryWebPlugin(t *testing.T, dir, pluginID, version, marker string) *Manifest {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatalf("create recovery plugin directory: %v", err)
	}
	manifest := &Manifest{
		ID:      pluginID,
		Name:    "Recovery journal fixture",
		Version: version,
		Webapp:  &WebappSpec{BundlePath: "web/main.js"},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatalf("write recovery manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "main.js"), []byte("window.__recoveryMarker="+fmt.Sprintf("%q", marker)+";"), 0o644); err != nil {
		t.Fatalf("write recovery web bundle: %v", err)
	}
	return manifest
}
