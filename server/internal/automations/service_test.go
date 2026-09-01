package automations

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/workitems"
)

func TestRuleMatchingIsTrimmedAndCaseInsensitive(t *testing.T) {
	t.Parallel()
	if !ruleMatches(MatchContains, " 장애 ", "운영 장애입니다") {
		t.Fatal("contains rule should match a normalized message")
	}
	if !ruleMatches(MatchStartsWith, "TODO:", "  todo: 배포 점검") {
		t.Fatal("starts-with rule should ignore surrounding space and case")
	}
	if ruleMatches(MatchStartsWith, "todo:", "내일 todo: 확인") || ruleMatches("regex", "todo", "todo") {
		t.Fatal("rule matched the wrong operator")
	}
}

func TestNormalizeActionRejectsInvalidDurableConfig(t *testing.T) {
	t.Parallel()
	valid := Action{Type: ActionTask, Config: ActionConfig{
		DueOffsetMinutes: 60, Priority: workitems.PriorityHigh,
		RecurrenceUnit: workitems.RecurrenceWeekly, RecurrenceInterval: 2,
	}}
	if err := normalizeAction(&valid); err != nil {
		t.Fatalf("valid task action: %v", err)
	}
	for name, action := range map[string]Action{
		"priority": {Type: ActionTask, Config: ActionConfig{Priority: "critical"}},
		"unit": {Type: ActionTask, Config: ActionConfig{
			Priority: workitems.PriorityNormal, RecurrenceUnit: "yearly", RecurrenceInterval: 1, DueOffsetMinutes: 1,
		}},
		"interval": {Type: ActionTask, Config: ActionConfig{
			Priority: workitems.PriorityNormal, RecurrenceUnit: workitems.RecurrenceDaily, DueOffsetMinutes: 1,
		}},
		"decision priority": {Type: ActionDecision, Config: ActionConfig{Priority: workitems.PriorityNormal}},
		"reminder priority": {Type: ActionReminder, Config: ActionConfig{RemindOffsetMinutes: 1, Priority: workitems.PriorityNormal}},
		"control text":      {Type: ActionTask, Config: ActionConfig{Title: "bad\x00title"}},
	} {
		action := action
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := normalizeAction(&action); !errors.Is(err, ErrInvalid) {
				t.Fatalf("normalizeAction error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestNormalizeSaveInputRejectsDuplicateActionIDs(t *testing.T) {
	t.Parallel()
	_, err := normalizeSaveInput(SaveInput{
		Name: "tasks", ChannelID: "channel", MatchType: MatchContains, MatchValue: "todo",
		Actions: []Action{
			{ID: "same", Type: ActionReminder, Config: ActionConfig{RemindOffsetMinutes: 1}},
			{ID: "same", Type: ActionReminder, Config: ActionConfig{RemindOffsetMinutes: 2}},
		},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate action id error = %v", err)
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	t.Parallel()
	if got := retryDelay(1); got != 30*time.Second {
		t.Fatalf("first retry = %v", got)
	}
	if got := retryDelay(100); got != 15*time.Minute {
		t.Fatalf("maximum retry = %v", got)
	}
	if got := truncateError(strings.Repeat("가", 1001)); len([]rune(got)) != 1000 {
		t.Fatalf("truncated error runes = %d", len([]rune(got)))
	}
}
