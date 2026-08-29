package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

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
	svc := New(nil, []byte("test-secret"), time.Hour)

	first, err := svc.issueToken("user-1")
	if err != nil {
		t.Fatalf("first issueToken() error = %v", err)
	}
	second, err := svc.issueToken("user-1")
	if err != nil {
		t.Fatalf("second issueToken() error = %v", err)
	}

	if first == second {
		t.Fatal("issueToken() returned duplicate tokens for repeated logins")
	}

	claims, err := svc.Parse(first)
	if err != nil {
		t.Fatalf("Parse(first) error = %v", err)
	}
	if claims.ID == "" {
		t.Fatal("token is missing a JWT ID")
	}
	if claims.UserID != "user-1" {
		t.Fatalf("claims.UserID = %q, want user-1", claims.UserID)
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
