package config

import (
	"os"
	"strings"
	"time"
)

type Config struct {
	Listen          string
	DatabaseURL     string
	RedisURL        string
	JWTSecret       []byte
	TokenTTL        time.Duration
	PluginDir       string
	FileStorageRoot string

	// AllowedOutgoingHosts, when non-empty, restricts outbound webhook
	// callbacks to exactly these hostnames. When empty, the dispatcher
	// falls back to blocking private / loopback / link-local addresses.
	// Comma-separated env value (MODDLE_ALLOWED_OUTGOING_HOSTS).
	AllowedOutgoingHosts []string

	// PublicBaseURL is the externally-reachable origin of the webapp. Used
	// to build OAuth redirect URIs when no explicit per-provider redirect
	// URL is configured, and to decide Secure cookie defaults.
	PublicBaseURL string

	// OAuth provider configuration. A provider is considered "enabled"
	// iff its ClientID is non-empty. Empty secrets disable the provider
	// server-side even if the client id is present (misconfig guard).
	OAuthGoogleClientID     string
	OAuthGoogleClientSecret string
	OAuthGoogleRedirectURL  string
	OAuthGitHubClientID     string
	OAuthGitHubClientSecret string
	OAuthGitHubRedirectURL  string

	// SMTP: daily digest email output (Phase 17). Empty Host → NoopSender
	// used in dev; digest worker still runs but Send() is a no-op.
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPTLS      bool

	// File storage backend selector (Phase 17). "fs" (default) preserves
	// the existing local filesystem impl; "s3" routes Write/Open/Remove
	// through an S3-compatible bucket.
	FileBackend  string
	S3Bucket     string
	S3Region     string
	S3Endpoint   string // optional, for minio/R2/B2 path-style endpoints

	// LinkPreviewsEnabled controls whether createPost kicks off the async
	// OpenGraph fetch + post_edited re-broadcast (Phase 18). Defaults true;
	// set MODDLE_LINK_PREVIEWS=false to disable for airgapped deploys.
	LinkPreviewsEnabled bool
}

func Load() (*Config, error) {
	cfg := &Config{
		Listen:               envOr("MODDLE_LISTEN", ":8065"),
		DatabaseURL:          envOr("MODDLE_DATABASE_URL", "postgres://moddle:moddle@localhost:5433/moddle?sslmode=disable"),
		RedisURL:             envOr("MODDLE_REDIS_URL", "redis://localhost:6380/0"),
		JWTSecret:            []byte(envOr("MODDLE_JWT_SECRET", "dev-secret-change-me")),
		TokenTTL:             24 * time.Hour,
		PluginDir:            envOr("MODDLE_PLUGIN_DIR", "./plugins"),
		FileStorageRoot:      envOr("MODDLE_FILE_ROOT", "./data/files"),
		AllowedOutgoingHosts: splitCSV(envOr("MODDLE_ALLOWED_OUTGOING_HOSTS", "")),
		PublicBaseURL:        envOr("MODDLE_PUBLIC_BASE_URL", "http://localhost:8065"),

		OAuthGoogleClientID:     envOr("MODDLE_OAUTH_GOOGLE_CLIENT_ID", ""),
		OAuthGoogleClientSecret: envOr("MODDLE_OAUTH_GOOGLE_CLIENT_SECRET", ""),
		OAuthGoogleRedirectURL:  envOr("MODDLE_OAUTH_GOOGLE_REDIRECT_URL", ""),
		OAuthGitHubClientID:     envOr("MODDLE_OAUTH_GITHUB_CLIENT_ID", ""),
		OAuthGitHubClientSecret: envOr("MODDLE_OAUTH_GITHUB_CLIENT_SECRET", ""),
		OAuthGitHubRedirectURL:  envOr("MODDLE_OAUTH_GITHUB_REDIRECT_URL", ""),

		SMTPHost:     envOr("MODDLE_SMTP_HOST", ""),
		SMTPPort:     envOr("MODDLE_SMTP_PORT", "25"),
		SMTPUsername: envOr("MODDLE_SMTP_USERNAME", ""),
		SMTPPassword: envOr("MODDLE_SMTP_PASSWORD", ""),
		SMTPFrom:     envOr("MODDLE_SMTP_FROM", "noreply@localhost"),
		SMTPTLS:      strings.EqualFold(envOr("MODDLE_SMTP_TLS", "false"), "true"),

		FileBackend: strings.ToLower(envOr("MODDLE_FILE_BACKEND", "fs")),
		S3Bucket:    envOr("MODDLE_S3_BUCKET", ""),
		S3Region:    envOr("MODDLE_S3_REGION", "us-east-1"),
		S3Endpoint:  envOr("MODDLE_S3_ENDPOINT", ""),

		LinkPreviewsEnabled: !strings.EqualFold(envOr("MODDLE_LINK_PREVIEWS", "true"), "false"),
	}
	return cfg, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
