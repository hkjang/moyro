// Package onboarding applies administrator-defined OIDC group mappings to
// Moyro account, team, and channel memberships. Synchronisation is deliberately
// additive: a transient omission in an identity-provider group claim never
// removes manually granted access.
package onboarding

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/store"
)

const (
	AccountRoleUser  = "user"
	AccountRoleAdmin = "admin"
	AccountRoleGuest = "guest"

	MembershipRoleMember = "member"
	MembershipRoleAdmin  = "admin"

	maxMappings = 100
	maxChannels = 100
)

var ErrInvalidMapping = errors.New("oidc group mapping is invalid")

// GroupMapping maps one exact (case-insensitive) provider group to additive
// local access. A blank TeamID is allowed only for an account-role-only map.
type GroupMapping struct {
	Group                    string   `json:"group"`
	AccountRole              string   `json:"account_role"`
	TeamID                   string   `json:"team_id,omitempty"`
	TeamRole                 string   `json:"team_role,omitempty"`
	ChannelIDs               []string `json:"channel_ids,omitempty"`
	ChannelRole              string   `json:"channel_role,omitempty"`
	GuestExpiresAfterSeconds int64    `json:"guest_expires_after_seconds,omitempty"`
	GuestFileDownload        bool     `json:"guest_file_download"`
}

type Service struct {
	db  *store.DB
	now func() time.Time
}

func New(db *store.DB) *Service { return &Service{db: db, now: time.Now} }

