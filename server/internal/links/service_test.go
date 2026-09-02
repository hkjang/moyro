package links

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/hkjang/moyro/server/internal/posts"
)

type stubResolver struct {
	ips []net.IP
	err error
}

func (s stubResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return s.ips, s.err
}

func TestExtractDedupesAndCaps(t *testing.T) {
	message := "see https://example.com/a and https://example.com/a plus " +
		"https://example.com/b, https://example.com/c. and https://example.com/d"
	got := Extract(message)
	want := []string{"https://example.com/a", "https://example.com/b", "https://example.com/c"}
	if len(got) != len(want) {
		t.Fatalf("extract returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extract[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractSkipsNonHTTPSchemes(t *testing.T) {
	if got := Extract("ftp://example.com/file mailto:someone@example.com"); got != nil {
		t.Fatalf("expected no URLs, got %v", got)
	}
}

func TestParseOpenGraphPrefersOGOverTitleTag(t *testing.T) {
	doc := `<html><head><title>title tag</title>` +
		`<meta name="description" content="fallback">` +
		`<meta property="og:title" content="og title">` +
		`<meta property="og:description" content="og description">` +
		`<meta property="og:image" content="/card.png">` +
		`</head><body><meta property="og:title" content="ignored"></body></html>`
	og := parseOpenGraph(strings.NewReader(doc))
	if og.Title != "og title" {
		t.Fatalf("title = %q, want %q", og.Title, "og title")
	}
	if og.Description != "og description" {
		t.Fatalf("description = %q, want %q", og.Description, "og description")
	}
	if og.Image != "/card.png" {
		t.Fatalf("image = %q, want %q", og.Image, "/card.png")
	}
}

func TestParseOpenGraphFallsBackToTitleTag(t *testing.T) {
	og := parseOpenGraph(strings.NewReader(`<html><head><title>only title</title></head><body></body></html>`))
	if og.Title != "only title" {
		t.Fatalf("title = %q, want %q", og.Title, "only title")
	}
}

func TestResolveImageURLJoinsRelativeHref(t *testing.T) {
	got := resolveImageURL("https://example.com/posts/1", "/card.png")
	if got != "https://example.com/card.png" {
		t.Fatalf("resolved = %q", got)
	}
	if got := resolveImageURL("https://example.com/posts/1", "https://cdn.example.net/c.png"); got != "https://cdn.example.net/c.png" {
		t.Fatalf("absolute image url must pass through, got %q", got)
	}
}

func TestBlockedIPCoversNonPublicRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "224.0.0.1",
		"100.64.0.1", "192.0.0.192", "198.18.0.1", "240.0.0.1",
		"::1", "fc00::1", "fe80::1", "::", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "2001:db8::1",
	}
	for _, raw := range blocked {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("test address %q did not parse", raw)
		}
		if !blockedIP(ip) {
			t.Errorf("%s must be blocked", raw)
		}
	}

	allowed := []string{"93.184.216.34", "1.1.1.1", "2606:4700:4700::1111"}
	for _, raw := range allowed {
		if blockedIP(net.ParseIP(raw)) {
			t.Errorf("%s must be allowed", raw)
		}
	}
}

// A resolved hostname must be dialed by address. Handing the hostname back to
// the dialer would resolve it a second time, letting a rebinding answer swap a
// private address in after the guard ran.
func TestDialTargetsPinsResolvedAddresses(t *testing.T) {
	resolver := stubResolver{ips: []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("2606:4700::1")}}
	targets, err := dialTargets(context.Background(), resolver, "example.com:443")
	if err != nil {
		t.Fatalf("dialTargets: %v", err)
	}
	want := []string{"93.184.216.34:443", "[2606:4700::1]:443"}
	if len(targets) != len(want) {
		t.Fatalf("targets = %v, want %v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("targets[%d] = %q, want %q", i, targets[i], want[i])
		}
	}
}

func TestDialTargetsRejectsMixedPrivateAnswer(t *testing.T) {
	resolver := stubResolver{ips: []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")}}
	if _, err := dialTargets(context.Background(), resolver, "rebind.example.com:80"); err == nil {
		t.Fatal("expected a private address in the answer to reject the dial")
	} else if !strings.Contains(err.Error(), blockedAddrReason) {
		t.Fatalf("err = %v, want the blocked-address reason", err)
	}
}

func TestDialTargetsRejectsPrivateLiteralWithoutResolving(t *testing.T) {
	resolver := stubResolver{err: errors.New("resolver must not be called for an IP literal")}
	if _, err := dialTargets(context.Background(), resolver, "169.254.169.254:80"); err == nil {
		t.Fatal("expected the link-local literal to be blocked")
	} else if !strings.Contains(err.Error(), blockedAddrReason) {
		t.Fatalf("err = %v, want the blocked-address reason", err)
	}
}

func TestDialTargetsRejectsEmptyAnswer(t *testing.T) {
	if _, err := dialTargets(context.Background(), stubResolver{}, "example.com:80"); err == nil {
		t.Fatal("expected an empty DNS answer to fail closed")
	}
}

func TestSafeDialContextBlocksLoopbackListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	conn, err := safeDialContext(context.Background(), "tcp", listener.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("expected loopback dial to be refused")
	}
	if !strings.Contains(err.Error(), blockedAddrReason) {
		t.Fatalf("err = %v, want the blocked-address reason", err)
	}
}

func TestCacheEvictsOldestBeyondMaxSize(t *testing.T) {
	service := New()
	for i := 0; i < cacheMaxSize+5; i++ {
		key := strings.Repeat("u", i+1)
		service.putCache(key, posts.LinkPreview{URL: key})
	}
	if len(service.cache) != cacheMaxSize {
		t.Fatalf("cache holds %d entries, want %d", len(service.cache), cacheMaxSize)
	}
	if _, ok := service.getCache("u"); ok {
		t.Fatal("oldest entry should have been evicted")
	}
	newest := strings.Repeat("u", cacheMaxSize+5)
	if _, ok := service.getCache(newest); !ok {
		t.Fatal("newest entry should still be cached")
	}
}
