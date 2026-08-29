// Package config loads the small, immutable trust-root configuration needed
// before PostgreSQL is available. All operator-managed settings belong in the
// database and are deliberately not read from the process environment here.
package config

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	EnvPostgresDSN           = "POSTGRES_DSN"
	EnvBootstrapAdmin        = "BOOTSTRAP_ADMIN"
	EnvBootstrapAdminPass    = "BOOTSTRAP_ADMIN_PASSWORD"
	EnvEncryptionKey         = "ENCRYPTION_KEY"
	DefaultListenAddress     = ":8065"
	DefaultPublicBaseURL     = "http://localhost:8065"
	DefaultPluginDirectory   = "/var/lib/moyro/plugins"
	DefaultFileStorageRoot   = "/var/lib/moyro/files"
	minimumBootstrapPassword = 12
	maximumBootstrapPassword = 72 // bcrypt's defined input limit
)

// Config contains the four immutable boot inputs plus compatibility fields
// consumed by the current composition root. Compatibility fields have fixed,
// offline-safe defaults; they must eventually be overlaid from the encrypted
// database settings service, never from additional environment variables.
type Config struct {
	PostgresDSN            string
	BootstrapAdminEmail    string
	BootstrapAdminPassword string
	EncryptionKey          []byte

	// Transitional aliases and fixed runtime defaults used by existing code.
	Listen          string
	DatabaseURL     string
	RedisURL        string
	JWTSecret       []byte
	TokenTTL        time.Duration
	PluginDir       string
	FileStorageRoot string

	AllowedOutgoingHosts []string
	PublicBaseURL        string

	OAuthGoogleClientID     string
	OAuthGoogleClientSecret string
	OAuthGoogleRedirectURL  string
	OAuthGitHubClientID     string
	OAuthGitHubClientSecret string
	OAuthGitHubRedirectURL  string

	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPTLS      bool

	FileBackend string
	S3Bucket    string
	S3Region    string
	S3Endpoint  string

	LinkPreviewsEnabled bool
}

// Load reads exactly POSTGRES_DSN, BOOTSTRAP_ADMIN,
// BOOTSTRAP_ADMIN_PASSWORD, and ENCRYPTION_KEY. Missing or malformed trust
// roots stop startup rather than silently selecting development credentials.
func Load() (*Config, error) {
	return loadFrom(os.Getenv)
}

func loadFrom(getenv func(string) string) (*Config, error) {
	dsn := strings.TrimSpace(getenv(EnvPostgresDSN))
	adminEmail := strings.ToLower(strings.TrimSpace(getenv(EnvBootstrapAdmin)))
	adminPassword := getenv(EnvBootstrapAdminPass)
	encodedKey := strings.TrimSpace(getenv(EnvEncryptionKey))

	var missing []string
	for key, value := range map[string]string{
		EnvPostgresDSN:        dsn,
		EnvBootstrapAdmin:     adminEmail,
		EnvBootstrapAdminPass: adminPassword,
		EnvEncryptionKey:      encodedKey,
	} {
		if value == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) != 0 {
		ordered := make([]string, 0, 4)
		for _, key := range []string{EnvPostgresDSN, EnvBootstrapAdmin, EnvBootstrapAdminPass, EnvEncryptionKey} {
			for _, absent := range missing {
				if key == absent {
					ordered = append(ordered, key)
				}
			}
		}
		return nil, fmt.Errorf("config: missing required environment variables: %s", strings.Join(ordered, ", "))
	}

	if _, err := pgxpool.ParseConfig(dsn); err != nil {
		return nil, fmt.Errorf("config: invalid %s: %w", EnvPostgresDSN, err)
	}
	if err := validateBootstrapEmail(adminEmail); err != nil {
		return nil, err
	}
	if len(adminPassword) < minimumBootstrapPassword {
		return nil, fmt.Errorf("config: %s must contain at least %d bytes", EnvBootstrapAdminPass, minimumBootstrapPassword)
	}
	if len(adminPassword) > maximumBootstrapPassword {
		return nil, fmt.Errorf("config: %s must contain at most %d bytes", EnvBootstrapAdminPass, maximumBootstrapPassword)
	}

	masterKey, err := decodeMasterKey(encodedKey)
	if err != nil {
		return nil, err
	}
	jwtKey, err := hkdf.Key(sha256.New, masterKey, []byte("moyro/config/v1"), "moyro/session-signing/v1", 32)
	if err != nil {
		return nil, fmt.Errorf("config: derive session key: %w", err)
	}

	return &Config{
		PostgresDSN:            dsn,
		BootstrapAdminEmail:    adminEmail,
		BootstrapAdminPassword: adminPassword,
		EncryptionKey:          append([]byte(nil), masterKey...),

		Listen:          DefaultListenAddress,
		DatabaseURL:     dsn,
		RedisURL:        "",
		JWTSecret:       jwtKey,
		TokenTTL:        24 * time.Hour,
		PluginDir:       DefaultPluginDirectory,
		FileStorageRoot: DefaultFileStorageRoot,

		AllowedOutgoingHosts: nil,
		PublicBaseURL:        DefaultPublicBaseURL,
		SMTPPort:             "25",
		SMTPFrom:             "noreply@localhost",
		FileBackend:          "fs",
		S3Region:             "us-east-1",
		// External link fetching is opt-in through database settings.
		LinkPreviewsEnabled: false,
	}, nil
}

