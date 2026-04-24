package config

import (
	"reflect"
	"testing"
	"time"
)

var configEnvKeys = []string{
	"MODDLE_LISTEN",
	"MODDLE_REDIS_URL",
	"MODDLE_DATABASE_URL",
	"MODDLE_JWT_SECRET",
	"MODDLE_PLUGIN_DIR",
	"MODDLE_SMTP_HOST",
	"MODDLE_SMTP_PORT",
	"MODDLE_SMTP_USERNAME",
	"MODDLE_SMTP_PASSWORD",
	"MODDLE_SMTP_FROM",
	"MODDLE_SMTP_TLS",
	"MODDLE_PUBLIC_BASE_URL",
	"MODDLE_OAUTH_GOOGLE_CLIENT_ID",
	"MODDLE_OAUTH_GOOGLE_CLIENT_SECRET",
	"MODDLE_OAUTH_GOOGLE_REDIRECT_URL",
	"MODDLE_OAUTH_GITHUB_CLIENT_ID",
	"MODDLE_OAUTH_GITHUB_CLIENT_SECRET",
	"MODDLE_OAUTH_GITHUB_REDIRECT_URL",
	"MODDLE_FILE_BACKEND",
	"MODDLE_FILE_ROOT",
	"MODDLE_S3_ENDPOINT",
	"MODDLE_S3_BUCKET",
	"MODDLE_S3_REGION",
	"MODDLE_ALLOWED_OUTGOING_HOSTS",
	"MODDLE_LINK_PREVIEWS",
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range configEnvKeys {
		t.Setenv(key, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfigEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Listen != ":8065" {
		t.Fatalf("Listen = %q, want %q", cfg.Listen, ":8065")
	}
	if cfg.RedisURL != "redis://localhost:6380/0" {
		t.Fatalf("RedisURL = %q, want default Redis URL", cfg.RedisURL)
	}
	if cfg.DatabaseURL != "postgres://moddle:moddle@localhost:5433/moddle?sslmode=disable" {
		t.Fatalf("DatabaseURL = %q, want default PostgreSQL URL", cfg.DatabaseURL)
	}
	if string(cfg.JWTSecret) != "dev-secret-change-me" {
		t.Fatalf("JWTSecret = %q, want default secret", string(cfg.JWTSecret))
	}
	if cfg.TokenTTL != 24*time.Hour {
		t.Fatalf("TokenTTL = %s, want 24h", cfg.TokenTTL)
	}
	if cfg.PluginDir != "./plugins" {
		t.Fatalf("PluginDir = %q, want ./plugins", cfg.PluginDir)
	}
	if cfg.FileBackend != "fs" {
		t.Fatalf("FileBackend = %q, want fs", cfg.FileBackend)
	}
	if !cfg.LinkPreviewsEnabled {
		t.Fatalf("LinkPreviewsEnabled = false, want true")
	}
	if cfg.AllowedOutgoingHosts != nil {
		t.Fatalf("AllowedOutgoingHosts = %#v, want nil", cfg.AllowedOutgoingHosts)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MODDLE_LISTEN", ":9999")
	t.Setenv("MODDLE_DATABASE_URL", "postgres://example/moddle")
	t.Setenv("MODDLE_REDIS_URL", "redis://redis.example/3")
	t.Setenv("MODDLE_JWT_SECRET", "test-secret")
	t.Setenv("MODDLE_PLUGIN_DIR", "custom/plugins")
	t.Setenv("MODDLE_SMTP_TLS", "true")
	t.Setenv("MODDLE_FILE_BACKEND", "S3")
	t.Setenv("MODDLE_ALLOWED_OUTGOING_HOSTS", "hooks.example.com, api.example.com ,,")
	t.Setenv("MODDLE_LINK_PREVIEWS", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Listen != ":9999" {
		t.Fatalf("Listen = %q, want :9999", cfg.Listen)
	}
	if cfg.RedisURL != "redis://redis.example/3" {
		t.Fatalf("RedisURL = %q, want override", cfg.RedisURL)
	}
	if cfg.DatabaseURL != "postgres://example/moddle" {
		t.Fatalf("DatabaseURL = %q, want override", cfg.DatabaseURL)
	}
	if string(cfg.JWTSecret) != "test-secret" {
		t.Fatalf("JWTSecret = %q, want override", string(cfg.JWTSecret))
	}
	if cfg.PluginDir != "custom/plugins" {
		t.Fatalf("PluginDir = %q, want override", cfg.PluginDir)
	}
	if !cfg.SMTPTLS {
		t.Fatalf("SMTPTLS = false, want true")
	}
	if cfg.FileBackend != "s3" {
		t.Fatalf("FileBackend = %q, want lowercase s3", cfg.FileBackend)
	}
	wantHosts := []string{"hooks.example.com", "api.example.com"}
	if !reflect.DeepEqual(cfg.AllowedOutgoingHosts, wantHosts) {
		t.Fatalf("AllowedOutgoingHosts = %#v, want %#v", cfg.AllowedOutgoingHosts, wantHosts)
	}
	if cfg.LinkPreviewsEnabled {
		t.Fatalf("LinkPreviewsEnabled = true, want false")
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "trims spaces", in: " alpha, beta ,gamma ", want: []string{"alpha", "beta", "gamma"}},
		{name: "skips empty items", in: "alpha,, ,beta,", want: []string{"alpha", "beta"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitCSV(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("splitCSV(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
