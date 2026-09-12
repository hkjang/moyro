// Package tracking lets an administrator attach a visitor-analytics snippet to
// the served web pages without weakening the content security policy.
//
// The web UI ships with `script-src 'self'`, so a pasted snippet is silently
// refused by the browser and the administrator has no way to tell why. This
// package produces both halves of the answer: the markup to inject, carrying
// a per-request nonce on every script tag, and the extra policy sources the
// snippet needs so the policy can stay strict for everything else.
//
// Momento, the self-hosted collector, comes first. With the same-origin proxy
// (`/momento/*`) no external origin appears in the policy at all, which is the
// only shape that works on installations whose policy cannot change.
package tracking

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	ProviderNone    = "none"
	ProviderMomento = "momento"
	ProviderGA4     = "ga4"
	ProviderGTM     = "gtm"
	ProviderMatomo  = "matomo"
	ProviderCustom  = "custom"

	PlacementHead = "head"
	PlacementBody = "body"

	// MaxSnippetBytes bounds a pasted snippet. Real loaders are a few hundred
	// bytes; anything larger is a mistake, not a tracker.
	MaxSnippetBytes = 8 * 1024

	// MomentoProxyPrefix is the same-origin path the app forwards to the
	// Momento collector when the proxy is on.
	MomentoProxyPrefix = "/momento"

	maxAllowedHosts = 32
)

// Config is the administrator-managed tracking configuration. It is stored as
// one JSON document in the settings store and edited from the admin screen,
// because a collector address differs per installation and changes while the
// service runs — a build-time variable would stay off forever.
type Config struct {
	Enabled       bool   `json:"enabled"`
	Provider      string `json:"provider"`
	MomentoURL    string `json:"momento_url"`
	MomentoSiteID string `json:"momento_site_id"`
	// MomentoProxy routes the tracker and its events through this origin so
	// the collector never has to be named in the policy. Default on.
	MomentoProxy  bool     `json:"momento_proxy"`
	MeasurementID string   `json:"measurement_id"`
	MatomoURL     string   `json:"matomo_url"`
	MatomoSiteID  string   `json:"matomo_site_id"`
	CustomSnippet string   `json:"custom_snippet"`
	AllowedHosts  []string `json:"allowed_hosts"`
	IncludeAdmin  bool     `json:"include_admin"`
	Placement     string   `json:"placement"`
}

// Default is what a fresh installation runs with: nothing attached.
func Default() Config {
	return Config{Provider: ProviderNone, MomentoProxy: true, AllowedHosts: []string{}, Placement: PlacementHead}
}

