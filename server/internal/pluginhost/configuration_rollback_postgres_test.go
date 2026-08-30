package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mmplugin "github.com/mattermost/mattermost/server/public/plugin"
)

func TestUpdateConfigurationRestoresPreviousEnvelopeOnHookFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper plugin executable uses a POSIX shell wrapper")
	}
	db, ctx, host := newReplacementFailureHarness(t)

	for _, test := range []struct {
		name string
		seed bool
	}{
		{name: "encrypted previous configuration", seed: true},
		{name: "no previous configuration envelope"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pluginID := "com.example.config-rollback-" + strings.ReplaceAll(test.name, " ", "-")
			result, err := host.Install(ctx, bytes.NewReader(configurationFailureBundle(t, pluginID, "fail")), "test-admin", false)
			if err != nil {
				t.Fatalf("install configuration failure plugin: %v", err)
			}
			if result.State != "running" {
				t.Fatalf("installed plugin state = %q, want running", result.State)
			}
			if test.seed {
				if _, err := host.persistConfiguration(ctx, pluginID, map[string]any{"Stable": "before"}, false); err != nil {
					t.Fatalf("seed previous configuration: %v", err)
				}
			}
			before := readConfigurationEnvelope(t, ctx, host, pluginID)

			err = host.UpdateConfiguration(ctx, pluginID, map[string]any{"Stable": "after"})
			if err == nil || !strings.Contains(err.Error(), "injected configuration failure") {
				t.Fatalf("UpdateConfiguration error = %v, want injected hook failure", err)
			}
			after := readConfigurationEnvelope(t, ctx, host, pluginID)
			if !reflect.DeepEqual(after, before) {
				t.Errorf("configuration envelope after rollback = %#v, want %#v", after, before)
			}
			configuration, _, err := host.Configuration(pluginID)
			if err != nil {
				t.Fatalf("read effective configuration after rollback: %v", err)
			}
			if test.seed {
				if configuration["Stable"] != "before" {
					t.Errorf("effective Stable = %#v, want before", configuration["Stable"])
				}
			} else if _, exists := configuration["Stable"]; exists {
				t.Errorf("failed update created configuration where none existed: %#v", configuration)
			}
			var count []byte
			if err := db.Pool.QueryRow(ctx, `
				SELECT value FROM plugin_key_values WHERE plugin_id=$1 AND key='configuration-hook-count'
			`, pluginID).Scan(&count); err != nil {
				t.Fatalf("read configuration hook count: %v", err)
			}
			if string(count) != "3" {
				t.Errorf("configuration hook count = %q, want 3 (activation, failed apply, rollback reapply)", count)
			}
		})
	}
}

func TestUpdateConfigurationFailsClosedWhenRollbackLosesCAS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper plugin executable uses a POSIX shell wrapper")
	}
	_, ctx, host := newReplacementFailureHarness(t)
	const pluginID = "com.example.config-rollback-cas"
	if _, err := host.Install(ctx, bytes.NewReader(configurationFailureBundle(t, pluginID, "delayed_fail")), "test-admin", false); err != nil {
		t.Fatalf("install delayed failure plugin: %v", err)
	}
	if _, err := host.persistConfiguration(ctx, pluginID, map[string]any{"Stable": "before"}, false); err != nil {
		t.Fatalf("seed previous configuration: %v", err)
	}
	before := readConfigurationEnvelope(t, ctx, host, pluginID)
	keyID, nonce, ciphertext, err := host.secrets.Encrypt(pluginConfigContext(pluginID), []byte(`{"Stable":"intervening"}`))
	if err != nil {
		t.Fatalf("encrypt intervening configuration: %v", err)
	}
	intervened := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var current []byte
			queryErr := host.db.Pool.QueryRow(ctx, `SELECT config_ciphertext FROM plugins WHERE id=$1`, pluginID).Scan(&current)
			if queryErr != nil {
				intervened <- queryErr
				return
			}
			if !bytes.Equal(current, before.ciphertext) {
				_, updateErr := host.db.Pool.Exec(ctx, `
					UPDATE plugins SET config_key_id=$2,config_nonce=$3,config_ciphertext=$4,update_at=$5
					WHERE id=$1
				`, pluginID, keyID, nonce, ciphertext, time.Now().UnixMilli())
				intervened <- updateErr
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		intervened <- errors.New("timed out waiting for tentative configuration")
	}()

	err = host.UpdateConfiguration(ctx, pluginID, map[string]any{"Stable": "after"})
	if interveneErr := <-intervened; interveneErr != nil {
		t.Fatalf("inject concurrent configuration change: %v", interveneErr)
	}
	if !errors.Is(err, ErrPluginRuntimeStuck) {
		t.Fatalf("UpdateConfiguration error = %v, want ErrPluginRuntimeStuck", err)
	}
	if !strings.Contains(err.Error(), "injected delayed configuration failure") ||
		!strings.Contains(err.Error(), "restore previous plugin configuration") {
		t.Errorf("fail-closed error did not preserve apply and rollback causes: %v", err)
	}
	if _, _, configErr := host.Configuration(pluginID); !errors.Is(configErr, ErrPluginRuntimeStuck) {
		t.Errorf("runtime accepted configuration reads after uncertain rollback: %v", configErr)
	}
}

