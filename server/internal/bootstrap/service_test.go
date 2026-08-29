package bootstrap

import (
	"errors"
	"strings"
	"testing"
)

func TestUsernameFromEmail(t *testing.T) {
	tests := map[string]string{
		"Admin@example.com":     "admin",
		"first+ops@example.com": "first-ops",
		"..@example.com":        "admin-d2ede201",
		"This.Is.A.Very.Long.Bootstrap.Name@example.com": "this.is.a.very.long.bootstrap",
	}
	for email, want := range tests {
		if got := UsernameFromEmail(email); got != want {
			t.Errorf("UsernameFromEmail(%q) = %q, want %q", email, got, want)
		}
	}
}

func TestEnsureAdminRolesIsCanonicalAndIdempotent(t *testing.T) {
	got, changed := ensureAdminRoles("system_user custom system_user")
	if !changed || got != "custom system_admin system_user" {
		t.Fatalf("ensureAdminRoles() = %q, %v", got, changed)
	}
	again, changed := ensureAdminRoles(got)
	if changed || again != got {
		t.Fatalf("second ensure = %q, %v", again, changed)
	}
}

func TestValidateEmail(t *testing.T) {
	for _, invalid := range []string{"", "admin", "Admin <admin@example.com>", "@example.com", "admin@"} {
		if err := validateEmail(invalid); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("validateEmail(%q) = %v", invalid, err)
		}
	}
	if err := validateEmail("admin@example.local"); err != nil {
		t.Fatal(err)
	}
}

func TestUsernameFallbackIsStable(t *testing.T) {
	a := UsernameFromEmail("x@example.local")
	b := UsernameFromEmail("x@example.local")
	if a != b || !strings.HasPrefix(a, "admin-") || len(a) != len("admin-")+8 {
		t.Fatalf("fallback = %q / %q", a, b)
	}
}