// Validate normalizes the configuration in place and reports what is missing
// for the chosen provider. A disabled configuration is always valid so an
// administrator can save a half-filled form with the switch off.
func (c *Config) Validate() error {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.Provider == "" {
		c.Provider = ProviderNone
	}
	c.Placement = strings.ToLower(strings.TrimSpace(c.Placement))
	if c.Placement != PlacementBody {
		c.Placement = PlacementHead
	}
	c.MomentoURL = trimOrigin(c.MomentoURL)
	c.MomentoSiteID = strings.TrimSpace(c.MomentoSiteID)
	c.MeasurementID = strings.TrimSpace(c.MeasurementID)
	c.MatomoURL = trimOrigin(c.MatomoURL)
	c.MatomoSiteID = strings.TrimSpace(c.MatomoSiteID)
	c.CustomSnippet = strings.TrimSpace(c.CustomSnippet)
	if c.AllowedHosts == nil {
		c.AllowedHosts = []string{}
	}

	switch c.Provider {
	case ProviderNone, ProviderMomento, ProviderGA4, ProviderGTM, ProviderMatomo, ProviderCustom:
	default:
		return errors.New("provider must be one of none, momento, ga4, gtm, matomo, custom")
	}
	// Size limits apply even while disabled: the row is stored either way.
	if len(c.CustomSnippet) > MaxSnippetBytes {
		return fmt.Errorf("custom snippet must not exceed %d bytes", MaxSnippetBytes)
	}
	if !utf8.ValidString(c.CustomSnippet) || strings.ContainsRune(c.CustomSnippet, 0) {
		return errors.New("custom snippet must be valid UTF-8 text")
	}
	hosts := make([]string, 0, len(c.AllowedHosts))
	seen := make(map[string]struct{}, len(c.AllowedHosts))
	for _, raw := range c.AllowedHosts {
		origin, err := policyOrigin(raw)
		if err != nil {
			return fmt.Errorf("allowed host %q: %w", strings.TrimSpace(raw), err)
		}
		if origin == "" {
			continue
		}
		if _, duplicate := seen[origin]; duplicate {
			continue
		}
		seen[origin] = struct{}{}
		hosts = append(hosts, origin)
	}
	if len(hosts) > maxAllowedHosts {
		return fmt.Errorf("at most %d allowed hosts can be configured", maxAllowedHosts)
	}
	c.AllowedHosts = hosts
	for _, field := range []struct{ name, value string }{
		{"momento_site_id", c.MomentoSiteID}, {"measurement_id", c.MeasurementID}, {"matomo_site_id", c.MatomoSiteID},
	} {
		if len(field.value) > 128 || strings.ContainsAny(field.value, "\"'<>\r\n\x00") {
			return fmt.Errorf("%s must be a short identifier without quotes or angle brackets", field.name)
		}
	}
	for _, field := range []struct{ name, value string }{{"momento_url", c.MomentoURL}, {"matomo_url", c.MatomoURL}} {
		if field.value != "" && !isAbsoluteHTTP(field.value) {
			return fmt.Errorf("%s must be an absolute http(s) URL", field.name)
		}
	}

	if !c.Enabled {
		return nil
	}
	switch c.Provider {
	case ProviderNone:
		return errors.New("choose a provider before enabling tracking")
	case ProviderMomento:
		if c.MomentoURL == "" || c.MomentoSiteID == "" {
			return errors.New("momento_url and momento_site_id are required")
		}
	case ProviderGA4, ProviderGTM:
		if c.MeasurementID == "" {
			return errors.New("measurement_id is required")
		}
	case ProviderMatomo:
		if c.MatomoURL == "" || c.MatomoSiteID == "" {
			return errors.New("matomo_url and matomo_site_id are required")
		}
	case ProviderCustom:
		if c.CustomSnippet == "" {
			return errors.New("custom_snippet is empty")
		}
	}
	return nil
}

// Active reports whether the page at path should carry the snippet. The
// administration and personal-settings screens are excluded unless asked for,
// because console traffic is rarely the visitor data anybody wants. Non-page
// paths never qualify; the router keeps those away from the page handler.
func (c Config) Active(path string) bool {
	if !c.Enabled || c.Provider == ProviderNone || c.Provider == "" {
		return false
	}
	if !c.IncludeAdmin && (path == "/admin" || strings.HasPrefix(path, "/admin/") ||
		path == "/settings" || strings.HasPrefix(path, "/settings/")) {
		return false
	}
	return strings.TrimSpace(c.Snippet("")) != ""
}

// ProxiesMomento reports whether `/momento/*` should be forwarded to the
// collector. It is the only condition under which the proxy answers at all,
// so turning tracking off also closes the proxy.
func (c Config) ProxiesMomento() bool {
	return c.Enabled && c.Provider == ProviderMomento && c.MomentoProxy && c.MomentoURL != ""
}

