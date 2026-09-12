// Package webui serves the production React bundle and provides SPA routing
// fallback without interfering with API routes owned by the main router.
package webui

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/tracking"
)

const DefaultRoot = "/opt/moyro/web"

// CSPReportPath is where browsers post the requests the page policy refused.
// It is named here because the policy that asks for the reports is built here.
const CSPReportPath = "/api/moyro/v1/tracking/csp-report"

// Handler serves immutable built assets and falls back to index.html for
// browser routes. Missing assets deliberately stay 404 so a failed JavaScript
// request is never answered with HTML.
type Handler struct {
	files      fs.FS
	fileServer http.Handler
	index      []byte
	indexTime  time.Time
	// tracking returns the administrator's current visitor-tracking
	// configuration. nil, or a disabled configuration, leaves the page and
	// its policy exactly as they were before tracking existed.
	tracking func() tracking.Config
}

// SetTracking installs the source of the visitor-tracking configuration. It
// is read once per page response so an administrator's change applies to the
// next load without a restart.
func (h *Handler) SetTracking(source func() tracking.Config) {
	h.tracking = source
}

func (h *Handler) trackingConfig() tracking.Config {
	if h.tracking == nil {
		return tracking.Config{}
	}
	return h.tracking()
}

// New opens a production web root on disk.
func New(root string) (*Handler, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("webui: empty root")
	}
	return NewFS(os.DirFS(root))
}

// NewFS constructs a handler from any filesystem. index.html must exist so a
// container cannot report healthy while every browser route is broken.
func NewFS(files fs.FS) (*Handler, error) {
	if files == nil {
		return nil, errors.New("webui: nil filesystem")
	}
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, err
	}
	indexTime := time.Time{}
	if info, statErr := fs.Stat(files, "index.html"); statErr == nil {
		indexTime = info.ModTime()
	}
	return &Handler{
		files:      files,
		fileServer: http.FileServer(http.FS(files)),
		index:      index,
		indexTime:  indexTime,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")

	clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	// The tracking snippet, when an administrator turned one on, is decided
	// once here so the policy header and the injected markup agree on the
	// same nonce. Reserved and asset paths get the untouched strict policy.
	var snippet, nonce string
	config := h.trackingConfig()
	if !reservedRoute(clean) && config.Active(clean) {
		if fresh, err := tracking.NewNonce(); err == nil {
			nonce = fresh
			snippet = config.Snippet(nonce)
		}
	}
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy(r, config, snippet != "", nonce))

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	if reservedRoute(clean) {
		http.NotFound(w, r)
		return
	}
	if clean == "/" || clean == "/index.html" {
		h.serveIndex(w, r, snippet, config.Placement)
		return
	}

	name := strings.TrimPrefix(clean, "/")
	if info, err := fs.Stat(h.files, name); err == nil && info.Mode().IsRegular() {
		if strings.HasPrefix(clean, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		h.fileServer.ServeHTTP(w, r)
		return
	}

	// Mattermost plugin IDs conventionally contain dots. The final segment in
	// this route is an SPA parameter, not a filename extension.
	if pluginSettingsRoute(clean) {
		h.serveIndex(w, r, snippet, config.Placement)
		return
	}
	if clean == "/assets" || strings.HasPrefix(clean, "/assets/") || path.Ext(clean) != "" {
		http.NotFound(w, r)
		return
	}
	h.serveIndex(w, r, snippet, config.Placement)
}

func pluginSettingsRoute(clean string) bool {
	for _, prefix := range []string{
		"/settings/plugins/",
		"/admin/integrations/plugins/",
	} {
		pluginID, ok := strings.CutPrefix(clean, prefix)
		if ok && pluginID != "" && !strings.Contains(pluginID, "/") {
			return true
		}
	}
	return false
}

func reservedRoute(clean string) bool {
	return clean == "/api" || strings.HasPrefix(clean, "/api/") ||
		clean == "/hooks" || strings.HasPrefix(clean, "/hooks/") ||
		clean == "/mcp" || strings.HasPrefix(clean, "/mcp/") ||
		clean == "/healthz" || strings.HasPrefix(clean, "/healthz/") ||
		clean == "/metrics" || strings.HasPrefix(clean, "/metrics/")
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request, snippet, placement string) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if snippet == "" {
		http.ServeContent(w, r, "index.html", h.indexTime, bytes.NewReader(h.index))
		return
	}
	// A page carrying a nonce is unique to this response: no modification
	// time, so a conditional request can never be answered with a 304 that
	// leaves the browser holding a page whose nonce the policy no longer names.
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(injectSnippet(h.index, snippet, placement)))
}

// injectSnippet places the markup just before the closing tag it belongs to,
// falling back to the end of the document when the tag is missing.
func injectSnippet(page []byte, snippet, placement string) []byte {
	marker := []byte("</head>")
	if placement == tracking.PlacementBody {
		marker = []byte("</body>")
	}
	index := bytes.LastIndex(bytes.ToLower(page), marker)
	if index < 0 {
		return append(append(append([]byte(nil), page...), '\n'), snippet+"\n"...)
	}
	out := make([]byte, 0, len(page)+len(snippet)+1)
	out = append(out, page[:index]...)
	out = append(out, snippet...)
	out = append(out, '\n')
	return append(out, page[index:]...)
}

// contentSecurityPolicy bounds what the browser will run and where it may
// connect on behalf of the app. The product loads administrator-installed
// web plugins in the page, so this policy is the line between "a plugin can
// render UI" and "a plugin, or an injected script, can call anywhere".
//
//   - Scripts come from the bundle (`'self'`) and from the `blob:` object
//     URLs the plugin runtime executes fetched bundles through. No inline
//     scripts, no eval, no third-party origins.
//   - Styles allow inline: MUI's emotion runtime injects style elements at
//     runtime, and a nonce cannot be threaded through that code path today.
//   - Connections are limited to this origin, including the WebSocket
//     endpoint, which `'self'` alone does not cover in every browser.
//   - Images and media may be object URLs because authenticated media is
//     fetched with credentials and rendered from a Blob.
//   - Nothing may frame the app, and forms and base URLs stay on-origin.
//
// When an administrator has attached a tracking snippet to this page, the
// policy grows by exactly what that snippet needs: the response's nonce for
// its inline code, the origins the snippet loads from and reports to, and a
// report-uri so whatever is still refused shows up on the admin screen
// instead of only in a visitor's console. It never grows by 'unsafe-inline':
// that would authorize every inline script on the page, and stay that way
// after tracking is turned off again.
func contentSecurityPolicy(r *http.Request, config tracking.Config, tracked bool, nonce string) string {
	host := r.Host
	websocket := ""
	if host != "" {
		websocket = " ws://" + host + " wss://" + host
	}
	scripts, connects, images := "", "", ""
	if tracked {
		extraScripts, extraConnects, extraImages := config.PolicySources()
		scripts = " 'nonce-" + nonce + "'" + joinSources(extraScripts)
		connects = joinSources(extraConnects)
		images = joinSources(extraImages)
	}
	directives := []string{
		"default-src 'self'",
		"script-src 'self' blob:" + scripts,
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:" + images,
		"media-src 'self' blob:",
		"font-src 'self' data:",
		"connect-src 'self'" + websocket + connects,
		"worker-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}
	if tracked {
		directives = append(directives, "report-uri "+CSPReportPath)
	}
	return strings.Join(directives, "; ")
}

func joinSources(sources []string) string {
	if len(sources) == 0 {
		return ""
	}
	return " " + strings.Join(sources, " ")
}
