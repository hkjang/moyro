package documents

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDocumentTextValidationUsesRuneLimitsAndRejectsControls(t *testing.T) {
	t.Parallel()
	if got, err := normalizeRequiredText("  운영 문서  ", MaxTitleRunes, false); err != nil || got != "운영 문서" {
		t.Fatalf("normalize title = %q, %v", got, err)
	}
	if got, err := normalizeRequiredText(" 첫 줄\n\t둘째 줄 ", MaxBodyRunes, true); err != nil || got != "첫 줄\n\t둘째 줄" {
		t.Fatalf("normalize body = %q, %v", got, err)
	}
	for _, value := range []string{"", "제목\n둘째 줄", "널\x00문자", strings.Repeat("가", MaxTitleRunes+1)} {
		if _, err := normalizeRequiredText(value, MaxTitleRunes, false); !errors.Is(err, ErrInvalid) {
			t.Errorf("normalizeRequiredText(%q) error = %v", value, err)
		}
	}
	if _, err := normalizeRequiredText(strings.Repeat("문", MaxBodyRunes+1), MaxBodyRunes, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized body error = %v", err)
	}
}

func TestDocumentIdentifierValidationIsBounded(t *testing.T) {
	t.Parallel()
	if got, err := normalizeIdentifier(" source-1 ", MaxIdentifierRunes, true); err != nil || got != "source-1" {
		t.Fatalf("normalized identifier = %q, %v", got, err)
	}
	for _, value := range []string{"", "bad\nvalue", strings.Repeat("x", MaxIdentifierRunes+1), string([]byte{0xff})} {
		if _, err := normalizeIdentifier(value, MaxIdentifierRunes, true); !errors.Is(err, ErrInvalid) {
			t.Errorf("normalizeIdentifier(%q) error = %v", value, err)
		}
	}
}

func TestSameCreateUsesImmutableCreationFingerprint(t *testing.T) {
	t.Parallel()
	base := &Document{
		Title: "제목", Body: "본문", TeamID: "team-1", ChannelID: "channel-1",
		CreatedBy: "user-1", SourceThreadID: "thread-1", SourceCursorAt: 100,
	}
	base.CreateFingerprint = createFingerprint(base)
	clone := *base
	if !sameCreate(base, &clone) {
		t.Fatal("identical create should replay")
	}
	for name, mutate := range map[string]func(*Document){
		"title":   func(document *Document) { document.Title = "다름" },
		"body":    func(document *Document) { document.Body = "다름" },
		"creator": func(document *Document) { document.CreatedBy = "user-2" },
		"team":    func(document *Document) { document.TeamID = "team-2" },
		"channel": func(document *Document) { document.ChannelID = "channel-2" },
		"thread":  func(document *Document) { document.SourceThreadID = "thread-2" },
		"cursor":  func(document *Document) { document.SourceCursorAt = 101 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *base
			mutate(&changed)
			changed.CreateFingerprint = createFingerprint(&changed)
			if sameCreate(base, &changed) {
				t.Fatal("different create input replayed")
			}
		})
	}
	patched := *base
	patched.Title = "생성 후 수정한 제목"
	patched.Body = "생성 후 수정한 본문"
	if !sameCreate(&patched, base) {
		t.Fatal("mutable fields must not change replay identity")
	}
}

func TestDocumentJSONDoesNotExposeIdempotencyKey(t *testing.T) {
	t.Parallel()
	document := Document{ID: "document-1", IdempotencyKey: "private-replay-key", CreateFingerprint: strings.Repeat("a", 64)}
	raw := strings.TrimSpace(string(mustJSON(t, document)))
	if strings.Contains(raw, "idempotency") || strings.Contains(raw, "private-replay-key") || strings.Contains(raw, strings.Repeat("a", 64)) {
		t.Fatalf("document JSON leaked replay metadata: %s", raw)
	}
}

func TestSourceRevisionChangesForContentAndMembership(t *testing.T) {
	t.Parallel()
	base := []SourcePost{{ID: "root", ChannelID: "channel-1", RootID: "", Message: "원문", CreateAt: 1, UpdateAt: 100}}
	revision := sourceRevision(base)
	if revision <= 0 || revision != sourceRevision(base) {
		t.Fatalf("source revision is not stable and positive: %d", revision)
	}
	changedMessage := append([]SourcePost(nil), base...)
	changedMessage[0].Message = "같은 millisecond의 수정"
	if got := sourceRevision(changedMessage); got == revision {
		t.Fatalf("same-ms content edit retained revision %d", got)
	}
	metadataOnly := append([]SourcePost(nil), base...)
	metadataOnly[0].UpdateAt++
	if got := sourceRevision(metadataOnly); got != revision {
		t.Fatalf("metadata-only timestamp changed revision: got %d, want %d", got, revision)
	}
	moved := append([]SourcePost(nil), base...)
	moved[0].ChannelID = "channel-2"
	if got := sourceRevision(moved); got == revision {
		t.Fatalf("channel move retained revision %d", got)
	}
	withOlderReply := append(append([]SourcePost(nil), base...), SourcePost{
		ID: "reply", RootID: "root", Message: "뒤늦게 적재된 답글", CreateAt: 2, UpdateAt: 2,
	})
	if got := sourceRevision(withOlderReply); got == revision {
		t.Fatalf("reply below the prior max timestamp retained revision %d", got)
	}
	if got := sourceRevision(withOlderReply[1:]); got == sourceRevision(withOlderReply) {
		t.Fatalf("source deletion retained revision %d", got)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
