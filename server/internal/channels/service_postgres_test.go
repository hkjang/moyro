package channels

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const channelsTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

// TestBumpUnreadRespectsAuthorMentionsAndMuting pins the notification-counter
// contract every sidebar badge depends on: the author never accrues unread
// state, a mention always increments both counters, and `mark_unread=mention`
// suppresses the message counter without suppressing the mention counter.
func TestBumpUnreadRespectsAuthorMentionsAndMuting(t *testing.T) {
	db := newChannelsTestDB(t)
	ctx := channelsTestContext(t)
	seedChannelsFixture(t, ctx, db)
	service := New(db)

	setNotifyProps(t, ctx, db, "channel-general", "user-muted", map[string]any{"mark_unread": "mention"})
	setNotifyProps(t, ctx, db, "channel-general", "user-quiet", map[string]any{"desktop": "none"})

	counters, err := service.BumpUnread(ctx, "channel-general", "user-author", []string{"user-mentioned", "user-muted"})
	if err != nil {
		t.Fatalf("bump unread: %v", err)
	}
	byUser := countersByUser(counters)
	if _, ok := byUser["user-author"]; ok {
		t.Fatalf("author accrued unread state: %#v", byUser)
	}
	if got := byUser["user-plain"]; got.MsgCount != 1 || got.MentionCount != 0 {
		t.Fatalf("plain member counter = %#v", got)
	}
	if got := byUser["user-mentioned"]; got.MsgCount != 1 || got.MentionCount != 1 {
		t.Fatalf("mentioned member counter = %#v", got)
	}
	// A muted member (mark_unread=mention) is bumped only when also mentioned;
	// being mentioned restores the ordinary message counter as well.
	if got := byUser["user-muted"]; got.MsgCount != 1 || got.MentionCount != 1 {
		t.Fatalf("muted mentioned member counter = %#v", got)
	}
	if got := byUser["user-quiet"]; got.Desktop != "none" {
		t.Fatalf("desktop notify prop not surfaced: %#v", got)
	}
	if got := byUser["user-plain"]; got.Desktop != "all" {
		t.Fatalf("default desktop notify prop = %q, want all", got.Desktop)
	}

	// A second message with no mentions must not re-increment mention_count.
	if _, err := service.BumpUnread(ctx, "channel-general", "user-author", nil); err != nil {
		t.Fatalf("second bump unread: %v", err)
	}
	second := readMemberCounters(t, ctx, db, "channel-general", "user-mentioned")
	if second.MsgCount != 2 || second.MentionCount != 1 {
		t.Fatalf("counters after unmentioned message = %#v", second)
	}
	// The unmentioned second message is the muting case that must stay flat.
	mutedSecond := readMemberCounters(t, ctx, db, "channel-general", "user-muted")
	if mutedSecond.MsgCount != 1 || mutedSecond.MentionCount != 1 {
		t.Fatalf("muted counters after unmentioned message = %#v", mutedSecond)
	}

	// Members of another channel must never be touched by this channel's bump.
	other := readMemberCounters(t, ctx, db, "channel-other", "user-plain")
	if other.MsgCount != 0 || other.MentionCount != 0 {
		t.Fatalf("unrelated channel counters mutated: %#v", other)
	}
}

