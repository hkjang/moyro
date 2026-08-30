package activityevents

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseEventTypeUsesClosedAllowlist(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"mention", "thread_reply", "direct_message", "approval_requested",
		"decided", "reminder_fired", "task_assigned", "system_warning", "plugin_event",
	} {
		if typ, err := ParseEventType(raw); err != nil || string(typ) != raw {
			t.Fatalf("ParseEventType(%q) = %q, %v", raw, typ, err)
		}
	}
	if _, err := ParseEventType("credential_rotated"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported event type error = %v", err)
	}
}

func TestActivityCursorRoundTripsAndRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	raw, err := encodeCursor(Event{ID: "event-1", CreateAt: 1234})
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	cursor, present, err := decodeCursor(raw)
	if err != nil || !present || cursor.ID != "event-1" || cursor.CreateAt != 1234 {
		t.Fatalf("decoded cursor = %#v, present=%v, err=%v", cursor, present, err)
	}
	unknownField := base64.RawURLEncoding.EncodeToString([]byte(`{"create_at":1,"id":"event","secret":"value"}`))
	trailingJSON := base64.RawURLEncoding.EncodeToString([]byte(`{"create_at":1,"id":"event"}{}`))
	for _, invalid := range []string{"%%%", unknownField, trailingJSON} {
		if _, _, err := decodeCursor(invalid); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("cursor %q error = %v, want ErrInvalidCursor", invalid, err)
		}
	}
}

func TestEventJSONCannotExposeOwnerOrDedupeKey(t *testing.T) {
	t.Parallel()
	event := Event{
		ID: "event-1", UserID: "owner-private", Type: TypeMention,
		Title: "Mention", DedupeKey: "dedupe-private", CreateAt: 1, UpdateAt: 1,
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	encoded := string(raw)
	for _, forbidden := range []string{"owner-private", "dedupe-private", "user_id", "dedupe_key"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("event JSON contains %q: %s", forbidden, encoded)
		}
	}
}

func TestNormalizeEmitInputValidatesRequiredAndBoundedFields(t *testing.T) {
	t.Parallel()
	valid := EmitInput{
		UserID: " user-1 ", Type: EventType("MENTION"), DedupeKey: " post-1:user-1 ", Title: " 새 멘션 ",
	}
	if err := normalizeEmitInput(&valid); err != nil {
		t.Fatalf("normalize valid input: %v", err)
	}
	if valid.UserID != "user-1" || valid.Type != TypeMention || valid.Title != "새 멘션" {
		t.Fatalf("normalized input = %#v", valid)
	}
	invalid := valid
	invalid.Summary = strings.Repeat("가", 4097)
	if err := normalizeEmitInput(&invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize summary error = %v", err)
	}
}
