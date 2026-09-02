// Package links handles OpenGraph link previews. The service extracts URLs
// from a post message, fetches each one (with a strict timeout + body cap +
// RFC1918 guard), and parses title/description/image out of meta tags. An
// in-memory LRU cache dedupes identical URLs across posts; the cache TTL is
// 24h so a stale preview doesn't hang around forever.
//
// Fetches run off the post's critical path — the HTTP handler kicks off a
// goroutine after the post is persisted and a post_edited WS event re-
// broadcasts the post with populated link_metadata once done.
package links

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hkjang/moyro/server/internal/posts"
	"golang.org/x/net/html"
)

// MaxURLsPerPost caps how many links we unfurl per post. Three is generous
// for real messages and keeps a pathological "100 links" post from doing
// 100 HTTP fetches.
const MaxURLsPerPost = 3

const (
	fetchTimeout = 5 * time.Second
	maxBodyBytes = 512 * 1024
	cacheTTL     = 24 * time.Hour
	cacheMaxSize = 1000
	userAgent    = "moyro-link-preview/1.0"
)

// urlRegex is deliberately permissive on the right side; we run the matched
// string through url.Parse next so garbage gets filtered there, not here.
var urlRegex = regexp.MustCompile(`https?://[^\s<>"')]+`)

// Extract returns up to MaxURLsPerPost deduped absolute URLs found in
// `message`. Preserves first-occurrence order so "here's two links"
// previews render left-to-right in the same order the user wrote them.
func Extract(message string) []string {
	matches := urlRegex.FindAllString(message, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		// Strip common trailing punctuation that isn't part of a URL even
		// though it matched the regex (periods at end-of-sentence, closing
		// parentheses in markdown emphasis, etc.).
		m = strings.TrimRight(m, ".,!?);:")
		if _, dup := seen[m]; dup {
			continue
		}
		u, err := url.Parse(m)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
		if len(out) >= MaxURLsPerPost {
			break
		}
	}
	return out
}

// Service wraps the fetch pipeline + cache. A single Service is shared by
// all goroutines; the cache lock is fine-grained (map-level mutex) and
// fetches themselves don't hold it.
type Service struct {
	client *http.Client
	mu     sync.Mutex
	cache  map[string]cacheEntry
	order  []string // LRU: most-recently-used at the end
}

type cacheEntry struct {
	preview  posts.LinkPreview
	inserted time.Time
}

// New builds a Service with a private http.Client whose DialContext blocks
// RFC1918 / loopback / link-local destinations. The client is NOT reused
// elsewhere; keep the SSRF guard local to link previews.
func New() *Service {
	return &Service{
		client: &http.Client{
			Timeout: fetchTimeout,
			Transport: &http.Transport{
				DialContext: safeDialContext,
				// Never follow to an internal redirect either — check on
				// every hop by wrapping DialContext, not just the first.
				MaxIdleConns:        20,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 3 * time.Second,
			},
		},
		cache: map[string]cacheEntry{},
		order: []string{},
	}
}

// Fetch returns a LinkPreview for the URL, using the cache when possible.
// A missing/failed fetch returns a preview with just URL + FetchedAt so
// the caller can still show "link to <host>" rather than omit the card.
func (s *Service) Fetch(ctx context.Context, rawURL string) posts.LinkPreview {
	now := time.Now()
	if cached, ok := s.getCache(rawURL); ok {
		return cached
	}

	preview := posts.LinkPreview{URL: rawURL, FetchedAt: now.UnixMilli()}

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		s.putCache(rawURL, preview)
		return preview
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := s.client.Do(req)
	if err != nil {
		s.putCache(rawURL, preview)
		return preview
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		s.putCache(rawURL, preview)
		return preview
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "text/html") && !strings.HasPrefix(ct, "application/xhtml") {
		// Direct image link (Content-Type: image/*) — treat the URL itself
		// as the image so the preview card still renders a thumbnail.
		if strings.HasPrefix(ct, "image/") {
			preview.ImageURL = rawURL
			preview.Title = hostname(rawURL)
		}
		s.putCache(rawURL, preview)
		return preview
	}

	// Cap the body so a malicious server streaming forever doesn't hang us.
	limited := io.LimitReader(resp.Body, maxBodyBytes)
	og := parseOpenGraph(limited)
	if og.Title != "" {
		preview.Title = og.Title
	} else {
		preview.Title = hostname(rawURL)
	}
	preview.Description = og.Description
	if og.Image != "" {
		preview.ImageURL = resolveImageURL(rawURL, og.Image)
	}

	s.putCache(rawURL, preview)
	return preview
}

func (s *Service) getCache(key string) (posts.LinkPreview, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok {
		return posts.LinkPreview{}, false
	}
	if time.Since(e.inserted) > cacheTTL {
		delete(s.cache, key)
		return posts.LinkPreview{}, false
	}
	return e.preview, true
}

func (s *Service) putCache(key string, p posts.LinkPreview) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cache[key]; !exists {
		s.order = append(s.order, key)
	}
	s.cache[key] = cacheEntry{preview: p, inserted: time.Now()}
	// Evict oldest until size is under cap.
	for len(s.order) > cacheMaxSize {
		old := s.order[0]
		s.order = s.order[1:]
		delete(s.cache, old)
	}
}

// ogMeta is the subset of OpenGraph + HTML we care about.
type ogMeta struct {
	Title       string
	Description string
	Image       string
}