// TestMarkViewedAndMarkUnreadFromPostRebuildCounters covers the read-marker
// round trip: viewing clears counters, and rewinding to a post rebuilds both
// counters from the persisted history rather than trusting the previous value.
func TestMarkViewedAndMarkUnreadFromPostRebuildCounters(t *testing.T) {
	db := newChannelsTestDB(t)
	ctx := channelsTestContext(t)
	seedChannelsFixture(t, ctx, db)
	service := New(db)

	// Three messages; the reader is mentioned on the second and third.
	insertPost(t, ctx, db, "post-1", "channel-general", "user-author", 1_000, nil)
	insertPost(t, ctx, db, "post-2", "channel-general", "user-author", 2_000, []string{"user-plain"})
	insertPost(t, ctx, db, "post-3", "channel-general", "user-author", 3_000, []string{"user-plain"})
	if _, err := service.BumpUnread(ctx, "channel-general", "user-author", nil); err != nil {
		t.Fatalf("seed unread: %v", err)
	}

	viewedAt, err := service.MarkViewed(ctx, "channel-general", "user-plain")
	if err != nil {
		t.Fatalf("mark viewed: %v", err)
	}
	if viewedAt <= 0 {
		t.Fatalf("mark viewed timestamp = %d", viewedAt)
	}
	cleared := readMemberCounters(t, ctx, db, "channel-general", "user-plain")
	if cleared.MsgCount != 0 || cleared.MentionCount != 0 || cleared.LastViewedAt != viewedAt {
		t.Fatalf("counters after view = %#v (viewedAt=%d)", cleared, viewedAt)
	}

	// Rewind so post-2 is the first unread row: two messages, two mentions.
	boundary, msgCount, mentionCount, err := service.MarkUnreadFromPost(ctx, "channel-general", "user-plain", 2_000)
	if err != nil {
		t.Fatalf("mark unread from post: %v", err)
	}
	if boundary != 1_999 || msgCount != 2 || mentionCount != 2 {
		t.Fatalf("rewind = boundary %d, msg %d, mention %d", boundary, msgCount, mentionCount)
	}
	persisted := readMemberCounters(t, ctx, db, "channel-general", "user-plain")
	if persisted.LastViewedAt != 1_999 || persisted.MsgCount != 2 || persisted.MentionCount != 2 {
		t.Fatalf("persisted rewind = %#v", persisted)
	}

	// A soft-deleted post must drop out of the rebuilt counters.
	if _, err := db.Pool.Exec(ctx, `UPDATE posts SET delete_at=9_000 WHERE id='post-3'`); err != nil {
		t.Fatalf("soft delete post-3: %v", err)
	}
	_, msgCount, mentionCount, err = service.MarkUnreadFromPost(ctx, "channel-general", "user-plain", 2_000)
	if err != nil {
		t.Fatalf("rewind after delete: %v", err)
	}
	if msgCount != 1 || mentionCount != 1 {
		t.Fatalf("rewind after delete = msg %d, mention %d", msgCount, mentionCount)
	}

	// A non-positive create_at is a no-op and must not clobber the marker.
	if _, _, _, err := service.MarkUnreadFromPost(ctx, "channel-general", "user-plain", 0); err != nil {
		t.Fatalf("no-op rewind: %v", err)
	}
	if after := readMemberCounters(t, ctx, db, "channel-general", "user-plain"); after.LastViewedAt != 1_999 {
		t.Fatalf("no-op rewind moved marker: %#v", after)
	}
}

// TestListForUserWithCountsScopesTeamsAndArchivedChannels locks the sidebar
// read model: archived channels disappear, a team filter excludes other teams
// while always retaining DMs, and notify props round-trip as JSON.
func TestListForUserWithCountsScopesTeamsAndArchivedChannels(t *testing.T) {
	db := newChannelsTestDB(t)
	ctx := channelsTestContext(t)
	seedChannelsFixture(t, ctx, db)
	service := New(db)
	setNotifyProps(t, ctx, db, "channel-general", "user-plain", map[string]any{"desktop": "mention"})

	scoped, err := service.ListForUserWithCounts(ctx, "user-plain", "team-main")
	if err != nil {
		t.Fatalf("list scoped: %v", err)
	}
	if got := channelIDs(scoped); !equalIDs(got, []string{"channel-dm", "channel-general", "channel-other"}) {
		t.Fatalf("team-scoped channels = %v", got)
	}
	for _, row := range scoped {
		if row.ChannelID != "channel-general" {
			continue
		}
		if row.NotifyProps["desktop"] != "mention" {
			t.Fatalf("notify props did not round-trip: %#v", row.NotifyProps)
		}
	}

	// Archiving must remove the channel from the sidebar read model.
	if _, err := db.Pool.Exec(ctx, `UPDATE channels SET delete_at=1 WHERE id='channel-other'`); err != nil {
		t.Fatalf("archive channel-other: %v", err)
	}
	afterArchive, err := service.ListForUserWithCounts(ctx, "user-plain", "team-main")
	if err != nil {
		t.Fatalf("list after archive: %v", err)
	}
	if got := channelIDs(afterArchive); !equalIDs(got, []string{"channel-dm", "channel-general"}) {
		t.Fatalf("channels after archive = %v", got)
	}

	// The cross-team read model keeps every live membership in one round-trip.
	all, err := service.ListAllForUserWithCounts(ctx, "user-plain")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if got := channelIDs(all); !equalIDs(got, []string{"channel-dm", "channel-general", "channel-side"}) {
		t.Fatalf("cross-team channels = %v", got)
	}

	// A user with no memberships gets an empty slice, never a nil map/slice.
	empty, err := service.ListAllForUserWithCounts(ctx, "user-outsider")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("outsider read model = %#v, %v", empty, err)
	}
}

// TestEnsureGroupDoesNotMutateCallerSlice guards the in-place dedupe hazard:
// a service helper must never reorder or truncate the caller's slice, because
// the HTTP layer still uses the request body after the call returns.
func TestEnsureGroupDoesNotMutateCallerSlice(t *testing.T) {
	db := newChannelsTestDB(t)
	ctx := channelsTestContext(t)
	seedChannelsFixture(t, ctx, db)
	service := New(db)

	request := []string{"user-plain", "user-author", "user-mentioned", "user-plain"}
	original := append([]string(nil), request...)
	if _, err := service.EnsureGroup(ctx, request); err != nil {
		t.Fatalf("ensure group: %v", err)
	}
	if !equalOrderedIDs(request, original) {
		t.Fatalf("caller slice mutated: got %v, want %v", request, original)
	}
}

