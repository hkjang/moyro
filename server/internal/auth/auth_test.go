package auth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/jackc/pgx/v5/pgconn"
)

var testJWTSecret = []byte("test-session-signing-key-32-byte!")

func newTokenTestService(t *testing.T) (*Service, *secrets.Manager) {
	t.Helper()
	manager, err := secrets.New(bytes.Repeat([]byte{0x41}, secrets.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	return New(nil, testJWTSecret, time.Hour, manager), manager
}

type recordingPasswordExecutor struct {
	calls      []string
	updateRows int64
	failDelete error
}

func (e *recordingPasswordExecutor) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	e.calls = append(e.calls, sql)
	if strings.Contains(sql, "DELETE FROM sessions") {
		if e.failDelete != nil {
			return pgconn.CommandTag{}, e.failDelete
		}
		return pgconn.NewCommandTag("DELETE 3"), nil
	}
	if e.updateRows == 0 {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func TestIssueTokenGeneratesUniqueJWTs(t *testing.T) {
	svc, manager := newTokenTestService(t)

	first, err := svc.issueToken("user-1")
	if err != nil {
		t.Fatalf("first issueToken() error = %v", err)
	}
	second, err := svc.issueToken("user-1")
	if err != nil {
		t.Fatalf("second issueToken() error = %v", err)
	}

	if first.Token == second.Token {
		t.Fatal("issueToken() returned duplicate tokens for repeated logins")
	}

	claims, err := svc.Parse(first.Token)
	if err != nil {
		t.Fatalf("Parse(first) error = %v", err)
	}
	if claims.ID == "" {
		t.Fatal("token is missing a JWT ID")
	}
	if claims.UserID != "user-1" {
		t.Fatalf("claims.UserID = %q, want user-1", claims.UserID)
	}
	wantDigest, err := manager.Digest(sessionJTIDigestPurpose, []byte(claims.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.JTIHash, wantDigest) || len(first.JTIHash) != sessionJTIDigestSize {
		t.Fatal("issued session did not carry the expected domain-separated JTI digest")
	}
}

func TestGuestAccessValidityFailsClosedAtExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name string
		user User
		want bool
	}{
		{name: "regular user", user: User{Roles: "system_user"}, want: true},
		{name: "live guest", user: User{Roles: "system_guest", GuestExpiresAt: now.Add(time.Second).UnixMilli()}, want: true},
		{name: "expired guest", user: User{Roles: "system_guest", GuestExpiresAt: now.UnixMilli()}, want: false},
		{name: "guest without expiry", user: User{Roles: "system_guest"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.user.GuestAccessValid(now); got != test.want {
				t.Fatalf("GuestAccessValid() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestParseRequiresStrictSessionClaims(t *testing.T) {
	svc, _ := newTokenTestService(t)
	now := time.Now()
	valid := func() *Claims {
		return &Claims{
			UserID: "user-1",
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer: sessionIssuer, ID: "jti-1",
				ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
				IssuedAt:  jwt.NewNumericDate(now),
			},
		}
	}
	tests := []struct {
		name   string
		method jwt.SigningMethod
		mutate func(*Claims)
	}{
		{name: "non HS256", method: jwt.SigningMethodHS384},
		{name: "wrong issuer", method: jwt.SigningMethodHS256, mutate: func(c *Claims) { c.Issuer = "other" }},
		{name: "missing subject", method: jwt.SigningMethodHS256, mutate: func(c *Claims) { c.UserID = "" }},
		{name: "missing jti", method: jwt.SigningMethodHS256, mutate: func(c *Claims) { c.ID = "" }},
		{name: "missing expiry", method: jwt.SigningMethodHS256, mutate: func(c *Claims) { c.ExpiresAt = nil }},
		{name: "expired", method: jwt.SigningMethodHS256, mutate: func(c *Claims) { c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute)) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := valid()
			if tt.mutate != nil {
				tt.mutate(claims)
			}
			token, err := jwt.NewWithClaims(tt.method, claims).SignedString(testJWTSecret)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Parse(token); err == nil {
				t.Fatal("Parse accepted an invalid session token")
			}
		})
	}
}

func TestValidatePasswordBoundaries(t *testing.T) {
	for _, password := range []string{
		"short",
		strings.Repeat("x", maximumPasswordBytes+1),
		"long-enough\x00password",
	} {
		if !errors.Is(ValidatePassword(password), ErrInvalidPassword) {
			t.Fatalf("ValidatePassword(%q) did not reject an unsafe password", password)
		}
	}
	for _, password := range []string{
		strings.Repeat("x", minimumPasswordBytes),
		strings.Repeat("x", maximumPasswordBytes),
	} {
		if err := ValidatePassword(password); err != nil {
			t.Fatalf("ValidatePassword(%d bytes) = %v", len(password), err)
		}
	}
}

func TestReplacePasswordAndRevokeSessions(t *testing.T) {
	executor := &recordingPasswordExecutor{updateRows: 1}
	changed, err := replacePasswordAndRevokeSessions(context.Background(), executor, "user-1", "hash", 123)
	if err != nil || !changed {
		t.Fatalf("replacePasswordAndRevokeSessions() = %v, %v", changed, err)
	}
	if len(executor.calls) != 2 || !strings.Contains(executor.calls[0], "UPDATE users") || !strings.Contains(executor.calls[1], "DELETE FROM sessions") {
		t.Fatalf("password mutation SQL order = %#v", executor.calls)
	}
}

func TestReplacePasswordAndRevokeSessionsFailsClosed(t *testing.T) {
	executor := &recordingPasswordExecutor{updateRows: 0}
	changed, err := replacePasswordAndRevokeSessions(context.Background(), executor, "missing", "hash", 123)
	if err != nil || changed || len(executor.calls) != 1 {
		t.Fatalf("missing user mutation = %v, %v, calls=%d", changed, err, len(executor.calls))
	}

	want := errors.New("delete failed")
	executor = &recordingPasswordExecutor{updateRows: 1, failDelete: want}
	changed, err = replacePasswordAndRevokeSessions(context.Background(), executor, "user-1", "hash", 123)
	if changed || !errors.Is(err, want) {
		t.Fatalf("delete failure mutation = %v, %v", changed, err)
	}
}