func TestUpdateConfigurationFailsClosedWhenPreviousConfigReapplyFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper plugin executable uses a POSIX shell wrapper")
	}
	_, ctx, host := newReplacementFailureHarness(t)
	const pluginID = "com.example.config-reapply-failure"
	if _, err := host.Install(ctx, bytes.NewReader(configurationFailureBundle(t, pluginID, "always_fail")), "test-admin", false); err != nil {
		t.Fatalf("install configuration failure plugin: %v", err)
	}
	if _, err := host.persistConfiguration(ctx, pluginID, map[string]any{"Stable": "before"}, false); err != nil {
		t.Fatalf("seed previous configuration: %v", err)
	}
	before := readConfigurationEnvelope(t, ctx, host, pluginID)

	err := host.UpdateConfiguration(ctx, pluginID, map[string]any{"Stable": "after"})
	if !errors.Is(err, ErrPluginRuntimeStuck) {
		t.Fatalf("UpdateConfiguration error = %v, want ErrPluginRuntimeStuck", err)
	}
	if !strings.Contains(err.Error(), "reapply previous plugin configuration") {
		t.Errorf("error did not surface rollback reapply failure: %v", err)
	}
	after := readConfigurationEnvelope(t, ctx, host, pluginID)
	if !reflect.DeepEqual(after, before) {
		t.Errorf("database envelope after failed live reapply = %#v, want restored %#v", after, before)
	}
	if _, _, configErr := host.Configuration(pluginID); !errors.Is(configErr, ErrPluginRuntimeStuck) {
		t.Errorf("runtime accepted configuration reads after failed live reapply: %v", configErr)
	}
}

func TestSavePluginConfigPersistsWithoutRecursiveConfigurationHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper plugin executable uses a POSIX shell wrapper")
	}
	db, ctx, host := newReplacementFailureHarness(t)
	const pluginID = "com.example.config-save-on-activate"
	if _, err := host.Install(ctx, bytes.NewReader(configurationFailureBundle(t, pluginID, "save_on_activate")), "test-admin", false); err != nil {
		t.Fatalf("install save-on-activate plugin: %v", err)
	}
	configuration, _, err := host.Configuration(pluginID)
	if err != nil {
		t.Fatalf("read configuration saved by plugin: %v", err)
	}
	if configuration["SavedDuringActivate"] != true {
		t.Errorf("configuration saved during activation = %#v, want true", configuration)
	}
	var count []byte
	if err := db.Pool.QueryRow(ctx, `
		SELECT value FROM plugin_key_values WHERE plugin_id=$1 AND key='configuration-hook-count'
	`, pluginID).Scan(&count); err != nil {
		t.Fatalf("read configuration hook count: %v", err)
	}
	if string(count) != "1" {
		t.Errorf("configuration hook count = %q, want one framework activation notification", count)
	}
}

func TestConfigurationFailurePluginProcess(t *testing.T) {
	mode := os.Getenv("MOYRO_CONFIGURATION_HELPER_MODE")
	if mode == "" {
		return
	}
	mmplugin.ClientMain(&configurationFailurePlugin{mode: mode})
}

type configurationFailurePlugin struct {
	mmplugin.MattermostPlugin
	mode  string
	calls atomic.Int32
}

func (p *configurationFailurePlugin) OnActivate() error {
	if p.mode == "save_on_activate" {
		if appErr := p.API.SavePluginConfig(map[string]any{"SavedDuringActivate": true}); appErr != nil {
			return fmt.Errorf("save plugin configuration during activation: %s", appErr.Error())
		}
	}
	return nil
}

func (p *configurationFailurePlugin) OnConfigurationChange() error {
	call := p.calls.Add(1)
	if appErr := p.API.KVSet("configuration-hook-count", []byte(strconv.FormatInt(int64(call), 10))); appErr != nil {
		return fmt.Errorf("record configuration hook call: %s", appErr.Error())
	}
	// Mattermost initializes configuration once while activating the plugin.
	// The administrator update is the second call and rollback reapply is the
	// third. Activation must succeed for every test mode.
	if call == 1 {
		return nil
	}
	if p.mode == "save_on_activate" {
		return nil
	}
	if call > 2 && p.mode != "always_fail" {
		return nil
	}
	if p.mode == "delayed_fail" {
		time.Sleep(750 * time.Millisecond)
		return errors.New("injected delayed configuration failure")
	}
	return errors.New("injected configuration failure")
}

func configurationFailureBundle(t *testing.T, pluginID, mode string) []byte {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executablePath := "server/plugin"
	script := "#!/bin/sh\nMOYRO_CONFIGURATION_HELPER_MODE=" + shellQuote(mode) + " exec " +
		shellQuote(executable) + " -test.run='^TestConfigurationFailurePluginProcess$'\n"
	manifest, err := json.Marshal(map[string]any{
		"id": pluginID, "name": "Configuration rollback test plugin", "version": "1.0.0",
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

func readConfigurationEnvelope(t *testing.T, ctx context.Context, host *Host, pluginID string) pluginConfigurationEnvelope {
	t.Helper()
	var envelope pluginConfigurationEnvelope
	if err := host.db.Pool.QueryRow(ctx, `
		SELECT config_key_id,config_nonce,config_ciphertext,update_at FROM plugins WHERE id=$1
	`, pluginID).Scan(&envelope.keyID, &envelope.nonce, &envelope.ciphertext, &envelope.updateAt); err != nil {
		t.Fatalf("read configuration envelope: %v", err)
	}
	return envelope
}
