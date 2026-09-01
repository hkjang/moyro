// Package inboxprefs stores and validates user-owned notification and inbox
// presentation rules.  Delivery adapters and browser clients consume the
// same normalized document so quiet hours cannot drift between surfaces.
package inboxprefs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	BundleNone    = "none"
	BundleChannel = "channel"
	BundleType    = "type"

	inboxPreferenceLockSeed int64 = 674116013
)

var ErrInvalid = errors.New("inbox preferences are invalid")

type Preferences struct {
	VIPUserIDs           []string                   `json:"vip_user_ids"`
	PriorityEventTypes   []activityevents.EventType `json:"priority_event_types"`
	BundleBy             string                     `json:"bundle_by"`
	SnoozePresetsMinutes []int                      `json:"snooze_presets_minutes"`
	WorkHoursEnabled     bool                       `json:"work_hours_enabled"`
	WorkHoursTimezone    string                     `json:"work_hours_timezone"`
	WorkHoursWeekdays    []int16                    `json:"work_hours_weekdays"`
	WorkHoursStartMinute int                        `json:"work_hours_start_minute"`
	WorkHoursEndMinute   int                        `json:"work_hours_end_minute"`
	PriorityOverride     bool                       `json:"priority_override"`
	UpdateAt             int64                      `json:"update_at"`
}

// Patch contains only fields explicitly supplied by a partial-update caller.
// Pointer-to-slice fields distinguish an omitted value from an explicit empty
// list, which is part of the HTTP PATCH contract.
type Patch struct {
	VIPUserIDs           *[]string
	PriorityEventTypes   *[]activityevents.EventType
	BundleBy             *string
	SnoozePresetsMinutes *[]int
	WorkHoursEnabled     *bool
	WorkHoursTimezone    *string
	WorkHoursWeekdays    *[]int16
	WorkHoursStartMinute *int
	WorkHoursEndMinute   *int
	PriorityOverride     *bool
}

func (p Patch) Empty() bool {
	return p.VIPUserIDs == nil && p.PriorityEventTypes == nil && p.BundleBy == nil &&
		p.SnoozePresetsMinutes == nil && p.WorkHoursEnabled == nil &&
		p.WorkHoursTimezone == nil && p.WorkHoursWeekdays == nil &&
		p.WorkHoursStartMinute == nil && p.WorkHoursEndMinute == nil &&
		p.PriorityOverride == nil
}

func (p Patch) apply(current *Preferences) {
	if p.VIPUserIDs != nil {
		current.VIPUserIDs = *p.VIPUserIDs
	}
	if p.PriorityEventTypes != nil {
		current.PriorityEventTypes = *p.PriorityEventTypes
	}
	if p.BundleBy != nil {
		current.BundleBy = *p.BundleBy
	}
	if p.SnoozePresetsMinutes != nil {
		current.SnoozePresetsMinutes = *p.SnoozePresetsMinutes
	}
	if p.WorkHoursEnabled != nil {
		current.WorkHoursEnabled = *p.WorkHoursEnabled
	}
	if p.WorkHoursTimezone != nil {
		current.WorkHoursTimezone = *p.WorkHoursTimezone
	}
	if p.WorkHoursWeekdays != nil {
		current.WorkHoursWeekdays = *p.WorkHoursWeekdays
	}
	if p.WorkHoursStartMinute != nil {
		current.WorkHoursStartMinute = *p.WorkHoursStartMinute
	}
	if p.WorkHoursEndMinute != nil {
		current.WorkHoursEndMinute = *p.WorkHoursEndMinute
	}
	if p.PriorityOverride != nil {
		current.PriorityOverride = *p.PriorityOverride
	}
}

func Defaults() Preferences {
	return Preferences{
		VIPUserIDs: []string{},
		PriorityEventTypes: []activityevents.EventType{
			activityevents.TypeMention,
			activityevents.TypeDirectMessage,
			activityevents.TypeApprovalRequested,
			activityevents.TypeSystemWarning,
		},
		BundleBy:             BundleChannel,
		SnoozePresetsMinutes: []int{60, 240, 1440},
		WorkHoursTimezone:    "UTC",
		WorkHoursWeekdays:    []int16{1, 2, 3, 4, 5},
		WorkHoursStartMinute: 9 * 60,
		WorkHoursEndMinute:   18 * 60,
		PriorityOverride:     true,
	}
}

