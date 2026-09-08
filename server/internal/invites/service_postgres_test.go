package invites

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const invitesTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

// TestConsumeHandsOutEveryUseExactlyOnce is the package's central claim: the
// single conditional UPDATE makes consumption atomic without row locking, so
// concurrent signups racing the last remaining use see exactly one winner.
// The claim was documented but never exercised, and an invite that overshoots
// its cap silently admits people an administrator meant to keep out.
func TestConsumeHandsOutEveryUseExactlyOnce(t *testing.T) {
	db := newInvitesTestDB(t)
	ctx := invitesTestContext(t)
	seedInvitesFixture(t, ctx, db)
	service := New(db)

	const maxUses = 5
	const racers = 24
	invite, err := service.Create(ctx, "team-main", "user-admin", maxUses, time.Hour)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, results[i] = service.Consume(ctx, invite.ID)
		}()
	}
	close(start)
	wg.Wait()

	granted := 0
	for _, err := range results {
		if err == nil {
			granted++
			continue
		}
		if !errors.Is(err, ErrInvalidInvite) {
			t.Fatalf("losing consumer error = %v, want ErrInvalidInvite", err)
		}
	}
	if granted != maxUses {
		t.Fatalf("%d of %d concurrent consumers were admitted, want exactly %d", granted, racers, maxUses)
	}

	var useCount int
	if err := db.Pool.QueryRow(ctx, `SELECT use_count FROM invite_tokens WHERE id=$1`, invite.ID).Scan(&useCount); err != nil {
		t.Fatalf("read use_count: %v", err)
	}
	if useCount != maxUses {
		t.Fatalf("use_count = %d after the race, want %d", useCount, maxUses)
	}
	if _, err := service.Validate(ctx, invite.ID); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("exhausted invite validates as %v, want ErrInvalidInvite", err)
	}
}

// TestConsumeRefusesRevokedExpiredAndUnknownInvites covers the three ways an
// invite stops being usable. Each is enforced inside the same WHERE clause, so
// a regression in one is invisible unless all three are asserted.
func TestConsumeRefusesRevokedExpiredAndUnknownInvites(t *testing.T) {
	db := newInvitesTestDB(t)
	ctx := invitesTestContext(t)
	seedInvitesFixture(t, ctx, db)
	service := New(db)

	revoked, err := service.Create(ctx, "team-main", "user-admin", 0, time.Hour)
	if err != nil {
		t.Fatalf("create revocable invite: %v", err)
	}
	if err := service.Revoke(ctx, revoked.ID, "team-main"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := service.Consume(ctx, revoked.ID); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("consume revoked = %v, want ErrInvalidInvite", err)
	}
	if _, err := service.Validate(ctx, revoked.ID); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("validate revoked = %v, want ErrInvalidInvite", err)
	}

	// Revoking from another team must not reach the row: an invite belongs to
	// the team that issued it.
	stillValid, err := service.Create(ctx, "team-main", "user-admin", 0, time.Hour)
	if err != nil {
		t.Fatalf("create cross-team invite: %v", err)
	}
	if err := service.Revoke(ctx, stillValid.ID, "team-side"); err != nil {
		t.Fatalf("cross-team revoke: %v", err)
	}
	if _, err := service.Validate(ctx, stillValid.ID); err != nil {
		t.Fatalf("invite revoked through the wrong team = %v, want it untouched", err)
	}

	expired, err := service.Create(ctx, "team-main", "user-admin", 0, time.Hour)
	if err != nil {
		t.Fatalf("create expiring invite: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE invite_tokens SET expires_at=$2 WHERE id=$1`,
		expired.ID, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatalf("age the invite: %v", err)
	}
	if _, err := service.Consume(ctx, expired.ID); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("consume expired = %v, want ErrInvalidInvite", err)
	}
	if _, err := service.Validate(ctx, expired.ID); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("validate expired = %v, want ErrInvalidInvite", err)
	}

	if _, err := service.Consume(ctx, uuid.NewString()); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("consume unknown = %v, want ErrInvalidInvite", err)
	}
	if _, err := service.Validate(ctx, uuid.NewString()); !errors.Is(err, ErrInvalidInvite) {
		t.Fatalf("validate unknown = %v, want ErrInvalidInvite", err)
	}
}

