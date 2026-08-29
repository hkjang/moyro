package config

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func validEnv() map[string]string {
	return map[string]string{
		EnvPostgresDSN:        "postgres://moyro:secret@postgres:5432/moyro?sslmode=disable",
		EnvBootstrapAdmin:     "Admin@Example.Local",
		EnvBootstrapAdminPass: "correct horse battery staple",
		EnvEncryptionKey:      base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)),
	}
}

func loader(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadRequiresExactlyTheFourBootInputs(t *testing.T) {
	for _, missing := range []string{EnvPostgresDSN, EnvBootstrapAdmin, EnvBootstrapAdminPass, EnvEncryptionKey} {
		t.Run(missing, func(t *testing.T) {
			values := validEnv()
			delete(values, missing)
			_, err := loadFrom(loader(values))
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("loadFrom() error = %v, want missing %s", err, missing)
			}
		})
	}
}

func TestLoadUsesFixedOfflineSafeRuntimeDefaults(t *testing.T) {
	values := validEnv()
	// Legacy variables must have no effect.
	values["MODDLE_LISTEN"] = ":9999"
	values["MODDLE_REDIS_URL"] = "redis://internet.example/0"
	values["MODDLE_LINK_PREVIEWS"] = "true"
	values["MODDLE_JWT_SECRET"] = "known-bad-secret"

	cfg, err := loadFrom(loader(values))
	if err != nil {
		t.Fatalf("loadFrom() returned error: %v", err)
	}
	if cfg.Listen != DefaultListenAddress || cfg.RedisURL != "" {
		t.Fatalf("runtime defaults = listen %q, redis %q", cfg.Listen, cfg.RedisURL)
	}
	if cfg.DatabaseURL != values[EnvPostgresDSN] || cfg.PostgresDSN != values[EnvPostgresDSN] {
		t.Fatal("postgres aliases were not populated")
	}
	if cfg.BootstrapAdminEmail != "admin@example.local" {
		t.Fatalf("BootstrapAdminEmail = %q", cfg.BootstrapAdminEmail)
	}
	if cfg.LinkPreviewsEnabled {
		t.Fatal("link previews must default off for an offline deployment")
	}
	if cfg.FileBackend != "fs" || cfg.FileStorageRoot != DefaultFileStorageRoot {
		t.Fatalf("file defaults = %q, %q", cfg.FileBackend, cfg.FileStorageRoot)
	}
	if bytes.Equal(cfg.JWTSecret, []byte(values["MODDLE_JWT_SECRET"])) || len(cfg.JWTSecret) != 32 {
		t.Fatal("JWT key must be independently derived from ENCRYPTION_KEY")
	}
	if !bytes.Equal(cfg.EncryptionKey, bytes.Repeat([]byte{0x42}, 32)) {
		t.Fatal("decoded encryption key mismatch")
	}
}

func TestLoadRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "dsn", key: EnvPostgresDSN, value: "://broken"},
		{name: "display name email", key: EnvBootstrapAdmin, value: "Admin <admin@example.com>"},
		{name: "short password", key: EnvBootstrapAdminPass, value: "short"},
		{name: "bcrypt oversized password", key: EnvBootstrapAdminPass, value: strings.Repeat("x", maximumBootstrapPassword+1)},
		{name: "invalid base64", key: EnvEncryptionKey, value: "not-base64"},
		{name: "short key", key: EnvEncryptionKey, value: base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{name: "zero key", key: EnvEncryptionKey, value: base64.StdEncoding.EncodeToString(make([]byte, 32))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := validEnv()
			values[tt.key] = tt.value
			if _, err := loadFrom(loader(values)); err == nil {
				t.Fatal("loadFrom() unexpectedly succeeded")
			}
		})
	}
}

func TestDecodeMasterKeyAcceptsPaddedAndRawBase64(t *testing.T) {
	want := bytes.Repeat([]byte{0x7e}, 32)
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(want),
		base64.RawStdEncoding.EncodeToString(want),
	} {
		got, err := decodeMasterKey(encoded)
		if err != nil {
			t.Fatalf("decodeMasterKey(): %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatal("decoded key mismatch")
		}
	}
}

func TestValidateRejectsIncompleteDirectConstruction(t *testing.T) {
	if err := (*Config)(nil).Validate(); err == nil {
		t.Fatal("nil config accepted")
	}
	if err := (&Config{}).Validate(); err == nil {
		t.Fatal("empty config accepted")
	}
	values := validEnv()
	key, _ := base64.StdEncoding.DecodeString(values[EnvEncryptionKey])
	cfg := &Config{
		PostgresDSN: values[EnvPostgresDSN], BootstrapAdminEmail: values[EnvBootstrapAdmin],
		BootstrapAdminPassword: values[EnvBootstrapAdminPass], EncryptionKey: key,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid directly constructed config rejected: %v", err)
	}
	cfg.EncryptionKey = make([]byte, 32)
	if err := cfg.Validate(); err == nil {
		t.Fatal("all-zero direct key accepted")
	}
}
