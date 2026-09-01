// Package automations implements bounded, offline-safe message rules. A rule
// is expanded into durable action runs in the same transaction that stores the
// triggering post, so a committed message cannot silently lose its automation.
package automations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/workitems"
	"github.com/jackc/pgx/v5"
)

const (
	MatchContains   = "contains"
	MatchStartsWith = "starts_with"

	ActionTask     = "task"
	ActionDecision = "decision"
	ActionReminder = "reminder"

	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusRetry      = "retry"
	StatusSucceeded  = "succeeded"
	StatusDead       = "dead"
	StatusCancelled  = "cancelled"

	maxRuleNameRunes  = 120
	maxMatchRunes     = 200
	maxActionsPerRule = 5
	maxOffsetMinutes  = 366 * 24 * 60
)

var (
	ErrInvalid          = errors.New("invalid automation rule")
	ErrNotFound         = errors.New("automation rule not found")
	ErrForbidden        = errors.New("automation rule operation forbidden")
	ErrRevisionConflict = errors.New("automation rule revision conflict")
)

type ActionConfig struct {
	Title               string `json:"title,omitempty"`
	Description         string `json:"description,omitempty"`
	AssigneeID          string `json:"assignee_id,omitempty"`
	DueOffsetMinutes    int    `json:"due_offset_minutes,omitempty"`
	Priority            string `json:"priority,omitempty"`
	RecurrenceUnit      string `json:"recurrence_unit,omitempty"`
	RecurrenceInterval  int    `json:"recurrence_interval,omitempty"`
	InitialStatus       string `json:"initial_status,omitempty"`
	ReviewerID          string `json:"reviewer_id,omitempty"`
	RemindOffsetMinutes int    `json:"remind_offset_minutes,omitempty"`
}

type Action struct {
	ID       string       `json:"id"`
	Position int          `json:"position"`
	Type     string       `json:"type"`
	Config   ActionConfig `json:"config"`
}

type Rule struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	CreatedBy  string   `json:"created_by"`
	TeamID     string   `json:"team_id,omitempty"`
	ChannelID  string   `json:"channel_id"`
	Enabled    bool     `json:"enabled"`
	MatchType  string   `json:"match_type"`
	MatchValue string   `json:"match_value"`
	Revision   int64    `json:"revision"`
	Actions    []Action `json:"actions"`
	CreateAt   int64    `json:"create_at"`
	UpdateAt   int64    `json:"update_at"`
	DeleteAt   int64    `json:"delete_at"`
}

type SaveInput struct {
	Name       string
	ChannelID  string
	Enabled    bool
	MatchType  string
	MatchValue string
	Revision   int64
	Actions    []Action
}

