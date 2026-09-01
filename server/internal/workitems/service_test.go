package workitems

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkItemKindAndStatusMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind, status string
		want         bool
	}{
		{KindTask, StatusOpen, true},
		{KindTask, StatusInProgress, true},
		{KindTask, StatusDone, true},
		{KindTask, StatusCancelled, true},
		{KindTask, StatusRecorded, false},
		{KindDecision, StatusProposed, true},
		{KindDecision, StatusUnderReview, true},
		{KindDecision, StatusRecorded, true},
		{KindDecision, StatusSuperseded, true},
		{KindDecision, StatusCancelled, true},
		{KindDecision, StatusDone, false},
		{"unknown", StatusOpen, false},
	}
	for _, test := range tests {
		if got := validStatus(test.kind, test.status); got != test.want {
			t.Errorf("validStatus(%q, %q) = %v, want %v", test.kind, test.status, got, test.want)
		}
	}
}

func TestWorkItemTransitionsRequireAnAtomicReplacement(t *testing.T) {
	t.Parallel()
	if !validTransition(KindDecision, StatusProposed, StatusUnderReview) ||
		!validTransition(KindDecision, StatusUnderReview, StatusRecorded) ||
		!validTransition(KindDecision, StatusRecorded, StatusCancelled) {
		t.Fatal("expected forward decision lifecycle transitions")
	}
	if validTransition(KindDecision, StatusRecorded, StatusSuperseded) {
		t.Fatal("a status patch must not supersede a decision without a replacement")
	}
	if validTransition(KindDecision, StatusSuperseded, StatusRecorded) {
		t.Fatal("a superseded decision with a successor must not be reactivated")
	}
}

func TestNextDueAtSkipsPastOccurrences(t *testing.T) {
	t.Parallel()
	current := time.Date(2026, time.January, 1, 9, 0, 0, 0, time.UTC).UnixMilli()
	now := time.Date(2026, time.January, 4, 10, 0, 0, 0, time.UTC).UnixMilli()
	want := time.Date(2026, time.January, 5, 9, 0, 0, 0, time.UTC).UnixMilli()
	if got := nextDueAt(current, RecurrenceDaily, 1, now); got != want {
		t.Fatalf("next daily occurrence = %d, want %d", got, want)
	}
	if got := nextDueAt(current, RecurrenceNone, 0, now); got != 0 {
		t.Fatalf("non-recurring next occurrence = %d, want 0", got)
	}
	monthEnd := time.Date(2025, time.January, 31, 9, 0, 0, 0, time.UTC).UnixMilli()
	wantMonthEnd := time.Date(2025, time.February, 28, 9, 0, 0, 0, time.UTC).UnixMilli()
	if got := nextDueAt(monthEnd, RecurrenceMonthly, 1, monthEnd-1); got != wantMonthEnd {
		t.Fatalf("clamped monthly occurrence = %d, want %d", got, wantMonthEnd)
	}
}

func TestWorkItemPriorityAndRecurrenceValidation(t *testing.T) {
	t.Parallel()
	for _, priority := range []string{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent} {
		if !validPriority(priority) {
			t.Fatalf("priority %q should be valid", priority)
		}
	}
	if validPriority("critical") || !validRecurrence(RecurrenceWeekly, 2) ||
		validRecurrence(RecurrenceNone, 1) || validRecurrence(RecurrenceDaily, 0) {
		t.Fatal("priority or recurrence validation accepted an invalid value")
	}
}

func TestWorkItemCursorRoundTripAndRejectsMalformedValues(t *testing.T) {
	t.Parallel()
	item := Item{ID: "work-1", CreateAt: 1234}
	encoded := encodeCursor(item)
	decoded, err := decodeCursor(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if decoded.ID != item.ID || decoded.CreateAt != item.CreateAt {
		t.Fatalf("decoded cursor = %#v", decoded)
	}
	unknownField := base64.RawURLEncoding.EncodeToString([]byte(`{"create_at":1,"id":"event","secret":"value"}`))
	trailingJSON := base64.RawURLEncoding.EncodeToString([]byte(`{"create_at":1,"id":"event"}{}`))
	oversizedID := base64.RawURLEncoding.EncodeToString([]byte(`{"create_at":1,"id":"` + strings.Repeat("x", maxIDRunes+1) + `"}`))
	for _, invalid := range []string{
		"%%", "e30", unknownField, trailingJSON, oversizedID,
		encodeCursor(Item{ID: "", CreateAt: 1}), encodeCursor(Item{ID: "x", CreateAt: 0}),
	} {
		if _, err := decodeCursor(invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("decodeCursor(%q) error = %v, want ErrInvalid", invalid, err)
		}
	}
}

func TestSameCreateRequestChecksEveryDurableInput(t *testing.T) {
	t.Parallel()
	base := &Item{
		Kind: KindTask, Title: "제목", Description: "본문", AssigneeID: "user-1",
		TeamID: "team-1", ChannelID: "channel-1", SourcePostID: "post-1",
		SourceThreadID: "root-1", DueAt: 123, Status: StatusOpen,
	}
	clone := *base
	if !sameCreateRequest(base, &clone) {
		t.Fatal("identical create request should replay")
	}
	for name, mutate := range map[string]func(*Item){
		"kind":        func(item *Item) { item.Kind = KindDecision },
		"status":      func(item *Item) { item.Status = StatusDone },
		"title":       func(item *Item) { item.Title = "다른 제목" },
		"description": func(item *Item) { item.Description = "다른 본문" },
		"assignee":    func(item *Item) { item.AssigneeID = "user-2" },
		"team":        func(item *Item) { item.TeamID = "team-2" },
		"channel":     func(item *Item) { item.ChannelID = "channel-2" },
		"source":      func(item *Item) { item.SourcePostID = "post-2" },
		"thread":      func(item *Item) { item.SourceThreadID = "root-2" },
		"due":         func(item *Item) { item.DueAt = 456 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *base
			mutate(&changed)
			if sameCreateRequest(base, &changed) {
				t.Fatal("different create request must conflict")
			}
		})
	}
}

