package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connection-pool and per-session defaults. pgxpool's own defaults are tuned
// for a small tool, not for a chat server: the pool is sized from CPU count,
// connections live forever, and no statement is ever bounded. A stuck query
// would therefore hold a connection until the process restarted.
//
// Every value here is a default, not a policy: an operator who sets the
// corresponding parameter in POSTGRES_DSN keeps their value.
const (
	defaultMaxConns          = 25
	defaultMinConns          = 2
	defaultMaxConnLifetime   = 30 * time.Minute
	defaultMaxConnIdleTime   = 5 * time.Minute
	defaultHealthCheckPeriod = 30 * time.Second

	// Chosen to sit above every legitimate query — the slowest routes already
	// carry a 30s HTTP timeout — while still bounding a pathological one.
	defaultStatementTimeout = "30000"
	// A lock wait is a queueing problem, not a slow query, so it fails much
	// sooner and surfaces as an error instead of a stalled request.
	defaultLockTimeout = "5000"
	// Bounds a transaction abandoned mid-flight, which would otherwise pin a
	// connection and hold its locks indefinitely.
	defaultIdleInTransactionTimeout = "60000"
)

type DB struct {
	Pool *pgxpool.Pool
}

// poolConfig parses the DSN and fills in the defaults above for anything the
// operator did not specify.
//
// Presence is detected against the raw DSN text rather than against the parsed
// result, because pgxpool substitutes its own defaults during parsing and an
// operator value that happens to equal one of them would otherwise be
// indistinguishable from "unset" — and silently overridden.
func poolConfig(dsn string) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if !dsnSpecifies(dsn, "pool_max_conns") {
		config.MaxConns = defaultMaxConns
	}
	if !dsnSpecifies(dsn, "pool_min_conns") {
		config.MinConns = defaultMinConns
	}
	if !dsnSpecifies(dsn, "pool_max_conn_lifetime") {
		config.MaxConnLifetime = defaultMaxConnLifetime
	}
	if !dsnSpecifies(dsn, "pool_max_conn_idle_time") {
		config.MaxConnIdleTime = defaultMaxConnIdleTime
	}
	if !dsnSpecifies(dsn, "pool_health_check_period") {
		config.HealthCheckPeriod = defaultHealthCheckPeriod
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = map[string]string{}
	}
	for name, value := range map[string]string{
		"statement_timeout":                   defaultStatementTimeout,
		"lock_timeout":                        defaultLockTimeout,
		"idle_in_transaction_session_timeout": defaultIdleInTransactionTimeout,
	} {
		if _, ok := config.ConnConfig.RuntimeParams[name]; !ok {
			config.ConnConfig.RuntimeParams[name] = value
		}
	}
	return config, nil
}

// dsnSpecifies reports whether the DSN mentions a parameter, covering both the
// URL form (`?pool_max_conns=10`) and the keyword/value form
// (`pool_max_conns=10 host=...`). A false positive would only mean keeping
// pgxpool's default instead of ours, so the check errs toward the operator.
func dsnSpecifies(dsn, parameter string) bool {
	return strings.Contains(dsn, parameter+"=")
}

// Options tunes what Open wires beyond the pool itself.
type Options struct {
	// QueryObserver, when set, sees every completed statement.
	QueryObserver QueryObserver
}

func Open(ctx context.Context, url string) (*DB, error) {
	return OpenWithOptions(ctx, url, Options{})
}

func OpenWithOptions(ctx context.Context, url string, options Options) (*DB, error) {
	config, err := poolConfig(url)
	if err != nil {
		return nil, err
	}
	if options.QueryObserver != nil {
		config.ConnConfig.Tracer = &queryTracer{observe: options.QueryObserver}
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{Pool: pool}, nil
}

func (d *DB) Close() {
	if d.Pool != nil {
		d.Pool.Close()
	}
}