// TestUnlimitedInvitesStayUsableUntilExpiry pins the meaning of max_uses = 0,
// which the WHERE clause spells as a disjunction rather than a count.
func TestUnlimitedInvitesStayUsableUntilExpiry(t *testing.T) {
	db := newInvitesTestDB(t)
	ctx := invitesTestContext(t)
	seedInvitesFixture(t, ctx, db)
	service := New(db)

	invite, err := service.Create(ctx, "team-main", "user-admin", 0, time.Hour)
	if err != nil {
		t.Fatalf("create unlimited invite: %v", err)
	}
	for i := range 12 {
		teamID, err := service.Consume(ctx, invite.ID)
		if err != nil {
			t.Fatalf("consume %d of an unlimited invite: %v", i+1, err)
		}
		if teamID != "team-main" {
			t.Fatalf("consumption returned team %q", teamID)
		}
	}
	if _, err := service.Validate(ctx, invite.ID); err != nil {
		t.Fatalf("unlimited invite stopped validating after use: %v", err)
	}

	// A negative cap means the same thing as zero, so the caller cannot create
	// an invite that is dead on arrival.
	negative, err := service.Create(ctx, "team-main", "user-admin", -3, time.Hour)
	if err != nil {
		t.Fatalf("create invite with a negative cap: %v", err)
	}
	if negative.MaxUses != 0 {
		t.Fatalf("negative cap stored as %d, want 0", negative.MaxUses)
	}
	if _, err := service.Consume(ctx, negative.ID); err != nil {
		t.Fatalf("consume invite created with a negative cap: %v", err)
	}
}

// TestGuestInvitesMustNameActiveChannelsInTheirOwnTeam keeps a guest link from
// becoming a way into channels the issuing admin never scoped it to.
func TestGuestInvitesMustNameActiveChannelsInTheirOwnTeam(t *testing.T) {
	db := newInvitesTestDB(t)
	ctx := invitesTestContext(t)
	seedInvitesFixture(t, ctx, db)
	service := New(db)

	guestOptions := func(channelIDs ...string) CreateOptions {
		return CreateOptions{
			MaxUses: 1, TTL: time.Hour, Kind: KindGuest,
			ChannelIDs: channelIDs, GuestAccessTTL: 24 * time.Hour, GuestFileDownload: true,
		}
	}

	for name, channelIDs := range map[string][]string{
		"no channels at all":        {},
		"a channel in another team": {"channel-side"},
		"an archived channel":       {"channel-archived"},
		"a channel that is gone":    {"channel-missing"},
		"one valid and one foreign": {"channel-general", "channel-side"},
	} {
		if _, err := service.CreateWithOptions(ctx, "team-main", "user-admin", guestOptions(channelIDs...)); !errors.Is(err, ErrInvalidScope) {
			t.Fatalf("guest invite scoped to %s = %v, want ErrInvalidScope", name, err)
		}
	}

	// The guest access window is bounded on both ends.
	tooShort := guestOptions("channel-general")
	tooShort.GuestAccessTTL = time.Minute
	if _, err := service.CreateWithOptions(ctx, "team-main", "user-admin", tooShort); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("guest invite with a one-minute window = %v, want ErrInvalidScope", err)
	}
	tooLong := guestOptions("channel-general")
	tooLong.GuestAccessTTL = 400 * 24 * time.Hour
	if _, err := service.CreateWithOptions(ctx, "team-main", "user-admin", tooLong); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("guest invite with a 400-day window = %v, want ErrInvalidScope", err)
	}

	valid, err := service.CreateWithOptions(ctx, "team-main", "user-admin", guestOptions("channel-general", "channel-other"))
	if err != nil {
		t.Fatalf("create a properly scoped guest invite: %v", err)
	}
	consumed, err := service.ConsumeDetails(ctx, valid.ID)
	if err != nil {
		t.Fatalf("consume guest invite: %v", err)
	}
	if consumed.Kind != KindGuest || consumed.GuestExpiresAfterSeconds != int64((24*time.Hour).Seconds()) {
		t.Fatalf("guest consumption = %#v", consumed)
	}
	if strings.Join(consumed.ChannelIDs, ",") != "channel-general,channel-other" {
		t.Fatalf("guest channel scope = %v", consumed.ChannelIDs)
	}
}

