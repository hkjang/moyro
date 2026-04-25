package auth

import (
	"testing"
	"time"
)

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