func countersByUser(counters []Counter) map[string]Counter {
	out := map[string]Counter{}
	for _, c := range counters {
		out[c.UserID] = c
	}
	return out
}

func channelIDs(rows []MemberWithCounts) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ChannelID)
	}
	sort.Strings(out)
	return out
}

func equalIDs(got, want []string) bool {
	sort.Strings(want)
	return equalOrderedIDs(got, want)
}

func equalOrderedIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func setNotifyProps(t *testing.T, ctx context.Context, db *store.DB, channelID, userID string, props map[string]any) {
	t.Helper()
	raw, err := json.Marshal(props)
	if err != nil {
		t.Fatalf("marshal notify props: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		UPDATE channel_members SET notify_props=$1 WHERE channel_id=$2 AND user_id=$3
	`, raw, channelID, userID); err != nil {
		t.Fatalf("set notify props: %v", err)
	}
}

func readMemberCounters(t *testing.T, ctx context.Context, db *store.DB, channelID, userID string) MemberWithCounts {
	t.Helper()
	var row MemberWithCounts
	if err := db.Pool.QueryRow(ctx, `
		SELECT channel_id, user_id, last_viewed_at, msg_count, mention_count
		FROM channel_members WHERE channel_id=$1 AND user_id=$2
	`, channelID, userID).Scan(&row.ChannelID, &row.UserID, &row.LastViewedAt, &row.MsgCount, &row.MentionCount); err != nil {
		t.Fatalf("read member counters: %v", err)
	}
	return row
}

func insertPost(t *testing.T, ctx context.Context, db *store.DB, postID, channelID, userID string, createAt int64, mentions []string) {
	t.Helper()
	props := map[string]any{}
	if len(mentions) > 0 {
		props["mention_user_ids"] = mentions
	}
	raw, err := json.Marshal(props)
	if err != nil {
		t.Fatalf("marshal post props: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO posts (id, channel_id, user_id, root_id, message, props, file_ids, is_pinned, create_at, update_at)
		VALUES ($1,$2,$3,'',$4,$5,'[]'::jsonb,FALSE,$6,$6)
	`, postID, channelID, userID, "message "+postID, raw, createAt); err != nil {
		t.Fatalf("insert post %s: %v", postID, err)
	}
}

func seedChannelsFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES
			('user-author',    'user-author',    'author@example.test',    'hash', 'system_user', 1, 1),
			('user-plain',     'user-plain',     'plain@example.test',     'hash', 'system_user', 1, 1),
			('user-mentioned', 'user-mentioned', 'mentioned@example.test', 'hash', 'system_user', 1, 1),
			('user-muted',     'user-muted',     'muted@example.test',     'hash', 'system_user', 1, 1),
			('user-quiet',     'user-quiet',     'quiet@example.test',     'hash', 'system_user', 1, 1),
			('user-outsider',  'user-outsider',  'outsider@example.test',  'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES
			('team-main', 'team-main', 'Main Team', 'O', 1, 1),
			('team-side', 'team-side', 'Side Team', 'O', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES
			('team-main', 'user-author', 'team_user', 1),
			('team-main', 'user-plain', 'team_user', 1),
			('team-main', 'user-mentioned', 'team_user', 1),
			('team-main', 'user-muted', 'team_user', 1),
			('team-main', 'user-quiet', 'team_user', 1),
			('team-side', 'user-plain', 'team_user', 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES
			('channel-general', 'team-main', 'O', 'General', 'general', 1, 1),
			('channel-other',   'team-main', 'O', 'Other',   'other',   1, 1),
			('channel-side',    'team-side', 'O', 'Side',    'side',    1, 1),
			('channel-dm',      NULL,        'D', 'DM',      'dm',      1, 1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES
			('channel-general', 'user-author', 'channel_user', 1),
			('channel-general', 'user-plain', 'channel_user', 1),
			('channel-general', 'user-mentioned', 'channel_user', 1),
			('channel-general', 'user-muted', 'channel_user', 1),
			('channel-general', 'user-quiet', 'channel_user', 1),
			('channel-other',   'user-plain', 'channel_user', 1),
			('channel-side',    'user-plain', 'channel_user', 1),
			('channel-dm',      'user-plain', 'channel_user', 1),
			('channel-dm',      'user-author', 'channel_user', 1)
	`); err != nil {
		t.Fatalf("seed channels fixture: %v", err)
	}
}

func newChannelsTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(channelsTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", channelsTestPostgresDSN)
	}
	ctx := channelsTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open channels test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping channels test PostgreSQL: %v", err)
	}

	schemaName := "moyro_channels_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create channels test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop channels test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse channels test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated channels test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated channels test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate channels test schema: %v", err)
	}
	return db
}

func channelsTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}