// TestMemberInvitesCarryNoGuestScope makes sure the guest fields cannot ride
// along on a member invite, where nothing downstream would constrain them.
func TestMemberInvitesCarryNoGuestScope(t *testing.T) {
	db := newInvitesTestDB(t)
	ctx := invitesTestContext(t)
	seedInvitesFixture(t, ctx, db)
	service := New(db)

	invite, err := service.CreateWithOptions(ctx, "team-main", "user-admin", CreateOptions{
		MaxUses: 1, TTL: time.Hour, Kind: KindMember,
		ChannelIDs: []string{"channel-general"}, GuestAccessTTL: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("create member invite carrying guest scope: %v", err)
	}
	if len(invite.ChannelIDs) != 0 || invite.GuestExpiresAfterSeconds != 0 {
		t.Fatalf("member invite kept guest scope: %#v", invite)
	}
	consumed, err := service.ConsumeDetails(ctx, invite.ID)
	if err != nil {
		t.Fatalf("consume member invite: %v", err)
	}
	if consumed.Kind != KindMember || len(consumed.ChannelIDs) != 0 || consumed.GuestExpiresAfterSeconds != 0 {
		t.Fatalf("member consumption = %#v", consumed)
	}
	if !consumed.GuestFileDownload {
		t.Fatal("member invites must not inherit a guest download restriction")
	}
}

// TestListForTeamShowsLiveInvitesNewestFirst covers what the admin panel reads:
// revoked links disappear, expired ones stay visible so they can be cleaned up,
// and another team's invites never leak in.
func TestListForTeamShowsLiveInvitesNewestFirst(t *testing.T) {
	db := newInvitesTestDB(t)
	ctx := invitesTestContext(t)
	seedInvitesFixture(t, ctx, db)
	service := New(db)

	older, err := service.Create(ctx, "team-main", "user-admin", 1, time.Hour)
	if err != nil {
		t.Fatalf("create older invite: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE invite_tokens SET create_at=1 WHERE id=$1`, older.ID); err != nil {
		t.Fatalf("age the older invite: %v", err)
	}
	newer, err := service.Create(ctx, "team-main", "user-admin", 1, time.Hour)
	if err != nil {
		t.Fatalf("create newer invite: %v", err)
	}
	expired, err := service.Create(ctx, "team-main", "user-admin", 1, time.Hour)
	if err != nil {
		t.Fatalf("create expired invite: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE invite_tokens SET expires_at=1, create_at=2 WHERE id=$1`, expired.ID); err != nil {
		t.Fatalf("age out the expired invite: %v", err)
	}
	gone, err := service.Create(ctx, "team-main", "user-admin", 1, time.Hour)
	if err != nil {
		t.Fatalf("create revoked invite: %v", err)
	}
	if err := service.Revoke(ctx, gone.ID, "team-main"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := service.Create(ctx, "team-side", "user-admin", 1, time.Hour); err != nil {
		t.Fatalf("create other-team invite: %v", err)
	}

	listed, err := service.ListForTeam(ctx, "team-main")
	if err != nil {
		t.Fatalf("list invites: %v", err)
	}
	ids := make([]string, 0, len(listed))
	for _, invite := range listed {
		if invite.TeamID != "team-main" {
			t.Fatalf("listing leaked an invite for team %q", invite.TeamID)
		}
		ids = append(ids, invite.ID)
	}
	if strings.Join(ids, ",") != strings.Join([]string{newer.ID, expired.ID, older.ID}, ",") {
		t.Fatalf("listing = %v, want newest first without the revoked invite", ids)
	}
}

func seedInvitesFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES ('user-admin', 'user-admin', 'admin@example.test', 'hash', 'system_admin', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES
			('team-main', 'team-main', 'Main Team', 'O', 1, 1),
			('team-side', 'team-side', 'Side Team', 'O', 1, 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at, delete_at)
		VALUES
			('channel-general',  'team-main', 'O', 'General',  'general',  1, 1, 0),
			('channel-other',    'team-main', 'P', 'Other',    'other',    1, 1, 0),
			('channel-archived', 'team-main', 'O', 'Archived', 'archived', 1, 1, 9),
			('channel-side',     'team-side', 'O', 'Side',     'side',     1, 1, 0)
	`); err != nil {
		t.Fatalf("seed invites fixture: %v", err)
	}
}

func newInvitesTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(invitesTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", invitesTestPostgresDSN)
	}
	ctx := invitesTestContext(t)
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open invites test admin pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping invites test PostgreSQL: %v", err)
	}

	schemaName := "moyro_invites_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create invites test schema: %v", err)
	}

	var testPool *pgxpool.Pool
	t.Cleanup(func() {
		if testPool != nil {
			testPool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop invites test schema %s: %v", schemaName, err)
		}
		adminPool.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse invites test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema
	// The concurrency test needs real parallelism, not a queue of one.
	config.MaxConns = 12
	testPool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated invites test pool: %v", err)
	}
	if err := testPool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated invites test pool: %v", err)
	}
	db := &store.DB{Pool: testPool}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate invites test schema: %v", err)
	}
	return db
}

func invitesTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}
