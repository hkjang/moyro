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
	const prefix = "/settings/plugins/"
	pluginID, ok := strings.CutPrefix(clean, prefix)
	return ok && pluginID != "" && !strings.Contains(pluginID, "/")
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
