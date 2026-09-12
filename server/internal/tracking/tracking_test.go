package tracking

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func momento(proxy bool) Config {
	config := Default()
	config.Enabled = true
	config.Provider = ProviderMomento
	config.MomentoURL = "https://momento.corp.example/"
	config.MomentoSiteID = "moyro-prd"
	config.MomentoProxy = proxy
	if err := config.Validate(); err != nil {
		panic(err)
	}
	return config
}

func TestDefaultIsOffAndInert(t *testing.T) {
	config := Default()
	if config.Enabled || config.Active("/today") || config.ProxiesMomento() || config.Snippet("n") != "" {
		t.Fatalf("default config is not inert: %+v", config)
	}
	scripts, connects, images := config.PolicySources()
	if len(scripts)+len(connects)+len(images) != 0 {
		t.Fatalf("default config adds policy sources: %v %v %v", scripts, connects, images)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("default config does not validate: %v", err)
	}
}

func TestMomentoProxyKeepsTheCollectorOutOfThePolicy(t *testing.T) {
	config := momento(true)
	snippet := config.Snippet("n0nce")
	for _, expected := range []string{`src="/momento/tracker.js"`, `data-endpoint="/momento"`, `data-site-id="moyro-prd"`, `nonce="n0nce"`} {
		if !strings.Contains(snippet, expected) {
			t.Fatalf("snippet lacks %s: %s", expected, snippet)
		}
	}
	if strings.Contains(snippet, "momento.corp.example") {
		t.Fatalf("proxied snippet names the collector: %s", snippet)
	}
	scripts, connects, images := config.PolicySources()
	if len(scripts)+len(connects)+len(images) != 0 {
		t.Fatalf("proxied momento should add no policy source, got %v %v %v", scripts, connects, images)
	}
	if !config.ProxiesMomento() {
		t.Fatal("proxy should be open")
	}

	direct := momento(false)
	if !strings.Contains(direct.Snippet(""), `src="https://momento.corp.example/tracker.js"`) {
		t.Fatalf("direct snippet = %s", direct.Snippet(""))
	}
	scripts, connects, _ = direct.PolicySources()
	if !slices.Contains(scripts, "https://momento.corp.example") || !slices.Contains(connects, "https://momento.corp.example") {
		t.Fatalf("direct momento must allow its origin: %v %v", scripts, connects)
	}
	if direct.ProxiesMomento() {
		t.Fatal("proxy must stay closed when the snippet talks to the collector directly")
	}
}

func TestAdminScreensAreSkippedUnlessAsked(t *testing.T) {
	config := momento(true)
	for _, path := range []string{"/admin", "/admin/site", "/settings", "/settings/profile"} {
		if config.Active(path) {
			t.Fatalf("%s should not be tracked by default", path)
		}
	}
	for _, path := range []string{"/", "/today", "/workspace/t/channel/c", "/administration"} {
		if !config.Active(path) {
			t.Fatalf("%s should be tracked", path)
		}
	}
	config.IncludeAdmin = true
	if !config.Active("/admin/site") {
		t.Fatal("include_admin should track the console")
	}
	config.Enabled = false
	if config.Active("/today") {
		t.Fatal("disabled config must never be active")
	}
}

func TestValidateRejectsWhatCannotWork(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"unknown provider", func(c *Config) { c.Provider = "piwik" }, "provider must be one of"},
		{"enabled without provider", func(c *Config) { c.Enabled = true }, "choose a provider"},
		{"momento without site", func(c *Config) { c.Enabled = true; c.Provider = ProviderMomento; c.MomentoURL = "https://m.example" }, "momento_site_id"},
		{"momento relative url", func(c *Config) { c.Provider = ProviderMomento; c.MomentoURL = "/momento" }, "absolute http(s)"},
		{"ga4 without id", func(c *Config) { c.Enabled = true; c.Provider = ProviderGA4 }, "measurement_id"},
		{"custom empty", func(c *Config) { c.Enabled = true; c.Provider = ProviderCustom }, "custom_snippet is empty"},
		{"custom too large", func(c *Config) { c.Provider = ProviderCustom; c.CustomSnippet = strings.Repeat("x", MaxSnippetBytes+1) }, "8192 bytes"},
		{"allowed host with path", func(c *Config) { c.AllowedHosts = []string{"https://a.example/collect"} }, "allowed host"},
		{"site id with quote", func(c *Config) { c.MomentoSiteID = `a"b` }, "momento_site_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := Default()
			tc.mutate(&config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}

	// A disabled, half-filled form saves, and the size limit still applies.
	config := Default()
	config.Provider = ProviderMomento
	config.AllowedHosts = []string{" pixel.corp.example ", "HTTPS://pixel.corp.example/", "", "*.stats.example"}
	if err := config.Validate(); err != nil {
		t.Fatalf("disabled config should validate: %v", err)
	}
	if want := []string{"https://pixel.corp.example", "https://*.stats.example"}; !slices.Equal(config.AllowedHosts, want) {
		t.Fatalf("allowed hosts = %v, want %v", config.AllowedHosts, want)
	}
}