type Run struct {
	ID            string          `json:"id"`
	RuleID        string          `json:"rule_id"`
	ActionID      string          `json:"action_id"`
	PostID        string          `json:"post_id"`
	ActorID       string          `json:"actor_id"`
	ActionType    string          `json:"action_type"`
	ActionConfig  json.RawMessage `json:"-"`
	Status        string          `json:"status"`
	AttemptCount  int             `json:"attempt_count"`
	NextAttemptAt int64           `json:"next_attempt_at"`
	ClaimedAt     int64           `json:"claimed_at,omitempty"`
	LeaseUntil    int64           `json:"lease_until,omitempty"`
	ClaimToken    string          `json:"-"`
	ResultType    string          `json:"result_type,omitempty"`
	ResultID      string          `json:"result_id,omitempty"`
	LastErrorCode string          `json:"last_error_code,omitempty"`
	LastErrorText string          `json:"last_error_text,omitempty"`
	CreateAt      int64           `json:"create_at"`
	UpdateAt      int64           `json:"update_at"`
	CompletedAt   int64           `json:"completed_at,omitempty"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

func validText(value string, maxRunes int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func normalizeSaveInput(input SaveInput) (SaveInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.MatchType = strings.TrimSpace(input.MatchType)
	input.MatchValue = strings.TrimSpace(input.MatchValue)
	if !validText(input.Name, maxRuleNameRunes) || !validText(input.MatchValue, maxMatchRunes) || input.ChannelID == "" {
		return SaveInput{}, ErrInvalid
	}
	if input.MatchType != MatchContains && input.MatchType != MatchStartsWith {
		return SaveInput{}, ErrInvalid
	}
	if len(input.Actions) < 1 || len(input.Actions) > maxActionsPerRule {
		return SaveInput{}, ErrInvalid
	}
	actionIDs := make(map[string]struct{}, len(input.Actions))
	for index := range input.Actions {
		input.Actions[index].Position = index
		if input.Actions[index].ID == "" {
			input.Actions[index].ID = uuid.NewString()
		}
		input.Actions[index].ID = strings.TrimSpace(input.Actions[index].ID)
		if !validText(input.Actions[index].ID, 128) {
			return SaveInput{}, ErrInvalid
		}
		if _, duplicate := actionIDs[input.Actions[index].ID]; duplicate {
			return SaveInput{}, ErrInvalid
		}
		actionIDs[input.Actions[index].ID] = struct{}{}
		if err := normalizeAction(&input.Actions[index]); err != nil {
			return SaveInput{}, err
		}
	}
	return input, nil
}

func normalizeAction(action *Action) error {
	action.Type = strings.TrimSpace(action.Type)
	action.Config.Title = strings.TrimSpace(action.Config.Title)
	action.Config.Description = strings.TrimSpace(action.Config.Description)
	action.Config.AssigneeID = strings.TrimSpace(action.Config.AssigneeID)
	action.Config.ReviewerID = strings.TrimSpace(action.Config.ReviewerID)
	action.Config.Priority = strings.TrimSpace(action.Config.Priority)
	action.Config.RecurrenceUnit = strings.TrimSpace(action.Config.RecurrenceUnit)
	action.Config.InitialStatus = strings.TrimSpace(action.Config.InitialStatus)
	if !validOptionalActionText(action.Config.Title, false, 240) ||
		!validOptionalActionText(action.Config.Description, true, 10_000) {
		return ErrInvalid
	}
	switch action.Type {
	case ActionTask:
		if action.Config.Priority == "" {
			action.Config.Priority = workitems.PriorityNormal
		}
		if action.Config.RecurrenceUnit == "" {
			action.Config.RecurrenceUnit = workitems.RecurrenceNone
		}
		if action.Config.Priority != workitems.PriorityLow && action.Config.Priority != workitems.PriorityNormal &&
			action.Config.Priority != workitems.PriorityHigh && action.Config.Priority != workitems.PriorityUrgent {
			return ErrInvalid
		}
		if (action.Config.RecurrenceUnit == workitems.RecurrenceNone && action.Config.RecurrenceInterval != 0) ||
			((action.Config.RecurrenceUnit == workitems.RecurrenceDaily || action.Config.RecurrenceUnit == workitems.RecurrenceWeekly ||
				action.Config.RecurrenceUnit == workitems.RecurrenceMonthly) &&
				(action.Config.RecurrenceInterval < 1 || action.Config.RecurrenceInterval > 365)) ||
			(action.Config.RecurrenceUnit != workitems.RecurrenceNone && action.Config.RecurrenceUnit != workitems.RecurrenceDaily &&
				action.Config.RecurrenceUnit != workitems.RecurrenceWeekly && action.Config.RecurrenceUnit != workitems.RecurrenceMonthly) {
			return ErrInvalid
		}
		if action.Config.DueOffsetMinutes < 0 || action.Config.DueOffsetMinutes > maxOffsetMinutes {
			return ErrInvalid
		}
		if action.Config.RecurrenceUnit != workitems.RecurrenceNone && action.Config.DueOffsetMinutes == 0 {
			return ErrInvalid
		}
		if action.Config.InitialStatus != "" || action.Config.ReviewerID != "" || action.Config.RemindOffsetMinutes != 0 {
			return ErrInvalid
		}
	case ActionDecision:
		if action.Config.InitialStatus == "" {
			action.Config.InitialStatus = workitems.StatusProposed
		}
		if action.Config.InitialStatus != workitems.StatusProposed && action.Config.InitialStatus != workitems.StatusRecorded {
			return ErrInvalid
		}
		if action.Config.AssigneeID != "" || action.Config.DueOffsetMinutes != 0 || action.Config.RemindOffsetMinutes != 0 ||
			action.Config.Priority != "" || action.Config.RecurrenceUnit != "" || action.Config.RecurrenceInterval != 0 {
			return ErrInvalid
		}
	case ActionReminder:
		if action.Config.RemindOffsetMinutes < 1 || action.Config.RemindOffsetMinutes > maxOffsetMinutes {
			return ErrInvalid
		}
		if action.Config.Title != "" || action.Config.Description != "" || action.Config.AssigneeID != "" || action.Config.ReviewerID != "" ||
			action.Config.DueOffsetMinutes != 0 || action.Config.InitialStatus != "" || action.Config.Priority != "" ||
			action.Config.RecurrenceUnit != "" || action.Config.RecurrenceInterval != 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validOptionalActionText(value string, allowLayout bool, maxRunes int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, char := range value {
		if !unicode.IsControl(char) {
			continue
		}
		if allowLayout && (char == '\n' || char == '\r' || char == '\t') {
			continue
		}
		return false
	}
	return true
}

func validateActionMembers(ctx context.Context, tx pgx.Tx, channelID, ownerID string, actions []Action) error {
	for _, action := range actions {
		candidate := action.Config.AssigneeID
		if action.Type == ActionDecision {
			candidate = action.Config.ReviewerID
		}
		if candidate == "" {
			continue
		}
		var member bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM channel_members cm
				JOIN users u ON u.id=cm.user_id AND u.delete_at=0
				  AND (
					NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(u.roles), E'\\s+')))
					OR u.guest_expires_at>$3
				  )
				WHERE cm.channel_id=$1 AND cm.user_id=$2
			)
		`, channelID, candidate, time.Now().UnixMilli()).Scan(&member); err != nil {
			return err
		}
		if !member {
			return ErrForbidden
		}
	}
	_ = ownerID
	return nil
}