// Snippet renders the markup to inject. Every script tag carries the nonce so
// the page policy can name that nonce instead of allowing inline code at large.
func (c Config) Snippet(nonce string) string {
	switch c.Provider {
	case ProviderMomento:
		base := trimOrigin(c.MomentoURL)
		site := html.EscapeString(strings.TrimSpace(c.MomentoSiteID))
		if base == "" || site == "" {
			return ""
		}
		if c.MomentoProxy {
			return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js" data-site-id="%s" data-endpoint="%s" data-environment="prd" data-contract-version="1"></script>`,
				MomentoProxyPrefix, site, MomentoProxyPrefix), nonce)
		}
		return withNonce(fmt.Sprintf(`<script async src="%s/tracker.js" data-site-id="%s" data-environment="prd" data-contract-version="1"></script>`,
			html.EscapeString(base), site), nonce)
	case ProviderGA4:
		id := html.EscapeString(strings.TrimSpace(c.MeasurementID))
		if id == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script async src="https://www.googletagmanager.com/gtag/js?id=%s"></script>
<script>window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag('js',new Date());gtag('config','%s');</script>`, id, id), nonce)
	case ProviderGTM:
		id := html.EscapeString(strings.TrimSpace(c.MeasurementID))
		if id == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>(function(w,d,s,l,i){w[l]=w[l]||[];w[l].push({'gtm.start':new Date().getTime(),event:'gtm.js'});var f=d.getElementsByTagName(s)[0],j=d.createElement(s),dl=l!='dataLayer'?'&l='+l:'';j.async=true;j.src='https://www.googletagmanager.com/gtm.js?id='+i+dl;f.parentNode.insertBefore(j,f);})(window,document,'script','dataLayer','%s');</script>`, id), nonce)
	case ProviderMatomo:
		base := trimOrigin(c.MatomoURL)
		site := html.EscapeString(strings.TrimSpace(c.MatomoSiteID))
		if base == "" || site == "" {
			return ""
		}
		return withNonce(fmt.Sprintf(`<script>var _paq=window._paq=window._paq||[];_paq.push(['trackPageView']);_paq.push(['enableLinkTracking']);(function(){var u="%s/";_paq.push(['setTrackerUrl',u+'matomo.php']);_paq.push(['setSiteId','%s']);var d=document,g=d.createElement('script'),s=d.getElementsByTagName('script')[0];g.async=true;g.src=u+'matomo.js';s.parentNode.insertBefore(g,s);})();</script>`, html.EscapeString(base), site), nonce)
	case ProviderCustom:
		return withNonce(strings.TrimSpace(c.CustomSnippet), nonce)
	}
	return ""
}

// PolicySources lists the extra origins the snippet needs, derived from the
// provider so a common setup needs no policy knowledge at all. Momento behind
// the proxy contributes nothing: everything it loads is same-origin.
func (c Config) PolicySources() (scripts, connects, images []string) {
	add := func(origin string) {
		scripts = append(scripts, origin)
		connects = append(connects, origin)
		images = append(images, origin)
	}
	switch c.Provider {
	case ProviderMomento:
		if !c.MomentoProxy {
			if origin := originOf(c.MomentoURL); origin != "" {
				add(origin)
			}
		}
	case ProviderGA4, ProviderGTM:
		scripts = append(scripts, "https://www.googletagmanager.com")
		connects = append(connects, "https://www.google-analytics.com", "https://analytics.google.com", "https://*.google-analytics.com")
		images = append(images, "https://www.google-analytics.com", "https://www.googletagmanager.com")
	case ProviderMatomo:
		if origin := originOf(c.MatomoURL); origin != "" {
			add(origin)
		}
	case ProviderCustom:
		// A pasted snippet names the addresses it loads and reports to, so
		// those origins are allowed without anybody reading a policy error.
		for _, origin := range SnippetOrigins(c.CustomSnippet) {
			add(origin)
		}
	}
	for _, host := range c.AllowedHosts {
		add(host)
	}
	return scripts, connects, images
}

