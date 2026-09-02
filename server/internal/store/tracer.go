package store

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueryObserver receives one call per completed query with the statement's
// leading text, its duration, and its error. Observers must be fast and must
// not touch the database: they run on the querying goroutine.
type QueryObserver func(query string, duration time.Duration, err error)

// queryTracer is the pgx hook that feeds a QueryObserver. Without it, a slow
// statement is visible only as unexplained request latency; the HTTP
// histogram says a route was slow but not which query made it so.
type queryTracer struct {
	observe QueryObserver
}

type queryStartKey struct{}

type queryStart struct {
	sql string
	at  time.Time
}

func (t *queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey{}, queryStart{sql: data.SQL, at: time.Now()})
}

func (t *queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	start, ok := ctx.Value(queryStartKey{}).(queryStart)
	if !ok || t.observe == nil {
		return
	}
	t.observe(SummarizeQuery(start.sql), time.Since(start.at), data.Err)
}

// summaryLength bounds how much of a statement an observer sees. Enough to
// recognise the query in the source; not enough to log a whole document.
const summaryLength = 160

var whitespace = strings.NewReplacer("\n", " ", "\t", " ", "\r", " ")

// SummarizeQuery collapses whitespace and truncates a statement for logs and
// labels. Parameters are never part of the SQL text pgx hands the tracer, so
// nothing user-supplied can appear here.
func SummarizeQuery(sql string) string {
	collapsed := strings.Join(strings.Fields(whitespace.Replace(sql)), " ")
	if len(collapsed) > summaryLength {
		return collapsed[:summaryLength] + "…"
	}
	return collapsed
}

// SlowQueryLog returns an observer that reports statements over `threshold`
// through `report`, rate-limited so a stalled database cannot flood the log
// with one line per query. Faster statements are ignored entirely.
func SlowQueryLog(threshold time.Duration, report func(query string, duration time.Duration, err error)) QueryObserver {
	var mu sync.Mutex
	var window time.Time
	var reported int
	const perMinute = 30
	return func(query string, duration time.Duration, err error) {
		if duration < threshold {
			return
		}
		mu.Lock()
		now := time.Now()
		if now.Sub(window) >= time.Minute {
			window = now
			reported = 0
		}
		reported++
		allowed := reported <= perMinute
		mu.Unlock()
		if allowed {
			report(query, duration, err)
		}
	}
}