// parseOpenGraph walks the HTML looking for <meta property="og:*" content=...>
// tags (plus fallbacks to <title> and <meta name="description">). We stop
// scanning once <body> starts — OG metadata is always in <head>.
func parseOpenGraph(r io.Reader) ogMeta {
	var out ogMeta
	var titleTag string
	tk := html.NewTokenizer(r)
	for {
		tt := tk.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			tag, _ := tk.TagName()
			tagName := string(tag)
			if tagName == "body" {
				// Stop scanning; OG metadata is always in <head>.
				if out.Title == "" {
					out.Title = titleTag
				}
				return out
			}
			if tagName == "title" {
				tt2 := tk.Next()
				if tt2 == html.TextToken {
					titleTag = strings.TrimSpace(string(tk.Text()))
				}
				continue
			}
			if tagName != "meta" {
				continue
			}
			var prop, name, content string
			for {
				k, v, more := tk.TagAttr()
				switch strings.ToLower(string(k)) {
				case "property":
					prop = string(v)
				case "name":
					name = string(v)
				case "content":
					content = string(v)
				}
				if !more {
					break
				}
			}
			switch strings.ToLower(prop) {
			case "og:title":
				out.Title = content
			case "og:description":
				out.Description = content
			case "og:image":
				out.Image = content
			}
			// Fallback to <meta name="description"> when OG isn't set.
			if out.Description == "" && strings.EqualFold(name, "description") {
				out.Description = content
			}
		}
	}
	if out.Title == "" {
		out.Title = titleTag
	}
	return out
}

// ipResolver is the slice of *net.Resolver this package needs. Taking it as a
// parameter keeps the address policy testable without a live DNS server.
type ipResolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

const blockedAddrReason = "link preview: private address blocked"

// blockedNets lists ranges net.IP's own predicates do not cover. A preview
// fetch has no business reaching any of them, and several (carrier-grade NAT,
// the IETF protocol block that holds 192.0.0.192) route to infrastructure a
// tenant could otherwise probe through this server.
var blockedNets = func() []*net.IPNet {
	cidrs := []string{
		"100.64.0.0/10",   // RFC 6598 carrier-grade NAT
		"192.0.0.0/24",    // RFC 6890 IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // RFC 2544 benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved, including the broadcast address
		"2001:db8::/32",   // IPv6 documentation
		"100::/64",        // IPv6 discard-only
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, parsed, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		nets = append(nets, parsed)
	}
	return nets
}()

// blockedIP reports whether an address is outside the public internet the
// preview fetcher is allowed to reach.
func blockedIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, blocked := range blockedNets {
		if blocked.Contains(ip) {
			return true
		}
	}
	return false
}

// dialTargets resolves `addr` and returns the concrete `ip:port` endpoints the
// dialer may connect to. Returning addresses rather than the original host is
// the point: handing the hostname back to the dialer would resolve it a second
// time, so a DNS answer that flips to 127.0.0.1 between the check and the
// connection would pass the guard. Any blocked address in the answer rejects
// the whole dial, so a mixed public/private record cannot be retried into.
func dialTargets(ctx context.Context, resolver ipResolver, addr string) ([]string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips := []net.IP{}
	if literal := net.ParseIP(host); literal != nil {
		ips = append(ips, literal)
	} else {
		resolved, err := resolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		ips = resolved
	}
	if len(ips) == 0 {
		return nil, &net.AddrError{Err: blockedAddrReason, Addr: host}
	}
	targets := make([]string, 0, len(ips))
	for _, ip := range ips {
		if blockedIP(ip) {
			return nil, &net.AddrError{Err: blockedAddrReason, Addr: ip.String()}
		}
		targets = append(targets, net.JoinHostPort(ip.String(), port))
	}
	return targets, nil
}

// safeDialContext refuses to connect to loopback, RFC1918, link-local, or
// unspecified addresses. Runs on every dial including redirects, so we
// can't be tricked into hitting a metadata service via a 302 on a public URL.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	targets, err := dialTargets(ctx, net.DefaultResolver, addr)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	d.Timeout = 3 * time.Second
	var lastErr error
	for _, target := range targets {
		conn, err := d.DialContext(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func hostname(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Host
}

// FetchImage proxies a remote image through the service. Used by the webapp
// link-preview image endpoint so browsers never dial third-party hosts
// directly. The same SSRF guard as Fetch applies (via the shared client's
// DialContext); a 5 MiB body cap plus a strict image/* content-type check
// keep this endpoint from being abused as a general proxy.
func (s *Service) FetchImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/*")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", &url.Error{Op: "GET", URL: rawURL, Err: errStatus(resp.StatusCode)}
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "image/") {
		return nil, "", &url.Error{Op: "GET", URL: rawURL, Err: errNotImage}
	}
	const maxImageBytes = 5 * 1024 * 1024
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return nil, "", err
	}
	return buf, resp.Header.Get("Content-Type"), nil
}

type staticError string

func (e staticError) Error() string { return string(e) }

const (
	errNotImage staticError = "link preview: non-image content-type"
)

func errStatus(code int) error {
	return staticError("link preview: upstream status " + strconv.Itoa(code))
}

// resolveImageURL turns a possibly-relative og:image href into an absolute URL
// by joining it against the page URL. Already-absolute values pass through.
func resolveImageURL(pageURL, imageURL string) string {
	pu, err := url.Parse(pageURL)
	if err != nil {
		return imageURL
	}
	iu, err := url.Parse(imageURL)
	if err != nil {
		return imageURL
	}
	return pu.ResolveReference(iu).String()
}
