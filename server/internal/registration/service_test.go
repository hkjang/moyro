package registration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/invites"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeRegistrationState struct {
	mu             sync.Mutex
	uses           int
	maxUses        int
	committedUsers int
	inviteKind     invites.Kind
	channelIDs     []string
	guestTTL       int64
	guestDownload  bool
	defaultQueries int
}

func (s *fakeRegistrationState) begin(context.Context) (registrationTx, error) {
	return &fakeRegistrationTx{state: s}, nil
}

type fakeRegistrationTx struct {
	state       *fakeRegistrationState
	locked      bool
	consumed    bool
	pendingUser bool
	closed      bool
}

func (tx *fakeRegistrationTx) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	switch {
	case strings.Contains(query, "UPDATE invite_tokens"):
		tx.state.mu.Lock()
		tx.locked = true
		if tx.state.maxUses > 0 && tx.state.uses >= tx.state.maxUses {
			return fakeRegistrationRow{err: pgx.ErrNoRows}
		}
		tx.state.uses++
		tx.consumed = true
		kind := tx.state.inviteKind
		if kind == "" {
			kind = invites.KindMember
		}
		return fakeRegistrationRow{values: []any{
			"invited-team", kind, tx.state.channelIDs, tx.state.guestTTL, tx.state.guestDownload || kind == invites.KindMember,
		}}
	case strings.Contains(query, "INSERT INTO teams"):
		tx.state.defaultQueries++
		return fakeRegistrationRow{values: []any{"default-team", int64(0)}}
	case strings.Contains(query, "INSERT INTO channels"):
		tx.state.defaultQueries++
		teamID, _ := args[1].(string)
		return fakeRegistrationRow{values: []any{"general-" + teamID, int64(0)}}
	default:
		return fakeRegistrationRow{err: fmt.Errorf("unexpected QueryRow: %s", query)}
	}
}

func (tx *fakeRegistrationTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "INSERT INTO users") {
		tx.pendingUser = true
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (tx *fakeRegistrationTx) Commit(context.Context) error {
	if tx.closed {
		return nil
	}
	if tx.pendingUser {
		tx.state.committedUsers++
	}
	tx.closed = true
	if tx.locked {
		tx.state.mu.Unlock()
	}
	return nil
}

func (tx *fakeRegistrationTx) Rollback(context.Context) error {
	if tx.closed {
		return nil
	}
	if tx.consumed {
		tx.state.uses--
	}
	tx.closed = true
	if tx.locked {
		tx.state.mu.Unlock()
	}
	return nil
}

type fakeRegistrationRow struct {
	values []any
	err    error
}

func (row fakeRegistrationRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != len(row.values) {
		return fmt.Errorf("scan destinations=%d values=%d", len(dest), len(row.values))
	}
	for i, value := range row.values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		case *invites.Kind:
			*target = value.(invites.Kind)
		case *[]string:
			*target = value.([]string)
		case *bool:
			*target = value.(bool)
		default:
			return fmt.Errorf("unsupported scan target %T", dest[i])
		}
	}
	return nil
}

func TestLimitedInviteConcurrentRegistrationCreatesExactlyOneAccount(t *testing.T) {
	state := &fakeRegistrationState{maxUses: 1}
	service := &Service{begin: state.begin, now: func() time.Time { return time.Unix(100, 0) }}

	errorsCh := make(chan error, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, _, err := service.Register(context.Background(), Input{
				Username: fmt.Sprintf("invitee-%d", index),
				Email:    fmt.Sprintf("invitee-%d@example.com", index),
				Password: "long-test-password",
				InviteID: "one-seat",
			})
			errorsCh <- err
		}(i)
	}
	wait.Wait()
	close(errorsCh)

	var succeeded, exhausted int
	for err := range errorsCh {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, invites.ErrInvalidInvite):
			exhausted++
		default:
			t.Fatalf("unexpected registration error: %v", err)
		}
	}
	if succeeded != 1 || exhausted != 1 || state.uses != 1 || state.committedUsers != 1 {
		t.Fatalf("success=%d exhausted=%d uses=%d users=%d", succeeded, exhausted, state.uses, state.committedUsers)
	}
}

func TestGuestInviteRegistrationAppliesRestrictedScopeWithoutDefaultSpace(t *testing.T) {
	state := &fakeRegistrationState{
		maxUses: 1, inviteKind: invites.KindGuest, channelIDs: []string{"partner-channel"},
		guestTTL: int64((24 * time.Hour) / time.Second), guestDownload: false,
	}
	service := &Service{begin: state.begin, now: func() time.Time { return time.Unix(100, 0) }}
	user, teamID, err := service.Register(context.Background(), Input{
		Username: "external", Email: "external@example.com", Password: "long-test-password", InviteID: "guest-seat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if teamID != "invited-team" || user.Roles != "system_guest" || user.GuestFileDownload {
		t.Fatalf("guest result = team %q user %#v", teamID, user)
	}
	wantExpiry := time.Unix(100, 0).UnixMilli() + (24 * time.Hour).Milliseconds()
	if user.GuestExpiresAt != wantExpiry {
		t.Fatalf("guest expiry = %d, want %d", user.GuestExpiresAt, wantExpiry)
	}
	if state.defaultQueries != 0 || state.committedUsers != 1 || state.uses != 1 {
		t.Fatalf("default queries=%d committed=%d uses=%d", state.defaultQueries, state.committedUsers, state.uses)
	}
}