func TestCreateFingerprintIsImmutableAndIncludesInitialStatus(t *testing.T) {
	t.Parallel()
	original := &Item{
		Kind: KindDecision, Status: StatusProposed, Title: "검토 제안", CreatedBy: "user-1",
		TeamID: "team-1", ChannelID: "channel-1", SourcePostID: "post-1",
		Priority: PriorityNormal, RecurrenceUnit: RecurrenceNone,
		ImpactTaskIDs: []string{"task-b", "task-a"},
	}
	original.CreateFingerprint = createRequestFingerprint(original)
	mutated := *original
	mutated.Title = "나중에 수정한 제목"
	mutated.Status = StatusRecorded
	if !sameCreateRequest(original, &mutated) {
		t.Fatal("mutable fields must not invalidate a stored create fingerprint")
	}
	differentRequest := *original
	differentRequest.Status = StatusRecorded
	differentRequest.CreateFingerprint = createRequestFingerprint(&differentRequest)
	if sameCreateRequest(original, &differentRequest) {
		t.Fatal("different initial decision status reused an idempotency fingerprint")
	}
	reordered := *original
	reordered.ImpactTaskIDs = []string{"task-a", "task-b"}
	if createRequestFingerprint(&reordered) != original.CreateFingerprint {
		t.Fatal("relation input order should not change the create fingerprint")
	}
}

func TestWorkItemJSONOmitsIdempotencyKey(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(Item{
		ID: "work-1", IdempotencyKey: "private-replay-key", CreateFingerprint: "private-fingerprint",
		PreviousAssigneeID: "former-private-assignee", AssigneeChanged: true,
	})
	if err != nil {
		t.Fatalf("marshal work item: %v", err)
	}
	if strings.Contains(string(raw), "idempotency") || strings.Contains(string(raw), "private-replay-key") ||
		strings.Contains(string(raw), "previous_assignee") || strings.Contains(string(raw), "former-private-assignee") ||
		strings.Contains(string(raw), "assignee_changed") || strings.Contains(string(raw), "private-fingerprint") {
		t.Fatalf("work item JSON leaked request-control metadata: %s", raw)
	}
}

func TestWorkItemTextValidationUsesRuneLimits(t *testing.T) {
	t.Parallel()
	if got, err := normalizeText("  한글 작업  ", maxTitleRunes); err != nil || got != "한글 작업" {
		t.Fatalf("normalize title = %q, %v", got, err)
	}
	if _, err := normalizeText(strings.Repeat("가", maxTitleRunes+1), maxTitleRunes); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized title error = %v", err)
	}
	if got, err := normalizeOptionalText("  ", maxBodyRunes); err != nil || got != "" {
		t.Fatalf("normalize optional body = %q, %v", got, err)
	}
	if got, err := normalizeOptionalText("첫 줄\n\t둘째 줄", maxBodyRunes); err != nil || got != "첫 줄\n\t둘째 줄" {
		t.Fatalf("normalize multiline body = %q, %v", got, err)
	}
	for _, invalid := range []string{"제목\n둘째 줄", "널\x00문자", "제어\x01문자"} {
		if _, err := normalizeText(invalid, maxTitleRunes); !errors.Is(err, ErrInvalid) {
			t.Fatalf("control-character title %q error = %v", invalid, err)
		}
	}
	if _, err := normalizeOptionalText("널\x00문자", maxBodyRunes); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NUL description error = %v", err)
	}
}

func TestWorkItemIdentifierValidationRequiresBoundedUTF8(t *testing.T) {
	t.Parallel()
	if got, err := normalizeIdentifier(" user-1 ", maxIDRunes, true); err != nil || got != "user-1" {
		t.Fatalf("normalize identifier = %q, %v", got, err)
	}
	for _, invalid := range []string{"", strings.Repeat("x", maxIDRunes+1), string([]byte{0xff})} {
		if _, err := normalizeIdentifier(invalid, maxIDRunes, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("identifier %q error = %v", invalid, err)
		}
	}
}

func TestWorkItemPublicInputsFailBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	service := &Service{}
	if _, _, err := service.Create(t.Context(), "user-1", CreateInput{
		Kind: KindTask, Title: "작업", SourcePostID: "post-1",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing idempotency key error = %v", err)
	}
	if _, err := service.ListForUser(t.Context(), "user-1", ListOptions{PageSize: -1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative page size error = %v", err)
	}
	status := StatusDone
	if _, err := service.Patch(t.Context(), "user-1", strings.Repeat("x", maxIDRunes+1), PatchInput{Status: &status}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized item id error = %v", err)
	}
}