func TestNonceIsAppliedToEveryScriptTag(t *testing.T) {
	config := Default()
	config.Enabled = true
	config.Provider = ProviderCustom
	config.CustomSnippet = `<script src="https://t.example/a.js"></script>
<SCRIPT>window.x=1</SCRIPT>
<script nonce="keep">window.y=2</script>`
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	snippet := config.Snippet("abc+/=")
	if got := strings.Count(snippet, `nonce="abc+/="`); got != 2 {
		t.Fatalf("nonce applied %d times, want 2: %s", got, snippet)
	}
	if !strings.Contains(snippet, `<script nonce="keep">`) {
		t.Fatalf("existing nonce was replaced: %s", snippet)
	}
	if config.Snippet("") != config.CustomSnippet {
		t.Fatal("an empty nonce must leave the snippet untouched")
	}
}

func TestSnippetOriginsAreReadIntoThePolicy(t *testing.T) {
	config := Default()
	config.Enabled = true
	config.Provider = ProviderCustom
	config.CustomSnippet = `<script src="https://momento.corp.example/tracker.js"></script>
<script>window.__t={endpoint:"https://momento.corp.example/collect/v1/events",pixel:'http://pixel.corp.example:8080/p.gif?id=1'};fetch("https://Momento.corp.example/x")</script>`
	config.AllowedHosts = []string{"https://extra.example"}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	scripts, connects, images := config.PolicySources()
	want := []string{"https://momento.corp.example", "http://pixel.corp.example:8080", "https://extra.example"}
	for _, group := range [][]string{scripts, connects, images} {
		if !slices.Equal(group, want) {
			t.Fatalf("sources = %v, want %v", group, want)
		}
	}
	if !config.AllowsOrigin("https://extra.example/") || config.AllowsOrigin("https://other.example") {
		t.Fatal("AllowsOrigin disagrees with PolicySources")
	}
	ga := Default()
	ga.Provider = ProviderGA4
	if !ga.AllowsOrigin("https://region1.google-analytics.com") {
		t.Fatal("wildcard policy entries must count as allowed")
	}
}

func TestAddAllowedHostDeduplicates(t *testing.T) {
	hosts := AddAllowedHost([]string{"https://a.example"}, "https://b.example/")
	hosts = AddAllowedHost(hosts, "HTTPS://A.EXAMPLE")
	hosts = AddAllowedHost(hosts, "not a url")
	hosts = AddAllowedHost(hosts, "")
	if want := []string{"https://a.example", "https://b.example"}; !slices.Equal(hosts, want) {
		t.Fatalf("hosts = %v, want %v", hosts, want)
	}
}

func TestNewNonceIsFreshPerCall(t *testing.T) {
	first, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := NewNonce()
	if first == second || len(first) < 20 {
		t.Fatalf("nonces %q and %q", first, second)
	}
}

func TestRecorderKeepsDistinctOriginsNotCounts(t *testing.T) {
	recorder := NewRecorder()
	clock := time.Unix(1_700_000_000, 0)
	recorder.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	for range 5 {
		recorder.Record("https://momento.corp.example/collect/v1/events", "connect-src", "https://moyro.example/today")
	}
	recorder.Record("https://momento.corp.example/tracker.js", "script-src-elem 'self'", "/today")
	recorder.Record("chrome-extension://abc/x.js", "script-src", "/today")
	recorder.Record("data", "img-src", "/today")
	recorder.Record("https://momento.corp.example/x", "", "/today")

	items := recorder.List(Default())
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Directive != "connect-src" || items[0].Count != 6 || items[0].Origin != "https://momento.corp.example" {
		t.Fatalf("most recent = %+v", items[0])
	}
	if items[1].Directive != "script-src-elem" || items[1].Count != 1 || items[1].Allowed {
		t.Fatalf("second = %+v", items[1])
	}

	allowed := Default()
	allowed.AllowedHosts = []string{"https://momento.corp.example"}
	if listed := recorder.List(allowed); !listed[0].Allowed || !listed[1].Allowed {
		t.Fatalf("allowed origins should be marked: %+v", listed)
	}

	for index := range MaxViolations + 10 {
		recorder.Record(fmt.Sprintf("https://host%d.example/pixel.gif", index), "img-src", "/")
	}
	if got := len(recorder.List(Default())); got > MaxViolations {
		t.Fatalf("recorder holds %d entries, want at most %d", got, MaxViolations)
	}
	recorder.Forget()
	if len(recorder.List(Default())) != 0 {
		t.Fatal("Forget left entries behind")
	}
}
