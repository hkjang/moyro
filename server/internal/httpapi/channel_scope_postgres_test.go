package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/teams"
)

func TestCompatChannelScopeDoesNotLeakAcrossTeamsOrGuestAllowListPostgres(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate channel scope schema: %v", err)
	}

	now := time.Now().UnixMilli()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users
			(id, username, email, password_hash, roles, create_at, update_at, guest_expires_at)
		VALUES
			('scope-owner', 'scope-owner', 'scope-owner@example.test', 'unused', 'system_user', $1, $1, 0),
			('scope-member', 'scope-member', 'scope-member@example.test', 'unused', 'system_user', $1, $1, 0),
			('scope-outsider', 'scope-outsider', 'scope-outsider@example.test', 'unused', 'system_user', $1, $1, 0),
			('scope-guest', 'scope-guest', 'scope-guest@example.test', 'unused', 'system_guest', $1, $1, $2)`,
		now, now+int64(time.Hour/time.Millisecond)); err != nil {
		t.Fatalf("seed channel scope users: %v", err)
	}
	seedStatements := []string{
		`INSERT INTO teams (id, name, display_name, create_at, update_at)
		VALUES
			('scope-target-team', 'scope-target', 'Scope Target', $1, $1),
			('scope-other-team', 'scope-other', 'Scope Other', $1, $1)`,
		`INSERT INTO team_members (team_id, user_id, roles, create_at)
		VALUES
			('scope-target-team', 'scope-owner', 'team_admin team_user', $1),
			('scope-target-team', 'scope-member', 'team_user', $1),
			('scope-target-team', 'scope-guest', 'team_user', $1),
			('scope-other-team', 'scope-outsider', 'team_user', $1)`,
		`INSERT INTO channels
			(id, team_id, type, display_name, name, create_at, update_at)
		VALUES
			('scope-public', 'scope-target-team', 'O', 'Finance', 'finance', $1, $1),
			('scope-private', 'scope-target-team', 'P', 'Finance Private', 'finance-private', $1, $1),
			('scope-guest-room', 'scope-target-team', 'P', 'Guest Room', 'guest-room', $1, $1),
			('scope-other-public', 'scope-other-team', 'O', 'Finance Other', 'finance-other', $1, $1)`,
		`INSERT INTO channel_members (channel_id, user_id, roles, create_at)
		VALUES
			('scope-public', 'scope-owner', 'channel_admin channel_user', $1),
			('scope-private', 'scope-owner', 'channel_admin channel_user', $1),
			('scope-guest-room', 'scope-owner', 'channel_admin channel_user', $1),
			('scope-guest-room', 'scope-guest', 'channel_user', $1),
			('scope-other-public', 'scope-outsider', 'channel_user', $1)`,
	}
	for _, statement := range seedStatements {
		if _, err := db.Pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed channel scope fixture: %v", err)
		}
	}

	h := &handlers{
		auth:     auth.New(db, []byte("channel-scope-test-signing-key"), time.Hour, nil),
		teams:    teams.New(db),
		channels: channels.New(db),
	}

	t.Run("search autocomplete", func(t *testing.T) {
		outsider := invokeChannelScopeHandler(ctx, h.searchAutocompleteTeamChannels, http.MethodGet,
			"/api/v4/teams/scope-target-team/channels/search_autocomplete?term=finance",
			"scope-outsider", map[string]string{"teamID": "scope-target-team"}, "")
		assertChannelScopeError(t, outsider, http.StatusForbidden, "scope-public")

		guest := invokeChannelScopeHandler(ctx, h.searchAutocompleteTeamChannels, http.MethodGet,
			"/api/v4/teams/scope-target-team/channels/search_autocomplete?term=finance",
			"scope-guest", map[string]string{"teamID": "scope-target-team"}, "")
		assertChannelScopeError(t, guest, http.StatusForbidden, "scope-public")

		member := invokeChannelScopeHandler(ctx, h.searchAutocompleteTeamChannels, http.MethodGet,
			"/api/v4/teams/scope-target-team/channels/search_autocomplete?term=finance",
			"scope-member", map[string]string{"teamID": "scope-target-team"}, "")
		assertChannelIDs(t, member, http.StatusOK, []string{"scope-public"})
	})

	t.Run("composite by name", func(t *testing.T) {
		params := map[string]string{"teamName": "scope-target", "channelName": "finance"}
		outsider := invokeChannelScopeHandler(ctx, h.getTeamChannelByName, http.MethodGet,
			"/api/v4/teams/name/scope-target/channels/name/finance", "scope-outsider", params, "")
		assertChannelScopeError(t, outsider, http.StatusNotFound, "scope-public")

		guest := invokeChannelScopeHandler(ctx, h.getTeamChannelByName, http.MethodGet,
			"/api/v4/teams/name/scope-target/channels/name/finance", "scope-guest", params, "")
		assertChannelScopeError(t, guest, http.StatusNotFound, "scope-public")

		joinedParams := map[string]string{"teamName": "scope-target", "channelName": "guest-room"}
		joined := invokeChannelScopeHandler(ctx, h.getTeamChannelByName, http.MethodGet,
			"/api/v4/teams/name/scope-target/channels/name/guest-room", "scope-guest", joinedParams, "")
		assertChannelID(t, joined, http.StatusOK, "scope-guest-room")
	})

	t.Run("bulk ids", func(t *testing.T) {
		const body = `["scope-public","scope-private","scope-guest-room"]`
		params := map[string]string{"teamID": "scope-target-team"}
		outsider := invokeChannelScopeHandler(ctx, h.channelsByIDsInTeam, http.MethodPost,
			"/api/v4/teams/scope-target-team/channels/ids", "scope-outsider", params, body)
		assertChannelIDs(t, outsider, http.StatusOK, nil)

		guest := invokeChannelScopeHandler(ctx, h.channelsByIDsInTeam, http.MethodPost,
			"/api/v4/teams/scope-target-team/channels/ids", "scope-guest", params, body)
		assertChannelIDs(t, guest, http.StatusOK, []string{"scope-guest-room"})

		member := invokeChannelScopeHandler(ctx, h.channelsByIDsInTeam, http.MethodPost,
			"/api/v4/teams/scope-target-team/channels/ids", "scope-member", params, body)
		assertChannelIDs(t, member, http.StatusOK, []string{"scope-public"})
	})
}

func invokeChannelScopeHandler(
	ctx context.Context,
	handler func(http.ResponseWriter, *http.Request),
	method, path, actorID string,
	params map[string]string,
	body string,
) *httptest.ResponseRecorder {
	routeContext := chi.NewRouteContext()
	for key, value := range params {
		routeContext.URLParams.Add(key, value)
	}
	requestContext := context.WithValue(ctx, chi.RouteCtxKey, routeContext)
	requestContext = context.WithValue(requestContext, userIDKey, actorID)
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(requestContext)
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func assertChannelScopeError(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, hiddenID string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, wantStatus, response.Body.String())
	}
	if strings.Contains(response.Body.String(), hiddenID) {
		t.Fatalf("hidden channel id %q leaked in response: %s", hiddenID, response.Body.String())
	}
}

func assertChannelIDs(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantIDs []string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, wantStatus, response.Body.String())
	}
	var got []channels.Channel
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode channels: %v; body=%s", err, response.Body.String())
	}
	gotIDs := make([]string, 0, len(got))
	for _, channel := range got {
		gotIDs = append(gotIDs, channel.ID)
	}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("channel ids = %v, want %v", gotIDs, wantIDs)
	}
}

func assertChannelID(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantID string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, wantStatus, response.Body.String())
	}
	var got channels.Channel
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode channel: %v", err)
	}
	if got.ID != wantID {
		t.Fatalf("channel id = %q, want %q", got.ID, wantID)
	}
}
