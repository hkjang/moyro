package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestQueryTracerReportsDurationAndError(t *testing.T) {
	var got struct {
		query string
		dur   time.Duration
		err   error
	}
	tracer := &queryTracer{observe: func(query string, duration time.Duration, err error) {
		got.query, got.dur, got.err = query, duration, err
	}}
	ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{
		SQL: "SELECT\n\tid\nFROM   users WHERE id = $1",
	})
	time.Sleep(2 * time.Millisecond)
	boom := errors.New("boom")
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: boom})

	if got.query != "SELECT id FROM users WHERE id = $1" {
		t.Fatalf("query summary = %q", got.query)
	}
	if got.dur < 2*time.Millisecond {
		t.Fatalf("duration = %s, want at least 2ms", got.dur)
	}
	if !errors.Is(got.err, boom) {
		t.Fatalf("err = %v, want boom", got.err)
	}
}

func TestQueryTracerIgnoresEndWithoutStart(t *testing.T) {
	called := false
	tracer := &queryTracer{observe: func(string, time.Duration, error) { called = true }}
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{})
	if called {
		t.Fatal("observer called without a matching start")
	}
}

func TestSummarizeQueryTruncates(t *testing.T) {
	long := "SELECT " + string(make([]byte, 400))
	if got := SummarizeQuery(long); len([]rune(got)) > summaryLength+1 {
		t.Fatalf("summary not truncated: %d runes", len([]rune(got)))
	}
}

func TestSlowQueryLogFiltersAndRateLimits(t *testing.T) {
	var reports int
	observe := SlowQueryLog(100*time.Millisecond, func(string, time.Duration, error) { reports++ })

	observe("fast", 5*time.Millisecond, nil)
	if reports != 0 {
		t.Fatalf("fast query reported")
	}
	for i := 0; i < 100; i++ {
		observe("slow", 2*time.Second, nil)
	}
	if reports != 30 {
		t.Fatalf("reports = %d, want the 30-per-minute cap", reports)
	}
}
