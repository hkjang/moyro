// Package metrics exposes a tiny facade over prometheus/client_golang so
// the rest of the codebase doesn't take a direct dependency on the
// Prometheus client types. Everything funnels through the package-level
// Registry singleton, which is cheap and simplifies callers.
package metrics

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
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
	ssoStages    *prometheus.CounterVec
	ssoDuration  *prometheus.HistogramVec
	wsUsers      prometheus.Gauge
	wsDrops      *prometheus.CounterVec
	dbPool       *prometheus.GaugeVec
	dbQuery      *prometheus.HistogramVec

	// The hub and the pool expose cumulative totals, while Prometheus
	// counters only move forward by a delta. Remember what was already
	// reported so a restart of the sampling loop cannot double-count.
	dropMu      sync.Mutex
	lastWSDrops map[string]int64
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
	r.ssoStages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moyro_sso_stage_total",
		Help: "SSO flow stages by bounded provider, stage, and result labels.",
	}, []string{"provider", "stage", "result"})
	r.ssoDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "moyro_sso_stage_duration_seconds",
		Help:    "SSO stage latency in seconds by bounded provider, stage, and result labels.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	}, []string{"provider", "stage", "result"})
	r.wsUsers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "moyro_ws_users",
		Help: "Distinct users with at least one open WebSocket client.",
	})
	r.wsDrops = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moyro_ws_events_dropped_total",
		Help: "WebSocket events the hub discarded, by reason: queue_full (delivery fell behind), slow_consumer (a client was not reading), audience_unresolved (authorization failed closed).",
	}, []string{"reason"})
	r.dbPool = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "moyro_db_pool_connections",
		Help: "PostgreSQL pool connections by state: acquired, idle, total, max, constructing.",
	}, []string{"state"})
	r.dbQuery = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "moyro_db_query_duration_seconds",
		Help:    "PostgreSQL statement latency by outcome. Unlabelled by statement on purpose: the slow-query log names the statement, the histogram sizes the problem.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"outcome"})
	r.lastWSDrops = map[string]int64{}
	reg.MustRegister(
		r.httpDuration, r.postsCreated, r.wsClients, r.webhookDepth,
		r.ssoStages, r.ssoDuration, r.wsUsers, r.wsDrops, r.dbPool, r.dbQuery,
	)
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

// ObserveWSUsers sets the distinct-connected-user gauge. Unlike the client
// gauge this counts people, not tabs, so it can be compared with the audience
// sizes the fan-out path resolves.
func ObserveWSUsers(n int) { pkg.wsUsers.Set(float64(n)) }

// ObserveWSDrops advances the dropped-event counters from the hub's cumulative
// totals. Dropping is how the hub stays responsive under load, but until it is
// counted an operator has no way to see that clients missed events.
func ObserveWSDrops(queueFull, slowConsumer, audienceUnresolved int64) {
	pkg.dropMu.Lock()
	defer pkg.dropMu.Unlock()
	for reason, total := range map[string]int64{
		"queue_full":          queueFull,
		"slow_consumer":       slowConsumer,
		"audience_unresolved": audienceUnresolved,
	} {
		previous := pkg.lastWSDrops[reason]
		if total < previous {
			// A total that moved backwards can only mean a fresh counter;
			// resynchronise instead of reporting a negative delta.
			previous = 0
		}
		if delta := total - previous; delta > 0 {
			pkg.wsDrops.WithLabelValues(reason).Add(float64(delta))
		}
		pkg.lastWSDrops[reason] = total
	}
}

// ObserveDBQuery records one completed statement. Statement text is not a
// label — it would be unbounded — so the histogram only separates success
// from failure; the slow-query log carries the statement.
func ObserveDBQuery(duration time.Duration, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	pkg.dbQuery.WithLabelValues(outcome).Observe(duration.Seconds())
}

// ObserveDBPool publishes PostgreSQL pool saturation. Exhaustion shows up as
// acquired approaching max with idle at zero, which is otherwise only visible
// as unexplained request latency.
func ObserveDBPool(stat *pgxpool.Stat) {
	if stat == nil {
		return
	}
	pkg.dbPool.WithLabelValues("acquired").Set(float64(stat.AcquiredConns()))
	pkg.dbPool.WithLabelValues("idle").Set(float64(stat.IdleConns()))
	pkg.dbPool.WithLabelValues("total").Set(float64(stat.TotalConns()))
	pkg.dbPool.WithLabelValues("max").Set(float64(stat.MaxConns()))
	pkg.dbPool.WithLabelValues("constructing").Set(float64(stat.ConstructingConns()))
}

func boundedSSOLabel(value string, allowed []string, fallback string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

// ObserveSSOStage intentionally accepts only bounded labels. Provider errors,
// identities, issuer URLs, codes, and other high-cardinality/secret values
// never enter the metrics registry.
func ObserveSSOStage(provider, stage, result string, duration time.Duration) {
	provider = boundedSSOLabel(provider, []string{"keycloak", "google", "github", "browser"}, "other")
	stage = boundedSSOLabel(stage, []string{"login", "callback", "exchange"}, "other")
	result = boundedSSOLabel(result, []string{
		"success", "disabled", "invalid", "provider_error", "exchange_error",
		"resolve_error", "session_error", "internal_error",
	}, "internal_error")
	pkg.ssoStages.WithLabelValues(provider, stage, result).Inc()
	pkg.ssoDuration.WithLabelValues(provider, stage, result).Observe(duration.Seconds())
}
