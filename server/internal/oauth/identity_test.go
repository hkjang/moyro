package oauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		" Operator@Example.COM ": "operator@example.com",
		"already@example.test":   "already@example.test",
		"\t\n":                   "",
	} {
		if got := NormalizeEmail(input); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", input, got, want)
		}
	}
}

type fakeIdentityTx struct {
	ownerID            string
	failIdentityInsert error
	failPictureUpdate  error
	events             []string
	committed          bool
	rolledBack         bool
	closed             bool
}

func (tx *fakeIdentityTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	switch {
	case strings.Contains(query, "INSERT INTO users"):
		tx.events = append(tx.events, "insert-user")
	case strings.Contains(query, "INSERT INTO user_identities"):
		tx.events = append(tx.events, "insert-identity")
		if tx.failIdentityInsert != nil {
			return pgconn.CommandTag{}, tx.failIdentityInsert
		}
	case strings.Contains(query, "UPDATE users SET picture"):
		tx.events = append(tx.events, "update-picture")
		if tx.failPictureUpdate != nil {
			return pgconn.CommandTag{}, tx.failPictureUpdate
		}
	default:
		return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec: %s", query)
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (tx *fakeIdentityTx) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if !strings.Contains(query, "SELECT user_id FROM user_identities") {
		return fakeIdentityRow{err: fmt.Errorf("unexpected QueryRow: %s", query)}
	}
	tx.events = append(tx.events, "read-owner")
	return fakeIdentityRow{value: tx.ownerID}
}

func (tx *fakeIdentityTx) Commit(context.Context) error {
	if tx.closed {
		return pgx.ErrTxClosed
	}
	tx.events = append(tx.events, "commit")
	tx.committed = true
	tx.closed = true
	return nil
}

func (tx *fakeIdentityTx) Rollback(context.Context) error {
	if tx.closed {
		return pgx.ErrTxClosed
	}
	tx.events = append(tx.events, "rollback")
	tx.rolledBack = true
	tx.closed = true
	return nil
}

type fakeIdentityRow struct {
	value string
	err   error
}

func (row fakeIdentityRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != 1 {
		return fmt.Errorf("scan destinations=%d, want 1", len(dest))
	}
	target, ok := dest[0].(*string)
	if !ok {
		return fmt.Errorf("unsupported scan target %T", dest[0])
	}
	*target = row.value
	return nil
}

func TestLinkExistingIdentityFailsClosedOnConcurrentOwnerConflict(t *testing.T) {
	tx := &fakeIdentityTx{ownerID: "different-user"}
	begin := func(context.Context) (identityTx, error) { return tx, nil }

	err := linkExistingIdentity(context.Background(), begin, "email-matched-user", "keycloak", &UserInfo{
		Subject: "subject-1", Email: "operator@example.com",
	}, 100)
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("linkExistingIdentity error = %v, want ErrIdentityConflict", err)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("conflicting link committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
	if got := strings.Join(tx.events, ","); got != "insert-identity,read-owner,rollback" {
		t.Fatalf("events = %s", got)
	}
}

func TestLinkExistingIdentityAcceptsConcurrentSameOwner(t *testing.T) {
	tx := &fakeIdentityTx{ownerID: "email-matched-user"}
	begin := func(context.Context) (identityTx, error) { return tx, nil }

	err := linkExistingIdentity(context.Background(), begin, "email-matched-user", "keycloak", &UserInfo{
		Subject: "subject-1", Email: "operator@example.com", Picture: "https://idp.internal/avatar",
	}, 100)
	if err != nil {
		t.Fatalf("linkExistingIdentity: %v", err)
	}
	if !tx.committed || tx.rolledBack {
		t.Fatalf("same-owner link committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
	if got := strings.Join(tx.events, ","); got != "insert-identity,read-owner,update-picture,commit" {
		t.Fatalf("events = %s", got)
	}
}

func TestCreateIdentityAccountRollsBackUserWhenIdentityInsertFails(t *testing.T) {
	wantErr := errors.New("identity insert failed")
	tx := &fakeIdentityTx{failIdentityInsert: wantErr}
	begin := func(context.Context) (identityTx, error) { return tx, nil }

	err := createIdentityAccount(context.Background(), begin, "new-user", "operator", "operator@example.com", "keycloak", &UserInfo{
		Subject: "subject-1", Email: "operator@example.com",
	}, 100)
	if !errors.Is(err, wantErr) {
		t.Fatalf("createIdentityAccount error = %v, want %v", err, wantErr)
	}
	if tx.committed || !tx.rolledBack {
		t.Fatalf("partial account committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
	if got := strings.Join(tx.events, ","); got != "insert-user,insert-identity,rollback" {
		t.Fatalf("events = %s", got)
	}
}

func TestCreateIdentityAccountCommitsUserAndIdentityTogether(t *testing.T) {
	tx := &fakeIdentityTx{}
	begin := func(context.Context) (identityTx, error) { return tx, nil }

	err := createIdentityAccount(context.Background(), begin, "new-user", "operator", "operator@example.com", "keycloak", &UserInfo{
		Subject: "subject-1", Email: "operator@example.com",
	}, 100)
	if err != nil {
		t.Fatalf("createIdentityAccount: %v", err)
	}
	if !tx.committed || tx.rolledBack {
		t.Fatalf("account committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
	if got := strings.Join(tx.events, ","); got != "insert-user,insert-identity,commit" {
		t.Fatalf("events = %s", got)
	}
}