type Service struct {
	db    *store.DB
	nowMS func() int64
}

func New(db *store.DB) *Service {
	return &Service{db: db, nowMS: func() int64 { return time.Now().UnixMilli() }}
}

type preferenceQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type preferenceExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func getPreferences(ctx context.Context, queryer preferenceQueryer, userID string) (Preferences, error) {
	var out Preferences
	var eventTypes []string
	err := queryer.QueryRow(ctx, `
		SELECT vip_user_ids, priority_event_types, bundle_by,
		       snooze_presets_minutes, work_hours_enabled,
		       work_hours_timezone, work_hours_weekdays,
		       work_hours_start_minute, work_hours_end_minute,
		       priority_override, update_at
		FROM user_inbox_preferences WHERE user_id=$1
	`, userID).Scan(
		&out.VIPUserIDs, &eventTypes, &out.BundleBy,
		&out.SnoozePresetsMinutes, &out.WorkHoursEnabled,
		&out.WorkHoursTimezone, &out.WorkHoursWeekdays,
		&out.WorkHoursStartMinute, &out.WorkHoursEndMinute,
		&out.PriorityOverride, &out.UpdateAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Defaults(), nil
	}
	if err != nil {
		return Preferences{}, err
	}
	out.PriorityEventTypes = make([]activityevents.EventType, 0, len(eventTypes))
	for _, value := range eventTypes {
		out.PriorityEventTypes = append(out.PriorityEventTypes, activityevents.EventType(value))
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, userID string) (Preferences, error) {
	if err := validateID(userID); err != nil {
		return Preferences{}, err
	}
	return getPreferences(ctx, s.db.Pool, strings.TrimSpace(userID))
}

func validateVIPUsers(ctx context.Context, queryer preferenceQueryer, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	var count int
	if err := queryer.QueryRow(ctx, `
		SELECT count(*) FROM users WHERE id=ANY($1::text[]) AND delete_at=0
	`, userIDs).Scan(&count); err != nil {
		return err
	}
	if count != len(userIDs) {
		return fmt.Errorf("%w: vip_user_ids contains an unavailable user", ErrInvalid)
	}
	return nil
}

func savePreferences(ctx context.Context, executor preferenceExecutor, userID string, input Preferences) error {
	eventTypes := make([]string, 0, len(input.PriorityEventTypes))
	for _, value := range input.PriorityEventTypes {
		eventTypes = append(eventTypes, string(value))
	}
	_, err := executor.Exec(ctx, `
		INSERT INTO user_inbox_preferences (
			user_id, vip_user_ids, priority_event_types, bundle_by,
			snooze_presets_minutes, work_hours_enabled, work_hours_timezone,
			work_hours_weekdays, work_hours_start_minute, work_hours_end_minute,
			priority_override, update_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (user_id) DO UPDATE SET
			vip_user_ids=EXCLUDED.vip_user_ids,
			priority_event_types=EXCLUDED.priority_event_types,
			bundle_by=EXCLUDED.bundle_by,
			snooze_presets_minutes=EXCLUDED.snooze_presets_minutes,
			work_hours_enabled=EXCLUDED.work_hours_enabled,
			work_hours_timezone=EXCLUDED.work_hours_timezone,
			work_hours_weekdays=EXCLUDED.work_hours_weekdays,
			work_hours_start_minute=EXCLUDED.work_hours_start_minute,
			work_hours_end_minute=EXCLUDED.work_hours_end_minute,
			priority_override=EXCLUDED.priority_override,
			update_at=EXCLUDED.update_at
	`, userID, input.VIPUserIDs, eventTypes, input.BundleBy,
		input.SnoozePresetsMinutes, input.WorkHoursEnabled, input.WorkHoursTimezone,
		input.WorkHoursWeekdays, input.WorkHoursStartMinute, input.WorkHoursEndMinute,
		input.PriorityOverride, input.UpdateAt)
	return err
}

func (s *Service) Put(ctx context.Context, userID string, input Preferences) (Preferences, error) {
	userID = strings.TrimSpace(userID)
	if err := validateID(userID); err != nil {
		return Preferences{}, err
	}
	if err := Normalize(&input); err != nil {
		return Preferences{}, err
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return Preferences{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, userID, inboxPreferenceLockSeed); err != nil {
		return Preferences{}, err
	}
	if err := validateVIPUsers(ctx, tx, input.VIPUserIDs); err != nil {
		return Preferences{}, err
	}
	input.UpdateAt = s.nowMS()
	if err := savePreferences(ctx, tx, userID, input); err != nil {
		return Preferences{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Preferences{}, err
	}
	return input, nil
}

// Patch atomically applies a partial update. The per-user transaction advisory
// lock is acquired before reading, so concurrent patches cannot both derive
// replacements from the same old document. Unlike a row lock, it also covers
// the first update when no preference row exists yet.
func (s *Service) Patch(ctx context.Context, userID string, patch Patch) (Preferences, error) {
	userID = strings.TrimSpace(userID)
	if err := validateID(userID); err != nil {
		return Preferences{}, err
	}
	if patch.Empty() {
		return Preferences{}, fmt.Errorf("%w: patch is empty", ErrInvalid)
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return Preferences{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, $2))`, userID, inboxPreferenceLockSeed); err != nil {
		return Preferences{}, err
	}
	current, err := getPreferences(ctx, tx, userID)
	if err != nil {
		return Preferences{}, err
	}
	patch.apply(&current)
	if err := Normalize(&current); err != nil {
		return Preferences{}, err
	}
	if err := validateVIPUsers(ctx, tx, current.VIPUserIDs); err != nil {
		return Preferences{}, err
	}
	current.UpdateAt = s.nowMS()
	if err := savePreferences(ctx, tx, userID, current); err != nil {
		return Preferences{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Preferences{}, err
	}
	return current, nil
}

// Normalize applies canonical ordering and all bounds before persistence.
func Normalize(input *Preferences) error {
	if input == nil {
		return fmt.Errorf("%w: document is required", ErrInvalid)
	}
	var err error
	input.VIPUserIDs, err = normalizeStrings(input.VIPUserIDs, 200, 128)
	if err != nil {
		return fmt.Errorf("%w: vip_user_ids: %v", ErrInvalid, err)
	}
	if len(input.PriorityEventTypes) > 16 {
		return fmt.Errorf("%w: too many priority event types", ErrInvalid)
	}
	typeSet := make(map[activityevents.EventType]struct{}, len(input.PriorityEventTypes))
	types := make([]activityevents.EventType, 0, len(input.PriorityEventTypes))
	for _, raw := range input.PriorityEventTypes {
		typ, parseErr := activityevents.ParseEventType(string(raw))
		if parseErr != nil {
			return fmt.Errorf("%w: priority_event_types: %v", ErrInvalid, parseErr)
		}
		if _, exists := typeSet[typ]; exists {
			continue
		}
		typeSet[typ] = struct{}{}
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	input.PriorityEventTypes = types

	switch input.BundleBy {
	case BundleNone, BundleChannel, BundleType:
	default:
		return fmt.Errorf("%w: bundle_by must be none, channel, or type", ErrInvalid)
	}
	if len(input.SnoozePresetsMinutes) < 1 || len(input.SnoozePresetsMinutes) > 8 {
		return fmt.Errorf("%w: snooze presets must contain 1-8 values", ErrInvalid)
	}
	presetSet := map[int]struct{}{}
	presets := make([]int, 0, len(input.SnoozePresetsMinutes))
	for _, minutes := range input.SnoozePresetsMinutes {
		if minutes < 5 || minutes > 43_200 {
			return fmt.Errorf("%w: snooze preset must be between 5 and 43200 minutes", ErrInvalid)
		}
		if _, exists := presetSet[minutes]; exists {
			continue
		}
		presetSet[minutes] = struct{}{}
		presets = append(presets, minutes)
	}
	sort.Ints(presets)
	input.SnoozePresetsMinutes = presets

	input.WorkHoursTimezone = strings.TrimSpace(input.WorkHoursTimezone)
	if input.WorkHoursTimezone == "" || len(input.WorkHoursTimezone) > 128 || strings.ContainsAny(input.WorkHoursTimezone, "\r\n\x00") {
		return fmt.Errorf("%w: invalid work-hours timezone", ErrInvalid)
	}
	if _, loadErr := time.LoadLocation(input.WorkHoursTimezone); loadErr != nil {
		return fmt.Errorf("%w: unknown work-hours timezone", ErrInvalid)
	}
	if input.WorkHoursStartMinute < 0 || input.WorkHoursStartMinute > 1439 ||
		input.WorkHoursEndMinute < 0 || input.WorkHoursEndMinute > 1439 ||
		input.WorkHoursStartMinute == input.WorkHoursEndMinute {
		return fmt.Errorf("%w: work-hours start and end must be distinct valid minutes", ErrInvalid)
	}
	if len(input.WorkHoursWeekdays) < 1 || len(input.WorkHoursWeekdays) > 7 {
		return fmt.Errorf("%w: work_hours_weekdays must contain 1-7 values", ErrInvalid)
	}
	weekdaySet := map[int16]struct{}{}
	weekdays := make([]int16, 0, len(input.WorkHoursWeekdays))
	for _, weekday := range input.WorkHoursWeekdays {
		if weekday < 1 || weekday > 7 {
			return fmt.Errorf("%w: work-hours weekday must be between 1 and 7", ErrInvalid)
		}
		if _, exists := weekdaySet[weekday]; exists {
			continue
		}
		weekdaySet[weekday] = struct{}{}
		weekdays = append(weekdays, weekday)
	}
	sort.Slice(weekdays, func(i, j int) bool { return weekdays[i] < weekdays[j] })
	input.WorkHoursWeekdays = weekdays
	return nil
}

func IsPriority(p Preferences, actorID string, eventType activityevents.EventType) bool {
	actorID = strings.TrimSpace(actorID)
	for _, id := range p.VIPUserIDs {
		if actorID != "" && id == actorID {
			return true
		}
	}
	for _, typ := range p.PriorityEventTypes {
		if typ == eventType {
			return true
		}
	}
	return false
}

// NotificationsAllowed reports whether an interruptive notification should
// be emitted at now.  Overnight windows associate the after-midnight segment
// with the weekday on which the shift began.
func NotificationsAllowed(p Preferences, now time.Time, priority bool) bool {
	if !p.WorkHoursEnabled || (priority && p.PriorityOverride) {
		return true
	}
	location, err := time.LoadLocation(p.WorkHoursTimezone)
	if err != nil {
		return false
	}
	local := now.In(location)
	minute := local.Hour()*60 + local.Minute()
	today := isoWeekday(local.Weekday())
	if p.WorkHoursStartMinute < p.WorkHoursEndMinute {
		return includesWeekday(p.WorkHoursWeekdays, today) &&
			minute >= p.WorkHoursStartMinute && minute < p.WorkHoursEndMinute
	}
	if minute >= p.WorkHoursStartMinute {
		return includesWeekday(p.WorkHoursWeekdays, today)
	}
	previous := today - 1
	if previous == 0 {
		previous = 7
	}
	return minute < p.WorkHoursEndMinute && includesWeekday(p.WorkHoursWeekdays, previous)
}

func validateID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%w: invalid user id", ErrInvalid)
	}
	return nil
}

func normalizeStrings(values []string, maxItems, maxLength int) ([]string, error) {
	if len(values) > maxItems {
		return nil, errors.New("too many values")
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maxLength || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("value is empty or too long")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func isoWeekday(value time.Weekday) int16 {
	if value == time.Sunday {
		return 7
	}
	return int16(value)
}

func includesWeekday(values []int16, target int16) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