// AllowsOrigin reports whether the current configuration already permits the
// origin, including wildcard entries such as https://*.google-analytics.com.
func (c Config) AllowsOrigin(origin string) bool {
	origin = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(origin), "/"))
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	scripts, connects, images := c.PolicySources()
	for _, group := range [][]string{scripts, connects, images} {
		for _, entry := range group {
			entry = strings.ToLower(strings.TrimSuffix(entry, "/"))
			if entry == origin {
				return true
			}
			if star := strings.Index(entry, "*."); star >= 0 &&
				strings.HasPrefix(origin, entry[:star]) && strings.HasSuffix(parsed.Host, entry[star+1:]) {
				return true
			}
		}
	}
	return false
}

// AddAllowedHost appends one origin to the allow list unless it is already
// present, leaving the existing order alone. It is the one-click fix for a
// reported violation.
func AddAllowedHost(existing []string, origin string) []string {
	origin, err := policyOrigin(origin)
	if err != nil || origin == "" {
		return existing
	}
	for _, host := range existing {
		if strings.EqualFold(host, origin) {
			return existing
		}
	}
	return append(append([]string(nil), existing...), origin)
}

// NewNonce returns a fresh base64 nonce for one page response. The policy
// names it and every injected script tag carries it; a nonce that repeats
// across responses would let a cached page authorize scripts it never saw.
func NewNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// SnippetOrigins lists every http(s) origin written into a snippet: the
// script it loads, the endpoint it posts to, the pixel it requests. A tracker
// almost always writes its own address somewhere in its loader.
func SnippetOrigins(snippet string) []string {
	origins := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	lower := strings.ToLower(snippet)
	for index := 0; index < len(snippet); {
		start := strings.Index(lower[index:], "http")
		if start < 0 {
			break
		}
		start += index
		end := start
		for end < len(snippet) && !isURLBoundary(snippet[end]) {
			end++
		}
		index = end
		origin := originOf(snippet[start:end])
		if origin == "" {
			continue
		}
		if _, duplicate := seen[origin]; duplicate {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins
}

// withNonce adds the nonce to every script tag that does not already carry
// one, which is what lets a pasted snippet run under a strict policy.
func withNonce(snippet, nonce string) string {
	if nonce == "" || snippet == "" {
		return snippet
	}
	var builder strings.Builder
	remaining := snippet
	for {
		index := strings.Index(strings.ToLower(remaining), "<script")
		if index < 0 {
			builder.WriteString(remaining)
			return builder.String()
		}
		end := index + len("<script")
		builder.WriteString(remaining[:end])
		tag := remaining[end:]
		if closing := strings.Index(tag, ">"); closing >= 0 {
			tag = tag[:closing]
		}
		if !strings.Contains(strings.ToLower(tag), "nonce=") {
			builder.WriteString(` nonce="` + html.EscapeString(nonce) + `"`)
		}
		remaining = remaining[end:]
	}
}

// isURLBoundary reports the characters that cannot appear in a URL written
// inside HTML or JavaScript, which is where each address ends.
func isURLBoundary(letter byte) bool {
	switch letter {
	case '"', '\'', '`', '<', '>', ' ', '\t', '\n', '\r', ')', ',', ';', '\\', '+':
		return true
	}
	return false
}

// originOf reduces a URL to scheme://host, or "" when it is not an http(s)
// address a policy could name.
func originOf(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + strings.ToLower(parsed.Host)
}

// policyOrigin canonicalizes one allow-list entry. A bare host is taken as
// https; a wildcard host such as *.example.com is kept for the policy.
func policyOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if strings.ContainsAny(raw, " \t\r\n\"'<>;,\x00") {
		return "", errors.New("must be a single http(s) origin")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("must be an http(s) origin without a path, query, credentials, or fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("must use http or https")
	}
	host := strings.ToLower(parsed.Host)
	if strings.Contains(host, "*") && !strings.HasPrefix(host, "*.") {
		return "", errors.New("a wildcard must be the leading label, like *.example.com")
	}
	return scheme + "://" + host, nil
}

func trimOrigin(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func isAbsoluteHTTP(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil
}