func resolveRuleScope(ctx context.Context, tx pgx.Tx, principal rbac.Principal, channelID string) (string, error) {
	var teamID string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(c.team_id,'')
		FROM channels c
		JOIN channel_members cm ON cm.channel_id=c.id AND cm.user_id=$2
		JOIN users u ON u.id=cm.user_id AND u.delete_at=0
		  AND (
			NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(u.roles), E'\\s+')))
			OR u.guest_expires_at>$3
		  )
		WHERE c.id=$1 AND c.delete_at=0
		FOR SHARE OF c, cm, u
	`, channelID, principal.UserID, time.Now().UnixMilli()).Scan(&teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	if err != nil {
		return "", err
	}
	if !rbac.ResourceConstraintsFor(principal).Allows(teamID, channelID) {
		return "", ErrForbidden
	}
	return teamID, nil
}

func insertActions(ctx context.Context, tx pgx.Tx, ruleID string, actions []Action, now int64) error {
	for _, action := range actions {
		raw, err := json.Marshal(action.Config)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO automation_rule_actions (id, rule_id, position, action_type, config, create_at)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, action.ID, ruleID, action.Position, action.Type, raw, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) CreateForPrincipal(ctx context.Context, principal rbac.Principal, input SaveInput) (*Rule, error) {
	input, err := normalizeSaveInput(input)
	if err != nil || strings.TrimSpace(principal.UserID) == "" {
		return nil, ErrInvalid
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	teamID, err := resolveRuleScope(ctx, tx, principal, input.ChannelID)
	if err != nil {
		return nil, err
	}
	if err := validateActionMembers(ctx, tx, input.ChannelID, principal.UserID, input.Actions); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	rule := &Rule{
		ID: uuid.NewString(), Name: input.Name, CreatedBy: principal.UserID,
		TeamID: teamID, ChannelID: input.ChannelID, Enabled: input.Enabled,
		MatchType: input.MatchType, MatchValue: input.MatchValue, Revision: 1,
		Actions: input.Actions, CreateAt: now, UpdateAt: now,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO automation_rules
			(id, name, created_by, team_id, channel_id, enabled, match_type, match_value, revision, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,$9,$9)
	`, rule.ID, rule.Name, rule.CreatedBy, nilIfEmpty(rule.TeamID), rule.ChannelID, rule.Enabled, rule.MatchType, rule.MatchValue, now); err != nil {
		return nil, err
	}
	if err := insertActions(ctx, tx, rule.ID, rule.Actions, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return rule, nil
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Service) UpdateForPrincipal(ctx context.Context, principal rbac.Principal, ruleID string, input SaveInput) (*Rule, error) {
	ruleID = strings.TrimSpace(ruleID)
	input, err := normalizeSaveInput(input)
	if err != nil || ruleID == "" || input.Revision < 1 {
		return nil, ErrInvalid
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	teamID, err := resolveRuleScope(ctx, tx, principal, input.ChannelID)
	if err != nil {
		return nil, err
	}
	if err := validateActionMembers(ctx, tx, input.ChannelID, principal.UserID, input.Actions); err != nil {
		return nil, err
	}
	constraints := rbac.ResourceConstraintsFor(principal)
	now := time.Now().UnixMilli()
	result, err := tx.Exec(ctx, `
		UPDATE automation_rules
		SET name=$1, team_id=$2, channel_id=$3, enabled=$4, match_type=$5, match_value=$6,
		    revision=revision+1, update_at=$7
		WHERE id=$8 AND created_by=$9 AND delete_at=0 AND revision=$10
		  AND (NOT $11::boolean OR cardinality($12::text[])=0 OR team_id=ANY($12::text[]))
		  AND (NOT $11::boolean OR cardinality($13::text[])=0 OR channel_id=ANY($13::text[]))
	`, input.Name, nilIfEmpty(teamID), input.ChannelID, input.Enabled, input.MatchType, input.MatchValue,
		now, ruleID, principal.UserID, input.Revision, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM automation_rules
				WHERE id=$1 AND created_by=$2 AND delete_at=0
				  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR team_id=ANY($4::text[]))
				  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR channel_id=ANY($5::text[]))
			)
		`, ruleID, principal.UserID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, ErrRevisionConflict
		}
		return nil, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM automation_rule_actions WHERE rule_id=$1`, ruleID); err != nil {
		return nil, err
	}
	if err := insertActions(ctx, tx, ruleID, input.Actions, now); err != nil {
		return nil, err
	}
	if !input.Enabled {
		if _, err := tx.Exec(ctx, `
			UPDATE automation_runs SET status='cancelled', update_at=$1, completed_at=$1,
			       claimed_at=0, lease_until=0, claim_token=''
			WHERE rule_id=$2 AND status IN ('pending','retry')
		`, now, ruleID); err != nil {
			return nil, err
		}
	}
	rule := &Rule{
		ID: ruleID, Name: input.Name, CreatedBy: principal.UserID, TeamID: teamID,
		ChannelID: input.ChannelID, Enabled: input.Enabled, MatchType: input.MatchType,
		MatchValue: input.MatchValue, Revision: input.Revision + 1, Actions: input.Actions,
		UpdateAt: now,
	}
	if err := tx.QueryRow(ctx, `SELECT create_at FROM automation_rules WHERE id=$1`, ruleID).Scan(&rule.CreateAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return rule, nil
}

