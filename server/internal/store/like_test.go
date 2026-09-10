package store

import "testing"

// TestEscapeLikeQuotesEveryPatternMetacharacter pins the three characters that
// used to leak out of a user-typed search term and into the pattern grammar.
func TestEscapeLikeQuotesEveryPatternMetacharacter(t *testing.T) {
	cases := []struct {
		name string
		term string
		want string
	}{
		{"plain term is untouched", "general", "general"},
		{"korean term is untouched", "공지사항", "공지사항"},
		{"percent becomes a literal", "50%", `50\%`},
		{"underscore becomes a literal", "john_doe", `john\_doe`},
		{"backslash is doubled", `a\b`, `a\\b`},
		{"trailing backslash cannot eat the appended wildcard", `dir\`, `dir\\`},
		{"an all-wildcard term matches only itself", "%_%", `\%\_\%`},
		{"an already escaped sequence is escaped again", `\%`, `\\\%`},
		{"empty term stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EscapeLike(tc.term); got != tc.want {
				t.Fatalf("EscapeLike(%q) = %q, want %q", tc.term, got, tc.want)
			}
		})
	}
}

// TestLikePatternHelpersWrapTheEscapedTerm checks that the wildcards the query
// wants are the only unescaped ones in the finished pattern.
func TestLikePatternHelpersWrapTheEscapedTerm(t *testing.T) {
	if got, want := LikeContains("50%"), `%50\%%`; got != want {
		t.Fatalf("LikeContains = %q, want %q", got, want)
	}
	if got, want := LikePrefix("50%"), `50\%%`; got != want {
		t.Fatalf("LikePrefix = %q, want %q", got, want)
	}
	// An empty term still has to mean "everything" for the callers that use
	// the pattern as an unfiltered list.
	if got, want := LikeContains(""), "%%"; got != want {
		t.Fatalf("LikeContains(\"\") = %q, want %q", got, want)
	}
}
