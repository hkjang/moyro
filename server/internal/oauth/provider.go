// Package oauth wraps the two OAuth2 providers we support — Google and
// GitHub — behind a single Provider interface. The implementations live
// in sibling files; this file defines only the shared surface + a small
// registry built from config.
//
// We deliberately avoid golang.org/x/oauth2: the exchange flow is ~60
// lines per provider, and taking a dependency on a complex state-machine
// library is more burden than benefit at this scale.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hkjang/moyro/server/internal/config"
)

// UserInfo is the normalised identity returned by every provider. Some
// fields may be empty depending on the provider's scopes / response.
type UserInfo struct {
	Subject       string // provider-local user id (stable)
	Username      string // preferred provider username (may be blank)
	Email         string // RFC-5322 email; may be blank on GitHub without user:email scope
	EmailVerified bool   // provider assertion — do NOT trust for security decisions alone
	Name          string // display name (may be blank)
	Picture       string // avatar URL (may be blank)
}

type Provider interface {
	Name() string                             // "google" | "github"
	AuthURL(state, redirectURL string) string // where to send the user's browser
	Exchange(ctx context.Context, code, redirectURL string) (*UserInfo, error)
}

// Registry maps a provider name to its implementation. Providers with
// missing/empty client id or secret are skipped at load time — the
// handlers treat "not in registry" as "disabled".
type Registry struct {
	providers map[string]Provider
	// RedirectURL[name] holds the resolved redirect_uri for each provider:
	// explicit config value if set, otherwise {PublicBaseURL}/api/v4/oauth/{name}/callback.
	RedirectURL map[string]string
}

func NewRegistry(cfg *config.Config) *Registry {
	reg := &Registry{
		providers:   map[string]Provider{},
		RedirectURL: map[string]string{},
	}
	if cfg.OAuthGoogleClientID != "" && cfg.OAuthGoogleClientSecret != "" {
		reg.providers["google"] = &GoogleProvider{
			ClientID:     cfg.OAuthGoogleClientID,
			ClientSecret: cfg.OAuthGoogleClientSecret,
		}
		reg.RedirectURL["google"] = resolveRedirect(cfg.PublicBaseURL, cfg.OAuthGoogleRedirectURL, "google")
	}
	if cfg.OAuthGitHubClientID != "" && cfg.OAuthGitHubClientSecret != "" {
		reg.providers["github"] = &GitHubProvider{
			ClientID:     cfg.OAuthGitHubClientID,
			ClientSecret: cfg.OAuthGitHubClientSecret,
		}
		reg.RedirectURL["github"] = resolveRedirect(cfg.PublicBaseURL, cfg.OAuthGitHubRedirectURL, "github")
	}
	return reg
}

// Get returns the provider by name or nil if disabled/unknown.
func (r *Registry) Get(name string) Provider {
	if r == nil {
		return nil
	}
	return r.providers[name]
}

// EnabledNames returns the provider names that are currently active.
// Useful for the /system/ping surface so clients can show/hide buttons.
func (r *Registry) EnabledNames() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	return out
}

// NewState returns a URL-safe random state string. 32 bytes of entropy is
// overkill for CSRF resistance but costs nothing.
func NewState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

var ErrExchangeFailed = errors.New("oauth exchange failed")

// resolveRedirect returns the explicit override if present, otherwise it
// composes one from PublicBaseURL so an administrator only needs to provide
// the provider credentials in the service settings page.
func resolveRedirect(publicBase, explicit, provider string) string {
	if explicit != "" {
		return explicit
	}
	base := strings.TrimRight(publicBase, "/")
	return fmt.Sprintf("%s/api/v4/oauth/%s/callback", base, provider)
}

// buildQuery is a small helper to avoid url.Values verbosity at callsites.
func buildQuery(pairs ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Set(pairs[i], pairs[i+1])
	}
	return v.Encode()
}
