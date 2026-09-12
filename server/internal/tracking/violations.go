package tracking

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxViolations bounds the recorder. Blocked requests repeat on every page
// view, so the interesting information is which origins are blocked, not how
// many times — a small buffer of distinct origins is enough to fix a snippet.
const MaxViolations = 100

// Violation is one origin the content security policy refused, kept with the
// directive that refused it so the admin screen can say what to allow.
type Violation struct {
	Origin    string `json:"origin"`
	Directive string `json:"directive"`
	Page      string `json:"page"`
	Count     int    `json:"count"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	// Allowed marks a report the current configuration already permits, so a
	// fixed snippet stops nagging without the administrator clearing the list.
	Allowed bool `json:"allowed"`
}

// Recorder collects policy violations reported by browsers. It is deliberately
// in memory: the reports are a live troubleshooting aid for the person pasting
// a snippet, not an audit record, and keeping them out of the database means
// an unauthenticated endpoint can never grow storage.
type Recorder struct {
	mu         sync.Mutex
	violations map[string]*Violation
	now        func() time.Time
}

func NewRecorder() *Recorder {
	return &Recorder{violations: make(map[string]*Violation), now: time.Now}
}

// Record notes one blocked request. Anything that is not an http origin, such
// as a browser extension or a data: URL, is ignored because allowing it is
// neither possible nor useful.
func (r *Recorder) Record(blockedURI, directive, page string) {
	origin := originOf(blockedURI)
	if origin == "" {
		return
	}
	directive = strings.TrimSpace(strings.ToLower(directive))
	if index := strings.IndexByte(directive, ' '); index > 0 {
		directive = directive[:index]
	}
	if directive == "" || len(directive) > 40 {
		directive = "connect-src"
	}
	if len(page) > 512 {
		page = page[:512]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := directive + " " + origin
	moment := r.now().UnixMilli()
	if existing, found := r.violations[key]; found {
		existing.Count++
		existing.LastSeen = moment
		existing.Page = page
		return
	}
	if len(r.violations) >= MaxViolations {
		r.evictOldestLocked()
	}
	r.violations[key] = &Violation{Origin: origin, Directive: directive, Page: page, Count: 1, FirstSeen: moment, LastSeen: moment}
}

func (r *Recorder) evictOldestLocked() {
	oldestKey := ""
	var oldest int64
	for key, violation := range r.violations {
		if oldestKey == "" || violation.LastSeen < oldest {
			oldestKey, oldest = key, violation.LastSeen
		}
	}
	delete(r.violations, oldestKey)
}

// List returns the blocked origins, most recent first, marking the ones the
// configuration already allows.
func (r *Recorder) List(config Config) []Violation {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]Violation, 0, len(r.violations))
	for _, violation := range r.violations {
		copied := *violation
		copied.Allowed = config.AllowsOrigin(copied.Origin)
		items = append(items, copied)
	}
	sort.Slice(items, func(first, second int) bool {
		if items[first].LastSeen == items[second].LastSeen {
			return items[first].Origin < items[second].Origin
		}
		return items[first].LastSeen > items[second].LastSeen
	})
	return items
}

// Forget drops the recorded violations, which is what an administrator does
// after fixing a snippet to check whether anything is still blocked.
func (r *Recorder) Forget() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.violations = make(map[string]*Violation)
}