func scanRule(row pgx.Row) (*Rule, error) {
	var rule Rule
	err := row.Scan(&rule.ID, &rule.Name, &rule.CreatedBy, &rule.TeamID, &rule.ChannelID,
		&rule.Enabled, &rule.MatchType, &rule.MatchValue, &rule.Revision,
		&rule.CreateAt, &rule.UpdateAt, &rule.DeleteAt)
	return &rule, err
}

func (s *Service) ListForPrincipal(ctx context.Context, principal rbac.Principal) ([]Rule, error) {
	constraints := rbac.ResourceConstraintsFor(principal)
	rows, err := s.db.Pool.Query(ctx, `
		SELECT r.id, r.name, r.created_by, COALESCE(r.team_id,''), r.channel_id,
		       r.enabled, r.match_type, r.match_value, r.revision, r.create_at, r.update_at, r.delete_at
		FROM automation_rules r
		JOIN channels c ON c.id=r.channel_id AND c.delete_at=0
		JOIN channel_members cm ON cm.channel_id=r.channel_id AND cm.user_id=$1
		JOIN users owner ON owner.id=cm.user_id AND owner.delete_at=0
		  AND (
			NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(owner.roles), E'\\s+')))
			OR owner.guest_expires_at>$5
		  )
		WHERE r.created_by=$1 AND r.delete_at=0
		  AND (NOT $2::boolean OR cardinality($3::text[])=0 OR r.team_id=ANY($3::text[]))
		  AND (NOT $2::boolean OR cardinality($4::text[])=0 OR r.channel_id=ANY($4::text[]))
		ORDER BY r.update_at DESC, r.id DESC
		LIMIT 200
	`, principal.UserID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []Rule{}
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, *rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range rules {
		actions, err := s.actionsForRule(ctx, rules[index].ID)
		if err != nil {
			return nil, err
		}
		rules[index].Actions = actions
	}
	return rules, nil
}

func (s *Service) actionsForRule(ctx context.Context, ruleID string) ([]Action, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, position, action_type, config
		FROM automation_rule_actions WHERE rule_id=$1 ORDER BY position ASC
	`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actions := []Action{}
	for rows.Next() {
		var action Action
		var raw []byte
		if err := rows.Scan(&action.ID, &action.Position, &action.Type, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &action.Config); err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

func (s *Service) DeleteForPrincipal(ctx context.Context, principal rbac.Principal, ruleID string) error {
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return ErrInvalid
	}
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	now := time.Now().UnixMilli()
	constraints := rbac.ResourceConstraintsFor(principal)
	result, err := tx.Exec(ctx, `
		UPDATE automation_rules SET enabled=FALSE, delete_at=$1, update_at=$1, revision=revision+1
		WHERE id=$2 AND created_by=$3 AND delete_at=0
		  AND (NOT $4::boolean OR cardinality($5::text[])=0 OR team_id=ANY($5::text[]))
		  AND (NOT $4::boolean OR cardinality($6::text[])=0 OR channel_id=ANY($6::text[]))
		  AND EXISTS (
			SELECT 1 FROM users owner
			WHERE owner.id=$3 AND owner.delete_at=0
			  AND (
				NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(owner.roles), E'\\s+')))
				OR owner.guest_expires_at>$7
			  )
		  )
	`, now, ruleID, principal.UserID, constraints.Restricted, constraints.TeamIDs, constraints.ChannelIDs, now)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err = tx.Exec(ctx, `
		UPDATE automation_runs SET status='cancelled', update_at=$1, completed_at=$1,
		       claimed_at=0, lease_until=0, claim_token=''
		WHERE rule_id=$2 AND status IN ('pending','retry')
	`, now, ruleID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func ruleMatches(matchType, matchValue, message string) bool {
	needle := strings.ToLower(strings.TrimSpace(matchValue))
	haystack := strings.ToLower(strings.TrimSpace(message))
	if needle == "" || haystack == "" {
		return false
	}
	if matchType == MatchStartsWith {
		return strings.HasPrefix(haystack, needle)
	}
	return matchType == MatchContains && strings.Contains(haystack, needle)
}

// EnqueuePost is called by posts.Service after INSERT and before COMMIT.
func (s *Service) EnqueuePost(ctx context.Context, tx pgx.Tx, post *posts.Post) error {
	if post == nil || strings.TrimSpace(post.ID) == "" {
		return ErrInvalid
	}
	rows, err := tx.Query(ctx, `
		SELECT r.id, r.created_by, r.match_type, r.match_value,
		       action.id, action.action_type, action.config
		FROM automation_rules r
		JOIN automation_rule_actions action ON action.rule_id=r.id
		JOIN channel_members owner_member
		  ON owner_member.channel_id=r.channel_id AND owner_member.user_id=r.created_by
		JOIN users owner ON owner.id=r.created_by AND owner.delete_at=0
		  AND (
			NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(owner.roles), E'\\s+')))
			OR owner.guest_expires_at>$2
		  )
		WHERE r.channel_id=$1 AND r.enabled=TRUE AND r.delete_at=0 AND r.create_at<=$2
		ORDER BY r.id, action.position
		FOR SHARE OF r, action, owner_member, owner
	`, post.ChannelID, post.CreateAt)
	if err != nil {
		return err
	}
	type candidate struct {
		ruleID, actorID, matchType, matchValue, actionID, actionType string
		config                                                       []byte
	}
	candidates := []candidate{}
	for rows.Next() {
		var candidate candidate
		if err := rows.Scan(&candidate.ruleID, &candidate.actorID, &candidate.matchType, &candidate.matchValue,
			&candidate.actionID, &candidate.actionType, &candidate.config); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, candidate := range candidates {
		if !ruleMatches(candidate.matchType, candidate.matchValue, post.Message) {
			continue
		}
		now := post.CreateAt
		if _, err := tx.Exec(ctx, `
			INSERT INTO automation_runs (
				id, rule_id, action_id, post_id, actor_id, action_type, action_config,
				status, next_attempt_at, create_at, update_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',$8,$8,$8)
			ON CONFLICT (rule_id, action_id, post_id) DO NOTHING
		`, deterministicRunID(candidate.ruleID, candidate.actionID, post.ID), candidate.ruleID, candidate.actionID, post.ID, candidate.actorID,
			candidate.actionType, candidate.config, now); err != nil {
			return err
		}
	}
	return nil
}
