package auth

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/secrets"
)

func TestLoginHandoffCreatesOneRecoverableAuthenticatedSessionPostgres(t *testing.T) {
	db := newAuthTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	manager, err := secrets.New(bytes.Repeat([]byte{0x63}, secrets.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db, testJWTSecret, time.Hour, manager)
	user, err := svc.Register(ctx, "sso-user", "sso@example.test", "long-test-password")
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	handoff, err := svc.CreateLoginHandoff(ctx, user.ID)
	if err != nil {
		t.Fatalf("create login handoff: %v", err)
	}
	wantDigest, ok := loginHandoffDigest(handoff.Code)
	if !ok {
		t.Fatalf("generated handoff code is malformed: %q", handoff.Code)
	}
	wantBindingDigest, ok := loginHandoffDigest(handoff.BrowserBinding)
	if !ok || handoff.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatal("generated browser binding or expiry is invalid")
	}
	var storedDigest, storedBindingDigest []byte
	if err := db.Pool.QueryRow(ctx, `
		SELECT code_hash, binding_hash FROM login_handoffs WHERE user_id=$1
	`, user.ID).Scan(&storedDigest, &storedBindingDigest); err != nil {
		t.Fatalf("read stored login handoff: %v", err)
	}
	if !bytes.Equal(storedDigest, wantDigest[:]) || !bytes.Equal(storedBindingDigest, wantBindingDigest[:]) ||
		bytes.Contains(storedDigest, []byte(handoff.Code)) || bytes.Contains(storedBindingDigest, []byte(handoff.BrowserBinding)) {
		t.Fatal("login handoff did not persist only the expected code and binding digests")
	}

	wrongBinding := "A" + handoff.BrowserBinding[1:]
	if handoff.BrowserBinding[0] == 'A' {
		wrongBinding = "B" + handoff.BrowserBinding[1:]
	}
	if _, _, err := svc.ExchangeLoginHandoff(ctx, handoff.Code, wrongBinding); !errors.Is(err, ErrInvalidLoginHandoff) {
		t.Fatalf("unbound browser exchange error = %v, want ErrInvalidLoginHandoff", err)
	}
	exchangedUser, token, err := svc.ExchangeLoginHandoff(ctx, handoff.Code, handoff.BrowserBinding)
	if err != nil {
		t.Fatalf("exchange login handoff: %v", err)
	}
	if exchangedUser.ID != user.ID || token == "" {
		t.Fatalf("exchange result = (%q, token=%t), want user %q and token", exchangedUser.ID, token != "", user.ID)
	}
	claims, err := svc.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("authenticate exchanged session: %v", err)
	}
	if claims.UserID != user.ID || claims.SessionID == "" {
		t.Fatalf("authenticated claims = user %q session %q", claims.UserID, claims.SessionID)
	}
	replayedUser, replayedToken, err := svc.ExchangeLoginHandoff(ctx, handoff.Code, handoff.BrowserBinding)
	if err != nil || replayedUser.ID != user.ID || replayedToken != token {
		t.Fatalf("idempotent retry = user %v token_equal=%t err=%v", replayedUser, replayedToken == token, err)
	}
	var sessionCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=$1`, user.ID).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 1 {
		t.Fatalf("session count after retry = %d, want 1", sessionCount)
	}
	if _, err := db.Pool.Exec(ctx, `
		UPDATE login_handoffs SET exchanged_at=$2 WHERE code_hash=$1
	`, wantDigest[:], time.Now().Add(-loginHandoffRetryWindow-time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ExchangeLoginHandoff(ctx, handoff.Code, handoff.BrowserBinding); !errors.Is(err, ErrInvalidLoginHandoff) {
		t.Fatalf("late retry error = %v, want ErrInvalidLoginHandoff", err)
	}
}

func TestLoginHandoffRejectsExpiredAndConcurrentReplayPostgres(t *testing.T) {
	db := newAuthTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	manager, err := secrets.New(bytes.Repeat([]byte{0x64}, secrets.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db, testJWTSecret, time.Hour, manager)
	user, err := svc.Register(ctx, "sso-replay-user", "sso-replay@example.test", "long-test-password")
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	expiredHandoff, err := svc.CreateLoginHandoff(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	expiredDigest, _ := loginHandoffDigest(expiredHandoff.Code)
	if _, err := db.Pool.Exec(ctx, `
		UPDATE login_handoffs SET expires_at=$2 WHERE code_hash=$1
	`, expiredDigest[:], time.Now().Add(-time.Second).UnixMilli()); err != nil {
		t.Fatalf("expire login handoff: %v", err)
	}
	if _, _, err := svc.ExchangeLoginHandoff(ctx, expiredHandoff.Code, expiredHandoff.BrowserBinding); !errors.Is(err, ErrInvalidLoginHandoff) {
		t.Fatalf("expired handoff error = %v, want ErrInvalidLoginHandoff", err)
	}

	handoff, err := svc.CreateLoginHandoff(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		token string
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, token, err := svc.ExchangeLoginHandoff(ctx, handoff.Code, handoff.BrowserBinding)
			results <- result{token: token, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	var successes int
	var firstToken string
	for result := range results {
		if result.err != nil || result.token == "" {
			t.Fatalf("unexpected concurrent exchange result: token=%t err=%v", result.token != "", result.err)
		}
		if firstToken == "" {
			firstToken = result.token
		} else if result.token != firstToken {
			t.Fatal("concurrent idempotent exchange returned different session tokens")
		}
		successes++
	}
	if successes != 2 {
		t.Fatalf("concurrent exchange results = %d successes, want 2 idempotent responses", successes)
	}
	var sessionCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=$1`, user.ID).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 1 {
		t.Fatalf("concurrent exchange created %d sessions, want 1", sessionCount)
	}
}

func TestCreateLoginHandoffRejectsExpiredGuestExplicitlyPostgres(t *testing.T) {
	db := newAuthTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager, err := secrets.New(bytes.Repeat([]byte{0x65}, secrets.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	service := New(db, testJWTSecret, time.Hour, manager)
	user, err := service.Register(ctx, "expired-handoff-guest", "expired-handoff-guest@example.test", "long-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `
		UPDATE users SET roles='system_guest', guest_expires_at=$2 WHERE id=$1
	`, user.ID, time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateLoginHandoff(ctx, user.ID); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expired guest handoff error = %v, want ErrInvalidSession", err)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM login_handoffs WHERE user_id=$1`, user.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired guest handoff rows = (%d, %v), want zero", count, err)
	}
}
