package bots

import (
	"strings"
	"testing"
)

func TestResolveTokenQueryRejectsInactiveUsersAtomically(t *testing.T) {
	canonical := strings.Join(strings.Fields(resolveTokenSQL), " ")
	for _, required := range []string{
		"JOIN users AS u ON u.id = pat.user_id",
		"u.delete_at = 0",
		"WHERE pat.token_hash = $1",
	} {
		if !strings.Contains(canonical, required) {
			t.Fatalf("PAT lookup lost active-user security predicate %q: %s", required, canonical)
		}
	}
}
