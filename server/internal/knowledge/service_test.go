package knowledge

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeSearchInputBoundsAndCanonicalizes(t *testing.T) {
	t.Parallel()
	got, err := normalizeSearchInput(SearchInput{Query: "  장애 대응  ", TeamID: " team-1 ", ChannelID: " channel-1 "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Query != "장애 대응" || got.TeamID != "team-1" || got.ChannelID != "channel-1" || got.Limit != DefaultLimit {
		t.Fatalf("normalized input = %#v", got)
	}
	for _, input := range []SearchInput{
		{Query: "", TeamID: "team-1"},
		{Query: "!!!", TeamID: "team-1"},
		{Query: "query", TeamID: ""},
		{Query: "query\nsecret", TeamID: "team-1"},
		{Query: "query", TeamID: "team\nsecret"},
		{Query: "query", TeamID: "team-1", ChannelID: "channel\tsecret"},
		{Query: strings.Repeat("가", MaxQueryRunes+1), TeamID: "team-1"},
		{Query: "query", TeamID: "team-1", Limit: MaxLimit + 1},
	} {
		if _, err := normalizeSearchInput(input); !errors.Is(err, ErrInvalid) {
			t.Errorf("normalizeSearchInput(%#v) error = %v", input, err)
		}
	}
}

func TestTruncateRunesPreservesUnicodeBoundaries(t *testing.T) {
	t.Parallel()
	got := truncateRunes("가나다라마바사", 4)
	if got != "가나다라…" || !utf8.ValidString(got) {
		t.Fatalf("truncated value = %q", got)
	}
	if got := truncateRunes("short", 10); got != "short" {
		t.Fatalf("short value = %q", got)
	}
}
