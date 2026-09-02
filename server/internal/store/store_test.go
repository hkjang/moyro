package store

import (
	"testing"
	"time"
)

// TestPoolConfigAppliesServiceDefaults pins the pool and per-session bounds a
// chat server needs. pgxpool's own defaults leave every statement unbounded, so
// a single stuck query would hold its connection until the process restarted.
func TestPoolConfigAppliesServiceDefaults(t *testing.T) {
	config, err := poolConfig("postgres://user:pass@localhost:5432/moyro?sslmode=disable")
	if err != nil {
		t.Fatalf("parse plain DSN: %v", err)
	}
	if config.MaxConns != defaultMaxConns {
		t.Errorf("MaxConns = %d, want %d", config.MaxConns, defaultMaxConns)
	}
	if config.MinConns != defaultMinConns {
		t.Errorf("MinConns = %d, want %d", config.MinConns, defaultMinConns)
	}
	if config.MaxConnLifetime != defaultMaxConnLifetime {
		t.Errorf("MaxConnLifetime = %s, want %s", config.MaxConnLifetime, defaultMaxConnLifetime)
	}
	if config.MaxConnIdleTime != defaultMaxConnIdleTime {
		t.Errorf("MaxConnIdleTime = %s, want %s", config.MaxConnIdleTime, defaultMaxConnIdleTime)
	}
	if config.HealthCheckPeriod != defaultHealthCheckPeriod {
		t.Errorf("HealthCheckPeriod = %s, want %s", config.HealthCheckPeriod, defaultHealthCheckPeriod)
	}
	for name, want := range map[string]string{
		"statement_timeout":                   defaultStatementTimeout,
		"lock_timeout":                        defaultLockTimeout,
		"idle_in_transaction_session_timeout": defaultIdleInTransactionTimeout,
	} {
		if got := config.ConnConfig.RuntimeParams[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestPoolConfigKeepsOperatorOverrides is the reason presence is detected from
// the DSN text: pgxpool substitutes its own defaults while parsing, so an
// operator value equal to one of them must still survive.
func TestPoolConfigKeepsOperatorOverrides(t *testing.T) {
	dsn := "postgres://user:pass@localhost:5432/moyro?sslmode=disable" +
		"&pool_max_conns=7&pool_min_conns=1&pool_max_conn_lifetime=2h" +
		"&pool_max_conn_idle_time=45s&pool_health_check_period=90s" +
		"&statement_timeout=1234"
	config, err := poolConfig(dsn)
	if err != nil {
		t.Fatalf("parse overriding DSN: %v", err)
	}
	if config.MaxConns != 7 {
		t.Errorf("MaxConns = %d, want the operator's 7", config.MaxConns)
	}
	if config.MinConns != 1 {
		t.Errorf("MinConns = %d, want the operator's 1", config.MinConns)
	}
	if config.MaxConnLifetime != 2*time.Hour {
		t.Errorf("MaxConnLifetime = %s, want 2h", config.MaxConnLifetime)
	}
	if config.MaxConnIdleTime != 45*time.Second {
		t.Errorf("MaxConnIdleTime = %s, want 45s", config.MaxConnIdleTime)
	}
	if config.HealthCheckPeriod != 90*time.Second {
		t.Errorf("HealthCheckPeriod = %s, want 90s", config.HealthCheckPeriod)
	}
	if got := config.ConnConfig.RuntimeParams["statement_timeout"]; got != "1234" {
		t.Errorf("statement_timeout = %q, want the operator's 1234", got)
	}
	// Unspecified session bounds still receive the service default.
	if got := config.ConnConfig.RuntimeParams["lock_timeout"]; got != defaultLockTimeout {
		t.Errorf("lock_timeout = %q, want %q", got, defaultLockTimeout)
	}
}

// TestPoolConfigSupportsKeywordValueDSNs covers the non-URL DSN form, which the
// presence check has to recognise too.
func TestPoolConfigSupportsKeywordValueDSNs(t *testing.T) {
	config, err := poolConfig("host=localhost user=moyro dbname=moyro pool_max_conns=3")
	if err != nil {
		t.Fatalf("parse keyword/value DSN: %v", err)
	}
	if config.MaxConns != 3 {
		t.Errorf("MaxConns = %d, want the operator's 3", config.MaxConns)
	}
	if config.MinConns != defaultMinConns {
		t.Errorf("MinConns = %d, want the default %d", config.MinConns, defaultMinConns)
	}
}

func TestPoolConfigRejectsInvalidDSN(t *testing.T) {
	if _, err := poolConfig("://not-a-dsn"); err == nil {
		t.Fatal("invalid DSN accepted")
	}
}
