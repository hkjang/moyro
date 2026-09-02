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
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
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

// state describes a bucket right after a take attempt. It exists so the 429
// response can tell the client the truth instead of a fixed guess.
type state struct {
	limit      int
	remaining  int
	retryAfter time.Duration
}

// Allow decrements one token for key. Returns false if the bucket is empty.
func (l *Limiter) Allow(key string) bool {
	ok, _ := l.take(key)
	return ok
}

// take decrements one token for key and reports the bucket state that the
// caller may advertise in response headers. When the bucket is empty the
// wait is derived from the configured rate: a signup bucket refilling at one
// token per five seconds must not tell clients to come back in one second,
// or every rejected client retries four times for nothing.
func (l *Limiter) take(key string) (bool, state) {
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

	// Prune buckets that look fully refilled and untouched for a while.
	// Keeps memory bounded without a background goroutine. This runs before
	// the verdict so a key that stays throttled cannot pin every other
	// bucket in memory.
	if now.Sub(l.lastGC) > time.Minute {
		for k, v := range l.by {
			if v.tokens >= l.burst && now.Sub(v.lastFill) > 5*time.Minute {
				delete(l.by, k)
			}
		}
		l.lastGC = now
	}

	if b.tokens < 1 {
		return false, state{limit: int(l.burst), retryAfter: l.waitFor(1 - b.tokens)}
	}
	b.tokens--
	return true, state{limit: int(l.burst), remaining: int(b.tokens)}
}

// waitFor converts a token deficit into refill time. A non-positive rate
// never refills, so there is no honest answer; report zero and let the
// header floor apply.
func (l *Limiter) waitFor(deficit float64) time.Duration {
	if l.rate <= 0 || deficit <= 0 {
		return 0
	}
	return time.Duration(deficit / l.rate * float64(time.Second))
}

// Middleware builds an http.Handler that rejects with 429 when `keyFn`
// yields a key whose bucket is empty. Requests with an empty key bypass
// the limiter (e.g. anonymous routes that pass through the per-user
// middleware — there's a separate per-IP limiter for those).
//
// A rejection carries Retry-After plus the Mattermost-compatible
// X-Ratelimit-Limit/Remaining/Reset headers. Allowed responses stay bare so
// the hot path adds nothing.
func (l *Limiter) Middleware(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)
			if key != "" {
				if allowed, st := l.take(key); !allowed {
					writeThrottled(w, st)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeThrottled answers with the same JSON envelope the rest of the API
// uses. http.Error would label this body text/plain, so a client that keys
// off Content-Type discards a payload it could have shown the user.
//
// Retry-After and X-Ratelimit-Reset are delta-seconds and must be at least
// one: a sub-second wait rounded down to 0 invites an immediate retry.
func writeThrottled(w http.ResponseWriter, st state) {
	seconds := max(1, int(math.Ceil(st.retryAfter.Seconds())))
	retryAfter := strconv.Itoa(seconds)

	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Retry-After", retryAfter)
	h.Set("X-Ratelimit-Limit", strconv.Itoa(st.limit))
	h.Set("X-Ratelimit-Remaining", strconv.Itoa(st.remaining))
	h.Set("X-Ratelimit-Reset", retryAfter)
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = io.WriteString(w, `{"id":"api.ratelimit.exceeded","message":"rate limit exceeded","status_code":429}`+"\n")
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
