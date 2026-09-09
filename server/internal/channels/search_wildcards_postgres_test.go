package channels

import (
	"context"
	"strings"
	"testing"

	"github.com/hkjang/moyro/server/internal/store"
)

// TestChannelSearchTreatsWildcardsInTheTermAsLiteralText covers every channel
// search path that answers a user-typed term with ILIKE. Before the term was
// escaped, `%` and `_` were read as pattern syntax: a lone `%` dumped the
// team's whole channel list into the quick switcher, and a team that actually
// has a channel named "50% 할인" could not be found by typing its name because
// the `%` matched everything instead of itself.
func TestChannelSearchTreatsWildcardsInTheTermAsLiteralText(t *testing.T) {
	db := newChannelsTestDB(t)
	ctx := channelsTestContext(t)
	seedWildcardChannelsFixture(t, ctx, db)
	service := New(db)

	// A bare `%` is a search for the character, not for everything: the only
	// hit is the channel whose display name literally contains a percent sign.
	// ListPublicDiscoverable additionally hides channels the caller has joined,
	// and `user-wild` is already in that one, so its answer is empty.
	for _, probe := range []struct {
		name string
		want string
		run  func() ([]Channel, error)
	}{
		{"SearchInTeam", "[discount]", func() ([]Channel, error) {
			return service.SearchInTeam(ctx, "team-wild", "user-wild", "%", 50)
		}},
		{"AutocompleteInTeam", "[discount]", func() ([]Channel, error) {
			return service.AutocompleteInTeam(ctx, "team-wild", "user-wild", "%", 50)
		}},
		{"SearchAll", "[discount]", func() ([]Channel, error) {
			return service.SearchAll(ctx, "%", 50)
		}},
		{"ListPublicDiscoverable", "[]", func() ([]Channel, error) {
			return service.ListPublicDiscoverable(ctx, "team-wild", "user-wild", "%", 50, 0)
		}},
	} {
		got, err := probe.run()
		if err != nil {
			t.Fatalf("%s with a bare %%: %v", probe.name, err)
		}
		if names := channelNames(got); names != probe.want {
			t.Fatalf("%s with a bare %% = %s, want %s", probe.name, names, probe.want)
		}
	}

	// The literal term does find the channel whose display name contains it.
	discount, err := service.SearchInTeam(ctx, "team-wild", "user-wild", "50%", 50)
	if err != nil {
		t.Fatalf("search for a literal percent: %v", err)
	}
	if len(discount) != 1 || discount[0].ID != "channel-discount" {
		t.Fatalf("search for \"50%%\" = %s, want channel-discount only", channelNames(discount))
	}

	// `_` matches exactly one character when it leaks into the pattern, so
	// "team_notes" would also drag in "teamXnotes".
	notes, err := service.AutocompleteInTeam(ctx, "team-wild", "user-wild", "team_notes", 50)
	if err != nil {
		t.Fatalf("autocomplete for an underscore name: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != "channel-underscore" {
		t.Fatalf("autocomplete for \"team_notes\" = %s, want channel-underscore only", channelNames(notes))
	}

	// A backslash used to escape the wildcard we append, turning "contains"
	// into "ends with" — and it must still be found as ordinary text.
	backslash, err := service.SearchAll(ctx, `back\slash`, 50)
	if err != nil {
		t.Fatalf("search for a backslash name: %v", err)
	}
	if len(backslash) != 1 || backslash[0].ID != "channel-backslash" {
		t.Fatalf("search for a backslash name = %s, want channel-backslash only", channelNames(backslash))
	}
}

// TestMembersAutocompleteTreatsWildcardsInThePrefixAsLiteralText is the
// mention-picker half of the same defect: typing `%` after an `@` used to list
// every member of the channel.
func TestMembersAutocompleteTreatsWildcardsInThePrefixAsLiteralText(t *testing.T) {
	db := newChannelsTestDB(t)
	ctx := channelsTestContext(t)
	seedWildcardChannelsFixture(t, ctx, db)
	service := New(db)

	members, err := service.MembersAutocomplete(ctx, "channel-discount", "%", 50)
	if err != nil {
		t.Fatalf("members autocomplete with a bare %%: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("members autocomplete with a bare %% returned %d members, want none", len(members))
	}

	// An underscore in a username is common, and it must match itself only.
	exact, err := service.MembersAutocomplete(ctx, "channel-discount", "kim_lead", 50)
	if err != nil {
		t.Fatalf("members autocomplete for an underscore username: %v", err)
	}
	if len(exact) != 1 || exact[0].Username != "kim_lead" {
		t.Fatalf("members autocomplete for \"kim_lead\" = %#v, want kim_lead only", exact)
	}
}

func channelNames(list []Channel) string {
	names := make([]string, 0, len(list))
	for _, c := range list {
		names = append(names, c.Name)
	}
	return "[" + strings.Join(names, " ") + "]"
}

// seedWildcardChannelsFixture plants names that only differ from a wildcard
// interpretation of the search term: `teamXnotes` is the decoy for `_`, and
// every channel in the team is a decoy for a bare `%`.
func seedWildcardChannelsFixture(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at)
		VALUES
			('user-wild', 'user-wild', 'wild@example.test', 'hash', 'system_user', 1, 1),
			('user-kim',  'kim_lead',  'kim@example.test',  'hash', 'system_user', 1, 1),
			('user-kimx', 'kimxlead',  'kimx@example.test', 'hash', 'system_user', 1, 1);
		INSERT INTO teams (id, name, display_name, type, create_at, update_at)
		VALUES ('team-wild', 'team-wild', 'Wild Team', 'O', 1, 1);
		INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES
			('team-wild', 'user-wild', 'team_user', 1),
			('team-wild', 'user-kim',  'team_user', 1),
			('team-wild', 'user-kimx', 'team_user', 1);
		INSERT INTO channels (id, team_id, type, display_name, name, create_at, update_at)
		VALUES
			('channel-discount',   'team-wild', 'O', '50% 할인',    'discount',   1, 1),
			('channel-underscore', 'team-wild', 'O', 'Team Notes', 'team_notes', 1, 1),
			('channel-decoy',      'team-wild', 'O', 'Team Notes', 'teamxnotes', 1, 1),
			('channel-backslash',  'team-wild', 'O', 'Escape',     'back\slash', 1, 1);
		INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES
			('channel-discount',   'user-wild', 'channel_user', 1),
			('channel-discount',   'user-kim',  'channel_user', 1),
			('channel-discount',   'user-kimx', 'channel_user', 1),
			('channel-underscore', 'user-wild', 'channel_user', 1)
	`); err != nil {
		t.Fatalf("seed wildcard channels fixture: %v", err)
	}
}
