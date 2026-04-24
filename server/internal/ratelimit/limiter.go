// Package ratelimit provides two simple limiters:
//
//   - PerUser: token-bucket keyed on the authenticated userID placed in
//     context by the auth middleware. Protects write endpoints from a
//     single client flooding the server.
//   - PerIP:   token-bucket keyed on RemoteAddr. Used on /users/login to
//     defeat password guessing without locking legitimate traffic.
//
// Implementation is deliberately small — a map of buckets with lazy
// refill on each take. No background goroutine. Stale entries are pruned
// opportunistically so long-lived processes don't leak memory.
package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type bucket struct {
	tokens   float64
	lastFill time.Time
}

// Limiter grants `rate` tokens per second up to `burst` stored tokens.
// It's safe for concurrent use. The limiter is O(1) per Allow call.
type Limiter struct {
	rate   float64 // tokens per second
	burst  float64 // max tokens held
	mu     sync.Mutex
	by     map[string]*bucket
	lastGC time.Time
}

func New(ratePerSec float64, burst int) *Limiter {
	return &Limiter{
		rate:   ratePerSec,
		burst:  float64(burst),
		by:     map[string]*bucket{},
		lastGC: time.Now(),
	}
}

// Allow decrements one token for key. Returns false if the bucket is empty.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.by[key]
	if !ok {
		b = &bucket{tokens: l.burst, lastFill: now}
		l.by[key] = b
	}
	elapsed := now.Sub(b.lastFill).Seconds()
	if elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+elapsed*l.rate)
		b.lastFill = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--

	// Prune buckets that look fully refilled and untouched for a while.
	// Keeps memory bounded without a background goroutine.
	if now.Sub(l.lastGC) > time.Minute {
		for k, v := range l.by {
			if v.tokens >= l.burst && now.Sub(v.lastFill) > 5*time.Minute {
				delete(l.by, k)
			}
		}
		l.lastGC = now
	}
	return true
}

// Middleware builds an http.Handler that rejects with 429 when `keyFn`
// yields a key whose bucket is empty. Requests with an empty key bypass
// the limiter (e.g. anonymous routes that pass through the per-user
// middleware — there's a separate per-IP limiter for those).
func (l *Limiter) Middleware(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)
			if key != "" && !l.Allow(key) {
				w.Header().Set("Retry-After", "1")
				http.Error(w, `{"id":"api.ratelimit.exceeded","message":"rate limit exceeded","status_code":429}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP returns the best-effort client address for keying the per-IP
// limiter. RemoteAddr may include a port so we strip it.
func ClientIP(r *http.Request) string {
	if r.RemoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
