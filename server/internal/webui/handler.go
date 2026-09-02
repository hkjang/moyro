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
)

const DefaultRoot = "/opt/moyro/web"

// Handler serves immutable built assets and falls back to index.html for
// browser routes. Missing assets deliberately stay 404 so a failed JavaScript
// request is never answered with HTML.
type Handler struct {
	files      fs.FS
	fileServer http.Handler
	index      []byte
	indexTime  time.Time
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
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy(r))

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
	if reservedRoute(clean) {
		http.NotFound(w, r)
		return
	}
	if clean == "/" || clean == "/index.html" {
		h.serveIndex(w, r)
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
		h.serveIndex(w, r)
		return
	}
	if clean == "/assets" || strings.HasPrefix(clean, "/assets/") || path.Ext(clean) != "" {
		http.NotFound(w, r)
		return
	}
	h.serveIndex(w, r)
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

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", h.indexTime, bytes.NewReader(h.index))
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
func contentSecurityPolicy(r *http.Request) string {
	host := r.Host
	websocket := ""
	if host != "" {
		websocket = " ws://" + host + " wss://" + host
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' blob:",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"media-src 'self' blob:",
		"font-src 'self' data:",
		"connect-src 'self'" + websocket,
		"worker-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}