// ValidateMappings normalises and validates persisted configuration against
// current active teams/channels. The returned slice is safe to store.
func (s *Service) ValidateMappings(ctx context.Context, mappings []GroupMapping) ([]GroupMapping, error) {
	normalized, err := normalizeMappings(mappings)
	if err != nil {
		return nil, err
	}
	for index, mapping := range normalized {
		if mapping.TeamID == "" {
			continue
		}
		var teamExists bool
		if err := s.db.Pool.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM teams WHERE id=$1 AND delete_at=0)
		`, mapping.TeamID).Scan(&teamExists); err != nil {
			return nil, err
		}
		if !teamExists {
			return nil, fmt.Errorf("%w: mapping %d refers to an inactive team", ErrInvalidMapping, index+1)
		}
		if len(mapping.ChannelIDs) == 0 {
			continue
		}
		var channelCount int
		if err := s.db.Pool.QueryRow(ctx, `
			SELECT count(*) FROM channels
			WHERE id=ANY($1::text[]) AND team_id=$2 AND delete_at=0 AND type IN ('O','P')
		`, mapping.ChannelIDs, mapping.TeamID).Scan(&channelCount); err != nil {
			return nil, err
		}
		if channelCount != len(mapping.ChannelIDs) {
			return nil, fmt.Errorf("%w: mapping %d has a missing, archived, direct, or cross-team channel", ErrInvalidMapping, index+1)
		}
	}
	return normalized, nil
}

// Apply grants every mapping matched by the asserted groups. Its boolean
// reports whether a collaboration target (team/channel) was assigned, not
// merely whether an account-role-only mapping matched. This lets regular and
// admin accounts with role-only mappings still receive the default space.
// Existing memberships and higher roles are never removed.
func (s *Service) Apply(ctx context.Context, userID string, groups []string, mappings []GroupMapping, created bool) (bool, error) {
	validated, err := s.ValidateMappings(ctx, mappings)
	if err != nil {
		return false, err
	}
	plan := buildPlan(groups, validated)
	if !plan.matched {
		return false, nil
	}

	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var currentRoles string
	var guestExpiresAt int64
	var guestDownload bool
	if err := tx.QueryRow(ctx, `
		SELECT roles, guest_expires_at, guest_file_download
		FROM users WHERE id=$1 AND delete_at=0 FOR UPDATE
	`, userID).Scan(&currentRoles, &guestExpiresAt, &guestDownload); err != nil {
		return false, err
	}

	now := s.now()
	nextRoles, nextGuestExpiry, nextGuestDownload := mergeAccountAccess(
		currentRoles, guestExpiresAt, guestDownload, plan, created, now,
	)
	if _, err := tx.Exec(ctx, `
		UPDATE users
		SET roles=$2, guest_expires_at=$3, guest_file_download=$4, update_at=$5
		WHERE id=$1 AND delete_at=0
	`, userID, nextRoles, nextGuestExpiry, nextGuestDownload, now.UnixMilli()); err != nil {
		return false, err
	}

	teamIDs := sortedKeys(plan.teams)
	for _, teamID := range teamIDs {
		role := "team_user"
		if plan.teams[teamID] == MembershipRoleAdmin {
			role = "team_admin team_user"
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO team_members (team_id, user_id, roles, create_at)
			SELECT id, $2, $3, $4 FROM teams WHERE id=$1 AND delete_at=0
			ON CONFLICT (team_id, user_id) DO UPDATE SET roles = CASE
				WHEN team_members.roles LIKE '%team_admin%' OR EXCLUDED.roles LIKE '%team_admin%'
				THEN 'team_admin team_user' ELSE 'team_user' END
		`, teamID, userID, role, now.UnixMilli())
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() != 1 {
			return false, fmt.Errorf("%w: team %s became unavailable", ErrInvalidMapping, teamID)
		}
	}

	channelIDs := sortedKeys(plan.channels)
	for _, channelID := range channelIDs {
		role := "channel_user"
		if plan.channels[channelID] == MembershipRoleAdmin {
			role = "channel_admin channel_user"
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO channel_members (channel_id, user_id, roles, create_at)
			SELECT id, $2, $3, $4 FROM channels
			WHERE id=$1 AND delete_at=0 AND type IN ('O','P')
			ON CONFLICT (channel_id, user_id) DO UPDATE SET roles = CASE
				WHEN channel_members.roles LIKE '%channel_admin%' OR EXCLUDED.roles LIKE '%channel_admin%'
				THEN 'channel_admin channel_user' ELSE 'channel_user' END
		`, channelID, userID, role, now.UnixMilli())
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() != 1 {
			return false, fmt.Errorf("%w: channel %s became unavailable", ErrInvalidMapping, channelID)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return plan.hasCollaborationTarget(), nil
}

type accessPlan struct {
	matched           bool
	accountRole       string
	teams             map[string]string
	channels          map[string]string
	guestTTLSeconds   int64
	guestFileDownload bool
}

func (p accessPlan) hasCollaborationTarget() bool {
	return len(p.teams) > 0 || len(p.channels) > 0
}

func buildPlan(groups []string, mappings []GroupMapping) accessPlan {
	plan := accessPlan{teams: map[string]string{}, channels: map[string]string{}}
	asserted := map[string]struct{}{}
	for _, group := range groups {
		group = canonicalGroup(group)
		if group != "" {
			asserted[group] = struct{}{}
		}
	}
	for _, mapping := range mappings {
		if _, ok := asserted[canonicalGroup(mapping.Group)]; !ok {
			continue
		}
		plan.matched = true
		plan.accountRole = strongerAccountRole(plan.accountRole, mapping.AccountRole)
		if mapping.TeamID != "" {
			plan.teams[mapping.TeamID] = strongerMembershipRole(plan.teams[mapping.TeamID], mapping.TeamRole)
		}
		for _, channelID := range mapping.ChannelIDs {
			plan.channels[channelID] = strongerMembershipRole(plan.channels[channelID], mapping.ChannelRole)
		}
		if mapping.AccountRole == AccountRoleGuest {
			if mapping.GuestExpiresAfterSeconds > plan.guestTTLSeconds {
				plan.guestTTLSeconds = mapping.GuestExpiresAfterSeconds
			}
			plan.guestFileDownload = plan.guestFileDownload || mapping.GuestFileDownload
		}
	}
	return plan
}

func normalizeMappings(mappings []GroupMapping) ([]GroupMapping, error) {
	if len(mappings) > maxMappings {
		return nil, fmt.Errorf("%w: at most %d mappings are allowed", ErrInvalidMapping, maxMappings)
	}
	out := make([]GroupMapping, 0, len(mappings))
	seenGroups := map[string]struct{}{}
	for index, raw := range mappings {
		mapping := raw
		mapping.Group = strings.TrimSpace(mapping.Group)
		mapping.TeamID = strings.TrimSpace(mapping.TeamID)
		mapping.AccountRole = strings.ToLower(strings.TrimSpace(mapping.AccountRole))
		mapping.TeamRole = strings.ToLower(strings.TrimSpace(mapping.TeamRole))
		mapping.ChannelRole = strings.ToLower(strings.TrimSpace(mapping.ChannelRole))
		if mapping.Group == "" || len(mapping.Group) > 255 {
			return nil, fmt.Errorf("%w: mapping %d requires a group of at most 255 characters", ErrInvalidMapping, index+1)
		}
		canonical := canonicalGroup(mapping.Group)
		if _, exists := seenGroups[canonical]; exists {
			return nil, fmt.Errorf("%w: group %q is mapped more than once", ErrInvalidMapping, mapping.Group)
		}
		seenGroups[canonical] = struct{}{}
		if mapping.AccountRole == "" {
			mapping.AccountRole = AccountRoleUser
		}
		if mapping.AccountRole != AccountRoleUser && mapping.AccountRole != AccountRoleAdmin && mapping.AccountRole != AccountRoleGuest {
			return nil, fmt.Errorf("%w: mapping %d has an unsupported account role", ErrInvalidMapping, index+1)
		}
		if mapping.TeamRole == "" {
			mapping.TeamRole = MembershipRoleMember
		}
		if mapping.TeamRole != MembershipRoleMember && mapping.TeamRole != MembershipRoleAdmin {
			return nil, fmt.Errorf("%w: mapping %d has an unsupported team role", ErrInvalidMapping, index+1)
		}
		if mapping.ChannelRole == "" {
			mapping.ChannelRole = MembershipRoleMember
		}
		if mapping.ChannelRole != MembershipRoleMember && mapping.ChannelRole != MembershipRoleAdmin {
			return nil, fmt.Errorf("%w: mapping %d has an unsupported channel role", ErrInvalidMapping, index+1)
		}
		mapping.ChannelIDs = uniqueSorted(mapping.ChannelIDs)
		if len(mapping.ChannelIDs) > maxChannels {
			return nil, fmt.Errorf("%w: mapping %d has too many channels", ErrInvalidMapping, index+1)
		}
		if len(mapping.ChannelIDs) > 0 && mapping.TeamID == "" {
			return nil, fmt.Errorf("%w: mapping %d channels require a team", ErrInvalidMapping, index+1)
		}
		if mapping.AccountRole == AccountRoleGuest {
			if mapping.TeamID == "" || len(mapping.ChannelIDs) == 0 {
				return nil, fmt.Errorf("%w: guest mapping %d requires a team and restricted channels", ErrInvalidMapping, index+1)
			}
			if mapping.TeamRole != MembershipRoleMember || mapping.ChannelRole != MembershipRoleMember {
				return nil, fmt.Errorf("%w: guest mapping %d cannot grant team or channel admin roles", ErrInvalidMapping, index+1)
			}
			if mapping.GuestExpiresAfterSeconds == 0 {
				mapping.GuestExpiresAfterSeconds = int64((30 * 24 * time.Hour) / time.Second)
			}
			if mapping.GuestExpiresAfterSeconds < int64(time.Hour/time.Second) || mapping.GuestExpiresAfterSeconds > int64((365*24*time.Hour)/time.Second) {
				return nil, fmt.Errorf("%w: guest mapping %d expiry must be between one hour and one year", ErrInvalidMapping, index+1)
			}
		} else {
			mapping.GuestExpiresAfterSeconds = 0
			mapping.GuestFileDownload = true
		}
		out = append(out, mapping)
	}
	return out, nil
}

func mergeAccountAccess(currentRoles string, currentExpiry int64, currentDownload bool, plan accessPlan, created bool, now time.Time) (string, int64, bool) {
	tokens := roleTokens(currentRoles)
	if plan.accountRole == AccountRoleAdmin {
		delete(tokens, "system_guest")
		tokens["system_user"] = struct{}{}
		tokens["system_admin"] = struct{}{}
		return joinRoleTokens(tokens), 0, true
	}
	if plan.accountRole == AccountRoleUser {
		delete(tokens, "system_guest")
		tokens["system_user"] = struct{}{}
		return joinRoleTokens(tokens), 0, true
	}
	_, alreadyGuest := tokens["system_guest"]
	if plan.accountRole == AccountRoleGuest && (created || alreadyGuest) {
		delete(tokens, "system_user")
		if created {
			delete(tokens, "system_admin")
		}
		tokens["system_guest"] = struct{}{}
		expiry := now.Add(time.Duration(plan.guestTTLSeconds) * time.Second).UnixMilli()
		if currentExpiry > expiry {
			expiry = currentExpiry
		}
		download := plan.guestFileDownload
		if !created {
			download = currentDownload || plan.guestFileDownload
		}
		return joinRoleTokens(tokens), expiry, download
	}
	return joinRoleTokens(tokens), currentExpiry, currentDownload
}

func canonicalGroup(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func strongerAccountRole(left, right string) string {
	rank := map[string]int{"": 0, AccountRoleGuest: 1, AccountRoleUser: 2, AccountRoleAdmin: 3}
	if rank[right] > rank[left] {
		return right
	}
	return left
}

func strongerMembershipRole(left, right string) string {
	if left == MembershipRoleAdmin || right == MembershipRoleAdmin {
		return MembershipRoleAdmin
	}
	return MembershipRoleMember
}

func roleTokens(value string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range strings.Fields(value) {
		out[token] = struct{}{}
	}
	return out
}

func joinRoleTokens(tokens map[string]struct{}) string {
	ordered := sortedKeys(tokens)
	return strings.Join(ordered, " ")
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
