package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/settings"
	"github.com/hkjang/moyro/server/internal/tracking"
)

// Visitor tracking: the administrator-managed snippet the web UI attaches to
// its pages, the browser reports of what the page policy refused, and the
// same-origin proxy that keeps a Momento collector out of that policy.
//
// The configuration lives in the settings store under section "tracking" and
// is held in memory like the site settings, because the page handler reads it
// on every HTML response.

const trackingSettingsSection = "tracking"

// maxCSPReportBytes keeps an unauthenticated endpoint from being used to push
// large bodies at the server. A browser report is a few hundred bytes.
const maxCSPReportBytes = 8 * 1024

// maxMomentoProxyBodyBytes bounds one forwarded event batch.
const maxMomentoProxyBodyBytes = 256 << 10

func (n *nativeServices) currentTracking() tracking.Config {
	if n != nil {
		if current := n.tracking.Load(); current != nil {
			copied := *current
			// Never nil: the admin screen reads the list as a JSON array.
			copied.AllowedHosts = append([]string{}, current.AllowedHosts...)
			return copied
		}
	}
	return tracking.Default()
}

func (n *nativeServices) reloadTracking(ctx context.Context) error {
	value := tracking.Default()
	err := n.loadJSON(ctx, trackingSettingsSection, nativeSettingsKey, &value)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	n.applyTracking(value)
	return nil
}

func (n *nativeServices) applyTracking(value tracking.Config) {
	copied := value
	copied.AllowedHosts = append([]string{}, value.AllowedHosts...)
	n.tracking.Store(&copied)
}

// trackingConfig is what the page handler and the proxy read. Without the
// management services there is nowhere to configure tracking, so it is off.
func (h *handlers) trackingConfig() tracking.Config {
	if h.native == nil {
		return tracking.Default()
	}
	return h.native.currentTracking()
}

type cspReport struct {
	Report struct {
		BlockedURI         string `json:"blocked-uri"`
		ViolatedDirective  string `json:"violated-directive"`
		EffectiveDirective string `json:"effective-directive"`
		DocumentURI        string `json:"document-uri"`
	} `json:"csp-report"`
}

// receiveCSPReport records what a browser refused to load. It is
// unauthenticated because the browser sends it without credentials, and it
// always answers 204 so a misbehaving page never sees an error from us. While
// tracking is off no page asks for reports, and none are kept.
func (h *handlers) receiveCSPReport(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	if h.violations == nil || !h.trackingConfig().Enabled {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCSPReportBytes))
	if err != nil || len(body) == 0 {
		return
	}
	var report cspReport
	if json.Unmarshal(body, &report) != nil {
		return
	}
	directive := report.Report.EffectiveDirective
	if directive == "" {
		directive = report.Report.ViolatedDirective
	}
	h.violations.Record(report.Report.BlockedURI, directive, report.Report.DocumentURI)
}

// listTrackingViolations shows the administrator which addresses the policy
// is blocking, so a snippet can be fixed without reading a visitor's console.
func (h *handlers) listTrackingViolations(w http.ResponseWriter, r *http.Request) {
	items := []tracking.Violation{}
	if h.violations != nil {
		items = h.violations.List(h.trackingConfig())
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// clearTrackingViolations forgets the recorded reports, which is how an
// administrator checks whether a change actually fixed the snippet.
func (h *handlers) clearTrackingViolations(w http.ResponseWriter, r *http.Request) {
	if h.violations != nil {
		h.violations.Forget()
	}
	w.WriteHeader(http.StatusNoContent)
}

// momentoProxy forwards /momento/* to the configured collector so the page
// only ever talks to its own origin. It answers only while Momento tracking
// with the proxy is on; turning tracking off closes it. Credentials for this
// service are stripped so a session cookie never reaches the collector.
func (h *handlers) momentoProxy(w http.ResponseWriter, r *http.Request) {
	config := h.trackingConfig()
	if !config.ProxiesMomento() {
		http.NotFound(w, r)
		return
	}
	target, err := url.Parse(config.MomentoURL)
	if err != nil || target.Host == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions:
	default:
		writeError(w, http.StatusMethodNotAllowed, "api.moyro.tracking.proxy_method", "method not allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, tracking.MomentoProxyPrefix)
	if rest == "" {
		rest = "/"
	}
	if !strings.HasPrefix(rest, "/") || strings.Contains(rest, "..") {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMomentoProxyBodyBytes)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.URL.Path = strings.TrimRight(target.Path, "/") + rest
			request.Out.URL.RawPath = ""
			request.Out.Host = target.Host
			request.SetXForwarded()
			for _, header := range []string{"Cookie", "Authorization", "X-Requested-With"} {
				request.Out.Header.Del(header)
			}
		},
		Transport: momentoTransport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			if h.logger != nil {
				h.logger.Warn("momento proxy upstream failed", "err", err)
			}
			writeError(w, http.StatusBadGateway, "api.moyro.tracking.proxy_upstream", "the Momento collector did not answer")
		},
	}
	proxy.ServeHTTP(w, r)
}

// momentoTransport bounds how long a page waits on the collector. A tracker
// is best-effort; a stalled collector must not pin server connections.
var momentoTransport http.RoundTripper = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: 10 * time.Second,
	IdleConnTimeout:       90 * time.Second,
	MaxIdleConns:          16,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: time.Second,
}
