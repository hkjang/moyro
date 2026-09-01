package ws

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const audienceTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestDatabaseAudienceResolverRequiresLiveIntersectingMembership(t *testing.T) {
	db := newAudienceTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES
			('audience-member','audience-member','audience-member@example.test','hash','system_user',1,1),
			('audience-channel-only','audience-channel-only','audience-channel-only@example.test','hash','system_user',1,1),
			('audience-outsider','audience-outsider','audience-outsider@example.test','hash','system_user',1,1),
			('audience-live-guest','audience-live-guest','audience-live-guest@example.test','hash','system_guest',1,1),
			('audience-expired-guest','audience-expired-guest','audience-expired-guest@example.test','hash','system_guest',1,1);
		UPDATE users SET guest_expires_at=9999999999999 WHERE id='audience-live-guest';
		UPDATE users SET guest_expires_at=1 WHERE id='audience-expired-guest';
		INSERT INTO teams (id, display_name, name, type, create_at, update_at)
		VALUES
			('','No Team','audience-no-team','O',1,1),
			('audience-team','Audience Team','audience-team','O',1,1),
			('audience-other-team','Other Audience Team','audience-other-team','O',1,1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES
			('audience-team','audience-member','team_user',1),
			('audience-team','audience-live-guest','team_user',1),
			('audience-team','audience-expired-guest','team_user',1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES
			('audience-channel','audience-team','O','Audience Channel','audience-channel',1,1),
			('audience-direct','','D','Audience Direct','audience-direct',1,1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES
			('audience-channel','audience-member','channel_user',1),
			('audience-channel','audience-channel-only','channel_user',1),
			('audience-channel','audience-live-guest','channel_user',1),
			('audience-channel','audience-expired-guest','channel_user',1),
			('audience-direct','audience-channel-only','channel_user',1)
	`); err != nil {
		t.Fatalf("seed audience fixture: %v", err)
	}

	resolve := DatabaseAudienceResolver(db)
	audience, err := resolve(ctx, Broadcast{ChannelID: "audience-channel", TeamID: "audience-team"})
	if err != nil {
		t.Fatalf("resolve live channel and team audience: %v", err)
	}
	if _, ok := audience["audience-member"]; !ok {
		t.Fatalf("live member missing from audience: %#v", audience)
	}
	if _, ok := audience["audience-channel-only"]; ok {
		t.Fatalf("user without live team membership entered audience: %#v", audience)
	}
	if _, ok := audience["audience-outsider"]; ok {
		t.Fatalf("outsider entered audience: %#v", audience)
	}
	if _, ok := audience["audience-live-guest"]; !ok {
		t.Fatalf("live guest missing from audience: %#v", audience)
	}
	if _, ok := audience["audience-expired-guest"]; ok {
		t.Fatalf("expired guest entered channel audience: %#v", audience)
	}
	teamAudience, err := resolve(ctx, Broadcast{TeamID: "audience-team"})
	if err != nil {
		t.Fatalf("resolve team audience: %v", err)
	}
	if _, ok := teamAudience["audience-live-guest"]; !ok {
		t.Fatalf("live guest missing from team audience: %#v", teamAudience)
	}
	if _, ok := teamAudience["audience-expired-guest"]; ok {
		t.Fatalf("expired guest entered team audience: %#v", teamAudience)
	}
	directAudience, err := resolve(ctx, Broadcast{ChannelID: "audience-direct"})
	if err != nil {
		t.Fatalf("resolve direct channel audience: %v", err)
	}
	if _, ok := directAudience["audience-channel-only"]; !ok || len(directAudience) != 1 {
		t.Fatalf("teamless direct channel audience = %#v", directAudience)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM channel_members WHERE channel_id='audience-channel' AND user_id='audience-member'`); err != nil {
		t.Fatalf("revoke audience channel membership: %v", err)
	}
	revoked, err := resolve(ctx, Broadcast{ChannelID: "audience-channel", TeamID: "audience-team"})
	if err != nil {
		t.Fatalf("resolve revoked channel membership: %v", err)
	}
	if _, ok := revoked["audience-member"]; ok {
		t.Fatalf("revoked channel member remained in audience: %#v", revoked)
	}
	if _, ok := revoked["audience-live-guest"]; !ok {
		t.Fatalf("unrelated live guest disappeared from audience: %#v", revoked)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO channel_members (channel_id,user_id,roles,create_at) VALUES ('audience-channel','audience-member','channel_user',1)`); err != nil {
		t.Fatalf("restore audience channel membership: %v", err)
	}

	mismatched, err := resolve(ctx, Broadcast{ChannelID: "audience-channel", TeamID: "audience-other-team"})
	if err != nil {
		t.Fatalf("resolve mismatched channel and team audience: %v", err)
	}
	if len(mismatched) != 0 {
		t.Fatalf("mismatched channel and team audience = %#v", mismatched)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE teams SET delete_at=10 WHERE id='audience-team'`); err != nil {
		t.Fatalf("archive audience team: %v", err)
	}
	archivedTeamChannel, err := resolve(ctx, Broadcast{ChannelID: "audience-channel"})
	if err != nil {
		t.Fatalf("resolve channel in archived team: %v", err)
	}
	if len(archivedTeamChannel) != 0 {
		t.Fatalf("channel in archived team audience = %#v", archivedTeamChannel)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE teams SET delete_at=0 WHERE id='audience-team'`); err != nil {
		t.Fatalf("restore audience team: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE channels SET delete_at=10 WHERE id='audience-channel'`); err != nil {
		t.Fatalf("archive audience channel: %v", err)
	}
	archived, err := resolve(ctx, Broadcast{ChannelID: "audience-channel"})
	if err != nil {
		t.Fatalf("resolve archived channel audience: %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("archived channel audience = %#v", archived)
	}
}

func TestDatabaseAudienceResolverRejectsArchivedTeam(t *testing.T) {
	db := newAudienceTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('audience-team-member','audience-team-member','audience-team-member@example.test','hash','system_user',1,1);
		INSERT INTO teams (id, display_name, name, type, create_at, update_at, delete_at)
		VALUES ('audience-archived-team','Archived Team','audience-archived-team','O',1,1,10);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES ('audience-archived-team','audience-team-member','team_user',1)
	`); err != nil {
		t.Fatalf("seed archived team fixture: %v", err)
	}

	audience, err := DatabaseAudienceResolver(db)(ctx, Broadcast{TeamID: "audience-archived-team"})
	if err != nil {
		t.Fatalf("resolve archived team audience: %v", err)
	}
	if len(audience) != 0 {
		t.Fatalf("archived team audience = %#v", audience)
	}
}

func TestDatabaseAudienceResolverLimitsSubjectEventsForGuestsPostgres(t *testing.T) {
	db := newAudienceTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	now := time.Now().UnixMilli()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users
			(id, username, email, password_hash, roles, create_at, update_at, delete_at, guest_expires_at)
		VALUES
			('presence-subject','presence-subject','presence-subject@example.test','hash','system_user',$1,$1,0,0),
			('presence-regular','presence-regular','presence-regular@example.test','hash','system_user',$1,$1,0,0),
			('presence-shared-guest','presence-shared-guest','presence-shared-guest@example.test','hash','system_guest',$1,$1,0,$2),
			('presence-unshared-guest','presence-unshared-guest','presence-unshared-guest@example.test','hash','system_guest',$1,$1,0,$2),
			('presence-expired-guest','presence-expired-guest','presence-expired-guest@example.test','hash','system_guest',$1,$1,0,$3),
			('presence-deleted','presence-deleted','presence-deleted@example.test','hash','system_user',$1,$1,$1,0),
			('presence-expired-subject','presence-expired-subject','presence-expired-subject@example.test','hash','system_guest',$1,$1,0,$3)
	`, now, now+int64(time.Hour/time.Millisecond), now-1); err != nil {
		t.Fatalf("seed subject audience users: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO teams (id, display_name, name, type, create_at, update_at)
		VALUES ('presence-team','Presence Team','presence-team','O',$1,$1)
	`, now); err != nil {
		t.Fatalf("seed subject audience team: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES
			('presence-team','presence-subject','team_user',$1),
			('presence-team','presence-shared-guest','team_user',$1),
			('presence-team','presence-expired-guest','team_user',$1)
	`, now); err != nil {
		t.Fatalf("seed subject audience team members: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES ('presence-shared-channel','presence-team','P','Presence Shared','presence-shared-channel',$1,$1)
	`, now); err != nil {
		t.Fatalf("seed subject audience channel: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES
			('presence-shared-channel','presence-subject','channel_user',$1),
			('presence-shared-channel','presence-shared-guest','channel_user',$1),
			('presence-shared-channel','presence-expired-guest','channel_user',$1)
	`, now); err != nil {
		t.Fatalf("seed subject audience channel members: %v", err)
	}

	resolve := DatabaseAudienceResolver(db)
	audience, err := resolve(ctx, Broadcast{SubjectUserID: "presence-subject"})
	if err != nil {
		t.Fatalf("resolve subject audience: %v", err)
	}
	for _, want := range []string{"presence-subject", "presence-regular", "presence-shared-guest"} {
		if _, ok := audience[want]; !ok {
			t.Errorf("expected recipient %q missing from %#v", want, audience)
		}
	}
	for _, denied := range []string{"presence-unshared-guest", "presence-expired-guest", "presence-deleted"} {
		if _, ok := audience[denied]; ok {
			t.Errorf("denied recipient %q entered %#v", denied, audience)
		}
	}

	intersected, err := resolve(ctx, Broadcast{
		ChannelID: "presence-shared-channel", SubjectUserID: "presence-subject",
	})
	if err != nil {
		t.Fatalf("resolve channel and subject intersection: %v", err)
	}
	if _, ok := intersected["presence-subject"]; !ok {
		t.Fatalf("subject missing from intersected audience: %#v", intersected)
	}
	if _, ok := intersected["presence-shared-guest"]; !ok {
		t.Fatalf("shared guest missing from intersected audience: %#v", intersected)
	}
	if _, ok := intersected["presence-regular"]; ok {
		t.Fatalf("regular user outside channel entered intersection: %#v", intersected)
	}

	for _, unavailableSubject := range []string{"presence-expired-subject", "presence-deleted", "presence-missing"} {
		resolved, err := resolve(ctx, Broadcast{SubjectUserID: unavailableSubject})
		if err != nil {
			t.Fatalf("resolve unavailable subject %q: %v", unavailableSubject, err)
		}
		if len(resolved) != 0 {
			t.Fatalf("unavailable subject %q audience = %#v, want empty", unavailableSubject, resolved)
		}
	}
}

func newAudienceTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(audienceTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", audienceTestPostgresDSN)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open audience test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping audience test PostgreSQL: %v", err)
	}
	schemaName := "moyro_ws_audience_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create audience test schema: %v", err)
	}
	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop audience test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse audience test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	config.MaxConns = 4
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated audience test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated audience test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate audience test schema: %v", err)
	}
	return db
}
