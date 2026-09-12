package tracking

import "testing"

// A byte index taken from strings.ToLower(s) is not an index into s. Some runes
// change length when they fold — U+212A KELVIN SIGN is three bytes and becomes a
// one-byte 'k', U+0130 'İ' is two and becomes three — so every position after
// one of them is off. The reviewer's case: the nonce landed inside the tag name.
func TestANonceGoesOnTheTagEvenAfterARuneThatFoldsToADifferentLength(t *testing.T) {
	for _, snippet := range []string{
		"İİİİ<script>1</script>",
		"KK<script>1</script>",
		"<script>1</script>",
	} {
		got := withNonce(snippet, "n0nce")
		if !containsFold(got, `<script nonce="n0nce"`) {
			t.Errorf("withNonce(%q) = %q; the nonce is not on the tag", snippet, got)
		}
		if containsFold(got, "<sc nonce=") || containsFold(got, "<scr nonce=") {
			t.Errorf("withNonce(%q) = %q; the nonce landed inside the tag name", snippet, got)
		}
	}
}

// The same index mistake made SnippetOrigins read past the end of the snippet.
func TestOriginsAreFoundAfterARuneThatFoldsToADifferentLength(t *testing.T) {
	for _, snippet := range []string{
		`İİİİ<script src="https://collector.example/tracker.js"></script>`,
		"K<script src=\"HTTPS://collector.example/tracker.js\"></script>",
	} {
		origins := SnippetOrigins(snippet)
		found := false
		for _, origin := range origins {
			if containsFold(origin, "collector.example") {
				found = true
			}
		}
		if !found {
			t.Errorf("SnippetOrigins(%q) = %v; the collector origin is missing", snippet, origins)
		}
	}
}
