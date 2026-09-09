package auth

import (
	"context"
	"testing"
	"time"
)

// TestUserSearchTreatsWildcardsInTheTermAsLiteralText pins the directory half
// of the LIKE-escaping contract. The term reaches these queries straight from
// the client, so an unescaped `%` turned every "search" endpoint into "list
// every account, with e-mail addresses" and an unescaped `_` — which is a
// perfectly ordinary character in a username — quietly matched its neighbours.
func TestUserSearchTreatsWildcardsInTheTermAsLiteralText(t *testing.T) {
	db := newAuthTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	service := New(db, testJWTSecret, time.Hour, nil)

	for _, username := range []string{"kim_lead", "kimxlead", "park_ops"} {
		if _, err := service.Register(ctx, username, username+"@example.test", "long-test-password"); err != nil {
			t.Fatalf("register %s: %v", username, err)
		}
	}

	searched, err := service.SearchUsers(ctx, "%", 50)
	if err != nil {
		t.Fatalf("search users with a bare %%: %v", err)
	}
	if len(searched) != 0 {
		t.Fatalf("SearchUsers(%%) returned %d accounts, want none: %s", len(searched), usernames(searched))
	}

	completed, err := service.AutocompleteUsers(ctx, "%", 50)
	if err != nil {
		t.Fatalf("autocomplete users with a bare %%: %v", err)
	}
	if len(completed) != 0 {
		t.Fatalf("AutocompleteUsers(%%) returned %d accounts, want none: %s", len(completed), usernames(completed))
	}

	// An underscore has to mean itself, or "kim_lead" also finds "kimxlead".
	exact, err := service.SearchUsers(ctx, "kim_lead", 50)
	if err != nil {
		t.Fatalf("search for an underscore username: %v", err)
	}
	if len(exact) != 1 || exact[0].Username != "kim_lead" {
		t.Fatalf("SearchUsers(\"kim_lead\") = %s, want kim_lead only", usernames(exact))
	}

	exactCompleted, err := service.AutocompleteUsers(ctx, "kim_lead", 50)
	if err != nil {
		t.Fatalf("autocomplete for an underscore username: %v", err)
	}
	if len(exactCompleted) != 1 || exactCompleted[0].Username != "kim_lead" {
		t.Fatalf("AutocompleteUsers(\"kim_lead\") = %s, want kim_lead only", usernames(exactCompleted))
	}
}

func usernames(list []User) string {
	out := "["
	for i, u := range list {
		if i > 0 {
			out += " "
		}
		out += u.Username
	}
	return out + "]"
}
