// Package metrics exposes a tiny facade over prometheus/client_golang so
// the rest of the codebase doesn't take a direct dependency on the
// Prometheus client types. Everything funnels through the package-level
// Registry singleton, which is cheap and simplifies callers.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry owns the collectors. The zero value is unusable; construct
// with New.
type Registry struct {
	reg *prometheus.Registry

	httpDuration *prometheus.HistogramVec
	postsCreated prometheus.Counter
	wsClients    prometheus.Gauge
	webhookDepth prometheus.Gauge
}

// package-level singleton — single-process server, no isolation needed.
var pkg *Registry = New()

// New builds a fresh Registry with all collectors pre-registered. Called
// once at package init; exposed so tests can spin up a sandbox if they
// ever want to.
func New() *Registry {
	reg := prometheus.NewRegistry()
	// Standard Go process + runtime metrics out of the box.
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	r := &Registry{reg: reg}
	r.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "moyro_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds, labelled by route pattern + method + status.",
		Buckets: prometheus.DefBuckets,
	}, []string{"path", "method", "status"})
	r.postsCreated = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "moyro_posts_created_total",
		Help: "Total number of posts successfully created (excludes rejected / errored).",
	})
	r.wsClients = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "moyro_ws_clients",
		Help: "Currently connected WebSocket clients (not distinct users).",
	})
	r.webhookDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "moyro_webhook_queue_depth",
		Help: "Length of the outgoing webhook dispatcher's job queue.",
	})
	reg.MustRegister(r.httpDuration, r.postsCreated, r.wsClients, r.webhookDepth)
	return r
}

// Handler returns an http.Handler that serves `/metrics` text-format
// output. Mount it unauthenticated at `/metrics`.
func Handler() http.Handler {
	return promhttp.HandlerFor(pkg.reg, promhttp.HandlerOpts{
		EnableOpenMetrics: false,
	})
}

// HTTPMiddleware records duration + status for every request. Uses the
// chi route pattern (e.g. `/channels/{id}/posts`) so label cardinality
// stays bounded — a raw URL path would blow up on UUIDs.
func HTTPMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			pattern := "unknown"
			if rc := chi.RouteContext(r.Context()); rc != nil {
				if p := rc.RoutePattern(); p != "" {
					pattern = p
				}
			}
			pkg.httpDuration.
				WithLabelValues(pattern, r.Method, strconv.Itoa(ww.Status())).
				Observe(time.Since(start).Seconds())
		})
	}
}

// IncPostsCreated bumps the successful-post counter by one. Called from
// the post creation handler after the DB write commits.
func IncPostsCreated() { pkg.postsCreated.Inc() }

// ObserveWSClients sets the current connected-client gauge. Call from
// the hub whenever a client registers or unregisters.
func ObserveWSClients(n int) { pkg.wsClients.Set(float64(n)) }

// ObserveWebhookQueue sets the outgoing-webhook queue-depth gauge.
func ObserveWebhookQueue(n int) { pkg.webhookDepth.Set(float64(n)) }