func validateBootstrapEmail(email string) error {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || !strings.EqualFold(addr.Address, email) {
		return fmt.Errorf("config: %s must be a plain email address", EnvBootstrapAdmin)
	}
	local, domain, ok := strings.Cut(addr.Address, "@")
	if !ok || local == "" || domain == "" || strings.ContainsAny(domain, " \t") {
		return fmt.Errorf("config: %s must be a plain email address", EnvBootstrapAdmin)
	}
	return nil
}

func decodeMasterKey(encoded string) ([]byte, error) {
	decode := func(enc *base64.Encoding) ([]byte, error) {
		decoded, err := enc.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		if len(decoded) != 32 {
			return nil, fmt.Errorf("decoded key is %d bytes, want 32", len(decoded))
		}
		return decoded, nil
	}
	key, err := decode(base64.StdEncoding.Strict())
	if err != nil {
		key, err = decode(base64.RawStdEncoding.Strict())
	}
	if err != nil {
		return nil, fmt.Errorf("config: %s must be base64 for exactly 32 bytes: %w", EnvEncryptionKey, err)
	}
	if allZero(key) {
		return nil, fmt.Errorf("config: %s must not be an all-zero key", EnvEncryptionKey)
	}
	return key, nil
}

func allZero(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	var combined byte
	for _, v := range b {
		combined |= v
	}
	return combined == 0
}

// Validate documents the invariant expected by composition code constructing a
// Config directly in tests. Load always returns an already-valid value.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config: nil config")
	}
	if c.PostgresDSN == "" || c.BootstrapAdminEmail == "" || c.BootstrapAdminPassword == "" || len(c.EncryptionKey) != 32 {
		return errors.New("config: incomplete boot configuration")
	}
	if _, err := pgxpool.ParseConfig(c.PostgresDSN); err != nil {
		return fmt.Errorf("config: invalid %s: %w", EnvPostgresDSN, err)
	}
	if err := validateBootstrapEmail(strings.ToLower(strings.TrimSpace(c.BootstrapAdminEmail))); err != nil {
		return err
	}
	if len(c.BootstrapAdminPassword) < minimumBootstrapPassword || len(c.BootstrapAdminPassword) > maximumBootstrapPassword {
		return fmt.Errorf("config: %s must contain between %d and %d bytes", EnvBootstrapAdminPass, minimumBootstrapPassword, maximumBootstrapPassword)
	}
	if allZero(c.EncryptionKey) {
		return fmt.Errorf("config: %s must not be an all-zero key", EnvEncryptionKey)
	}
	return nil
}
