package inboxprefs

import (
	"errors"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/activityevents"
)

func TestNormalizeRejectsUnboundedAndUnknownValues(t *testing.T) {
	t.Parallel()
	valid := Defaults()
	valid.VIPUserIDs = []string{" user-b ", "user-a", "user-a"}
	valid.PriorityEventTypes = []activityevents.EventType{activityevents.TypeMention, activityevents.TypeMention}
	valid.SnoozePresetsMinutes = []int{1440, 60, 60}
	valid.WorkHoursTimezone = "Asia/Seoul"
	if err := Normalize(&valid); err != nil {
		t.Fatalf("normalize valid preferences: %v", err)
	}
	if len(valid.VIPUserIDs) != 2 || valid.VIPUserIDs[0] != "user-a" || len(valid.SnoozePresetsMinutes) != 2 {
		t.Fatalf("normalized preferences = %#v", valid)
	}

	for _, mutate := range []func(*Preferences){
		func(p *Preferences) { p.BundleBy = "arbitrary" },
		func(p *Preferences) { p.PriorityEventTypes = []activityevents.EventType{"secret_rotated"} },
		func(p *Preferences) { p.SnoozePresetsMinutes = []int{1} },
		func(p *Preferences) { p.WorkHoursTimezone = "Not/A_Zone" },
		func(p *Preferences) { p.WorkHoursWeekdays = []int16{0} },
		func(p *Preferences) { p.WorkHoursEndMinute = p.WorkHoursStartMinute },
	} {
		input := Defaults()
		mutate(&input)
		if err := Normalize(&input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid preferences error = %v", err)
		}
	}
}

func TestNotificationsAllowedHandlesQuietHoursAndOvernightWeekdays(t *testing.T) {
	t.Parallel()
	location, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	prefs := Defaults()
	prefs.WorkHoursEnabled = true
	prefs.WorkHoursTimezone = "Asia/Seoul"
	prefs.WorkHoursWeekdays = []int16{1}
	prefs.WorkHoursStartMinute = 22 * 60
	prefs.WorkHoursEndMinute = 6 * 60

	mondayLate := time.Date(2026, 8, 31, 23, 0, 0, 0, location)
	tuesdayEarly := time.Date(2026, 9, 1, 2, 0, 0, 0, location)
	tuesdayLate := time.Date(2026, 9, 1, 23, 0, 0, 0, location)
	if !NotificationsAllowed(prefs, mondayLate, false) || !NotificationsAllowed(prefs, tuesdayEarly, false) {
		t.Fatal("overnight Monday window did not include both sides of midnight")
	}
	if NotificationsAllowed(prefs, tuesdayLate, false) {
		t.Fatal("Tuesday-only portion was incorrectly treated as Monday work hours")
	}
	if !NotificationsAllowed(prefs, tuesdayLate, true) {
		t.Fatal("priority override did not bypass quiet hours")
	}
	prefs.PriorityOverride = false
	if NotificationsAllowed(prefs, tuesdayLate, true) {
		t.Fatal("disabled priority override bypassed quiet hours")
	}
}

func TestIsPriorityUsesVIPAndEventType(t *testing.T) {
	t.Parallel()
	prefs := Defaults()
	prefs.VIPUserIDs = []string{"leader"}
	if !IsPriority(prefs, "leader", activityevents.TypePluginEvent) {
		t.Fatal("VIP actor was not prioritized")
	}
	if !IsPriority(prefs, "other", activityevents.TypeMention) {
		t.Fatal("priority event type was not prioritized")
	}
	if IsPriority(prefs, "other", activityevents.TypePluginEvent) {
		t.Fatal("ordinary event was prioritized")
	}
}
