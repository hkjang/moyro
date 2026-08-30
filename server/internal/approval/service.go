// Package approval implements the optional team-lead review workflow. The
// absence of an enabled matching policy is a first-class direct-execution
// result: no request or decision row is created in that case.
package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

var (
	ErrNotFound       = errors.New("approval request not found")
	ErrForbidden      = errors.New("approval review is not permitted")
	ErrSelfReview     = errors.New("self approval is forbidden")
	ErrAlreadyDecided = errors.New("approval request is already decided")
	ErrInvalid        = errors.New("invalid approval request")
)

type Policy struct {
	ID                  string          `json:"id"`
	ScopeType           string          `json:"scope_type"`
	ScopeID             string          `json:"scope_id"`
	ActionType          string          `json:"action_type"`
	Enabled             bool            `json:"enabled"`
	ReviewerPermission  string          `json:"reviewer_permission"`
	ApprovalsRequired   int             `json:"approvals_required"`
	ForbidSelfApproval  bool            `json:"forbid_self_approval"`
	ExpiresAfterSeconds int64           `json:"expires_after_seconds"`
	Config              json.RawMessage `json:"config"`
	Revision            int64           `json:"revision"`
	UpdatedBy           string          `json:"updated_by"`
	UpdateAt            int64           `json:"update_at"`
}

type Request struct {
	ID             string          `json:"id"`
	PolicyID       string          `json:"policy_id"`
	ActionType     string          `json:"action_type"`
	RequesterID    string          `json:"requester_id"`
	TeamID         string          `json:"team_id"`
	ResourceType   string          `json:"resource_type"`
	ResourceID     string          `json:"resource_id"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Status         string          `json:"status"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	CreateAt       int64           `json:"create_at"`
	UpdateAt       int64           `json:"update_at"`
	DecidedAt      int64           `json:"decided_at"`
	ExecutedAt     int64           `json:"executed_at"`
	ExpiresAt      int64           `json:"expires_at"`
}

type Decision struct {
	RequestID  string `json:"request_id"`
	ReviewerID string `json:"reviewer_id"`
	Decision   string `json:"decision"`
	Reason     string `json:"reason"`
	CreateAt   int64  `json:"create_at"`
}

type Submission struct {
	ActionType     string
	RequesterID    string
	TeamID         string
	ResourceType   string
	ResourceID     string
	Payload        any
	IdempotencyKey string
}

type Result struct {
	ApprovalRequired bool     `json:"approval_required"`
	Request          *Request `json:"request,omitempty"`
}

// ReviewAuthorizer is shared with the configurable RBAC layer. Implementations
// decide whether reviewerID has policy.ReviewerPermission in the team scope.
type ReviewAuthorizer func(context.Context, string, *Policy, *Request) (bool, error)

type Service struct {
	db        *store.DB
	canReview ReviewAuthorizer
	activity  activityevents.Emitter
}

// SetActivityEmitter enables durable, user-scoped approval inbox events while
// preserving the established constructor used by tests and other adapters.
func (s *Service) SetActivityEmitter(emitter activityevents.Emitter) {
	s.activity = emitter
}

func New(db *store.DB, canReview ReviewAuthorizer) *Service {
	service := &Service{db: db, canReview: canReview}
	if service.canReview == nil {
		service.canReview = service.defaultCanReview
	}
	return service
}

// CanReviewRequest exposes the same policy/RBAC decision used by Decide and
// reviewer request lists. Activity inbox reads use it to make a previously
// emitted approval_review row disappear as soon as reviewer authority is
// revoked. Missing or malformed references fail closed without disclosing
// whether the request once existed.
func (s *Service) CanReviewRequest(ctx context.Context, reviewerID, requestID string) (bool, error) {
	reviewerID = strings.TrimSpace(reviewerID)
	requestID = strings.TrimSpace(requestID)
	if reviewerID == "" || requestID == "" {
		return false, nil
	}
	request, err := s.Get(ctx, requestID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	policy, err := s.policyByID(ctx, request.PolicyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if policy.ForbidSelfApproval && request.RequesterID == reviewerID {
		return false, nil
	}
	return s.canReview(ctx, reviewerID, policy, request)
}

func (s *Service) AnyEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM approval_policies WHERE enabled)`).Scan(&enabled)
	return enabled, err
}

func (s *Service) Required(ctx context.Context, actionType, teamID string) (bool, error) {
	_, err := s.matchPolicy(ctx, strings.TrimSpace(actionType), strings.TrimSpace(teamID))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) Submit(ctx context.Context, input Submission) (*Result, error) {
	input.ActionType = strings.TrimSpace(input.ActionType)
	input.RequesterID = strings.TrimSpace(input.RequesterID)
	if input.ActionType == "" || input.RequesterID == "" {
		return nil, ErrInvalid
	}
	policy, err := s.matchPolicy(ctx, input.ActionType, input.TeamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return &Result{ApprovalRequired: false}, nil
	}
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || string(payload) == "null" {
		payload = []byte(`{}`)
	}
	now := time.Now().UnixMilli()
	expiresAt := int64(0)
	if policy.ExpiresAfterSeconds > 0 {
		expiresAt = now + policy.ExpiresAfterSeconds*1000
	}
	request := &Request{
		ID: uuid.NewString(), PolicyID: policy.ID, ActionType: input.ActionType,
		RequesterID: input.RequesterID, TeamID: input.TeamID,
		ResourceType: input.ResourceType, ResourceID: input.ResourceID,
		Payload: payload, Status: "pending", IdempotencyKey: strings.TrimSpace(input.IdempotencyKey),
		CreateAt: now, UpdateAt: now, ExpiresAt: expiresAt,
	}

	if request.IdempotencyKey != "" {
		existing, lookupErr := s.byIdempotency(ctx, input.RequesterID, input.ActionType, request.IdempotencyKey)
		if lookupErr == nil {
			return &Result{ApprovalRequired: true, Request: existing}, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return nil, lookupErr
		}
	}
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO approval_requests (
			id, policy_id, action_type, requester_id, team_id,
			resource_type, resource_id, payload, status, idempotency_key,
			create_at, update_at, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$10,$11)
	`, request.ID, request.PolicyID, request.ActionType, request.RequesterID, nullable(request.TeamID),
		request.ResourceType, request.ResourceID, request.Payload, nullable(request.IdempotencyKey), now, expiresAt)
	if err != nil {
		if request.IdempotencyKey != "" {
			if existing, lookupErr := s.byIdempotency(ctx, input.RequesterID, input.ActionType, request.IdempotencyKey); lookupErr == nil {
				return &Result{ApprovalRequired: true, Request: existing}, nil
			}
		}
		return nil, err
	}
	s.emitSubmitted(ctx, policy, request)
	return &Result{ApprovalRequired: true, Request: request}, nil
}

func (s *Service) Decide(ctx context.Context, requestID, reviewerID, decision, reason string) (*Request, error) {
	decision = strings.ToLower(strings.TrimSpace(decision))
	if decision != "approve" && decision != "reject" {
		return nil, ErrInvalid
	}
	tx, err := s.db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	request, policy, err := requestAndPolicyForUpdate(ctx, tx, requestID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if request.Status != "pending" {
		return nil, ErrAlreadyDecided
	}
	if request.ExpiresAt > 0 && request.ExpiresAt <= time.Now().UnixMilli() {
		now := time.Now().UnixMilli()
		_, _ = tx.Exec(ctx, `UPDATE approval_requests SET status='expired', update_at=$2, decided_at=$2 WHERE id=$1`, request.ID, now)
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		request.Status, request.UpdateAt, request.DecidedAt = "expired", now, now
		s.emitDecided(ctx, request, reviewerID)
		return request, ErrAlreadyDecided
	}
	if policy.ForbidSelfApproval && request.RequesterID == reviewerID {
		return nil, ErrSelfReview
	}
	allowed, err := s.canReview(ctx, reviewerID, policy, request)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrForbidden
	}
	now := time.Now().UnixMilli()
	if decision == "reject" && strings.TrimSpace(reason) == "" {
		var config struct {
			RequireRejectionReason bool `json:"require_rejection_reason"`
		}
		if json.Unmarshal(policy.Config, &config) == nil && config.RequireRejectionReason {
			return nil, ErrInvalid
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO approval_decisions (request_id, reviewer_id, decision, reason, create_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (request_id, reviewer_id) DO UPDATE
		SET decision=EXCLUDED.decision, reason=EXCLUDED.reason, create_at=EXCLUDED.create_at
	`, request.ID, reviewerID, decision, strings.TrimSpace(reason), now)
	if err != nil {
		return nil, err
	}

	status := "pending"
	if decision == "reject" {
		status = "rejected"
	} else {
		var approvals int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM approval_decisions
			WHERE request_id=$1 AND decision='approve'
		`, request.ID).Scan(&approvals); err != nil {
			return nil, err
		}
		if approvals >= policy.ApprovalsRequired {
			status = "approved"
		}
	}
	decidedAt := int64(0)
	if status != "pending" {
		decidedAt = now
	}
	_, err = tx.Exec(ctx, `
		UPDATE approval_requests SET status=$2, update_at=$3, decided_at=$4 WHERE id=$1
	`, request.ID, status, now, decidedAt)
	if err != nil {
		return nil, err
	}
	if status == "approved" {
		_, err = tx.Exec(ctx, `
			INSERT INTO workflow_outbox (id, request_id, action_type, payload, status, attempts, available_at, create_at, update_at)
			VALUES ($1,$2,$3,$4,'pending',0,$5,$5,$5)
			ON CONFLICT (request_id) DO NOTHING
		`, uuid.NewString(), request.ID, request.ActionType, request.Payload, now)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	request.Status, request.UpdateAt, request.DecidedAt = status, now, decidedAt
	if status != "pending" {
		s.emitDecided(ctx, request, reviewerID)
	}
	return request, nil
}

func (s *Service) emitSubmitted(ctx context.Context, policy *Policy, request *Request) {
	if s.activity == nil || policy == nil || request == nil {
		return
	}
	preview := ToRequestView(request, "").Preview
	summary := preview.Title
	if len(preview.Changes) > 0 {
		summary = preview.Changes[0].After
	}
	_, _ = s.activity.Emit(ctx, activityevents.EmitInput{
		UserID: request.RequesterID, Type: activityevents.TypeApprovalRequested,
		DedupeKey: request.ID, ActorID: request.RequesterID, TeamID: request.TeamID,
		ChannelID:    approvalActivityChannelID(request),
		ResourceType: "approval", ResourceID: request.ID,
		Title: "승인 요청이 접수되었습니다", Summary: summary,
	})

	reviewers, err := s.reviewCandidates(ctx, policy, request)
	if err != nil {
		return
	}
	for _, reviewerID := range reviewers {
		if reviewerID == request.RequesterID {
			continue
		}
		_, _ = s.activity.Emit(ctx, activityevents.EmitInput{
			UserID: reviewerID, Type: activityevents.TypeApprovalRequested,
			DedupeKey: request.ID, ActorID: request.RequesterID, TeamID: request.TeamID,
			ChannelID:    approvalActivityChannelID(request),
			ResourceType: "approval_review", ResourceID: request.ID,
			Title: "검토할 승인 요청: " + preview.Title, Summary: summary,
		})
	}
}

func (s *Service) emitDecided(ctx context.Context, request *Request, reviewerID string) {
	if s.activity == nil || request == nil {
		return
	}
	statusLabel := map[string]string{
		"approved": "승인", "rejected": "반려", "expired": "만료",
	}[request.Status]
	if statusLabel == "" {
		statusLabel = "처리"
	}
	preview := ToRequestView(request, "").Preview
	_, _ = s.activity.Emit(ctx, activityevents.EmitInput{
		UserID: request.RequesterID, Type: activityevents.TypeDecided,
		DedupeKey: request.ID, ActorID: reviewerID, TeamID: request.TeamID,
		ChannelID:    approvalActivityChannelID(request),
		ResourceType: "approval", ResourceID: request.ID,
		Title: "승인 요청이 " + statusLabel + "되었습니다", Summary: preview.Title,
	})
}

func approvalActivityChannelID(request *Request) string {
	if request == nil || strings.TrimSpace(request.ResourceType) != "channel" {
		return ""
	}
	return strings.TrimSpace(request.ResourceID)
}

// reviewCandidates narrows the scan to users with a role that grants either
// the policy permission or manage_system, then applies the canonical
// canReview function (including configured reviewer-role constraints).
func (s *Service) reviewCandidates(ctx context.Context, policy *Policy, request *Request) ([]string, error) {
	rows, err := s.db.Pool.Query(ctx, `
		WITH permitted_roles AS (
			SELECT DISTINCT r.name
			FROM roles r
			JOIN role_permissions rp ON rp.role_id=r.id
			WHERE rp.permission_name=$1 OR rp.permission_name='manage_system'
		), candidates AS (
			SELECT u.id
			FROM users u, permitted_roles pr
			WHERE u.delete_at=0
			  AND pr.name = ANY(regexp_split_to_array(BTRIM(COALESCE(u.roles,'')), E'\\s+'))
			UNION
			SELECT tm.user_id
			FROM team_members tm, permitted_roles pr
			WHERE $2<>'' AND tm.team_id=$2
			  AND pr.name = ANY(regexp_split_to_array(BTRIM(COALESCE(tm.roles,'')), E'\\s+'))
		)
		SELECT DISTINCT id FROM candidates ORDER BY id
	`, policy.ReviewerPermission, request.TeamID)
	if err != nil {
		return nil, err
	}
	candidates := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	result := make([]string, 0, len(candidates))
	for _, userID := range candidates {
		if policy.ForbidSelfApproval && userID == request.RequesterID {
			continue
		}
		allowed, err := s.canReview(ctx, userID, policy, request)
		if err != nil {
			return nil, err
		}
		if allowed {
			result = append(result, userID)
		}
	}
	return result, nil
}

func (s *Service) List(ctx context.Context, actorID string, reviewer bool, status string, limit int) ([]Request, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	args := []any{}
	where := "TRUE"
	if !reviewer {
		args = append(args, actorID)
		where = fmt.Sprintf("r.requester_id=$%d", len(args))
	}
	if status != "" {
		args = append(args, status)
		where += fmt.Sprintf(" AND r.status=$%d", len(args))
	}
	queryLimit := limit
	if reviewer {
		queryLimit = min(limit*5, 1000)
	}
	args = append(args, queryLimit)
	limitPlaceholder := fmt.Sprintf("$%d", len(args))
	rows, err := s.db.Pool.Query(ctx, `
		SELECT r.id, r.policy_id, r.action_type, r.requester_id,
		       COALESCE(r.team_id,''), r.resource_type, r.resource_id,
		       r.payload, r.status, COALESCE(r.idempotency_key,''),
		       r.create_at, r.update_at, r.decided_at, r.executed_at, r.expires_at
		FROM approval_requests r WHERE `+where+`
		ORDER BY r.create_at DESC LIMIT `+limitPlaceholder+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		request, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		if reviewer {
			policy, err := s.policyByID(ctx, request.PolicyID)
			if err != nil {
				return nil, err
			}
			if policy.ForbidSelfApproval && request.RequesterID == actorID {
				continue
			}
			allowed, err := s.canReview(ctx, actorID, policy, request)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
		}
		out = append(out, *request)
		if len(out) == limit {
			break
		}
	}
	return out, rows.Err()
}

// Get returns request metadata for an internal authorization decision. Callers
// must authorize the returned team/resource before exposing it to a client.
func (s *Service) Get(ctx context.Context, requestID string) (*Request, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, ErrInvalid
	}
	request, err := scanRequest(s.db.Pool.QueryRow(ctx, `
		SELECT id, policy_id, action_type, requester_id, COALESCE(team_id,''),
		       resource_type, resource_id, payload, status, COALESCE(idempotency_key,''),
		       create_at, update_at, decided_at, executed_at, expires_at
		FROM approval_requests WHERE id=$1
	`, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return request, err
}

func (s *Service) policyByID(ctx context.Context, id string) (*Policy, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT id, scope_type, COALESCE(scope_id,''), action_type, enabled,
		       reviewer_permission, approvals_required, forbid_self_approval,
		       expires_after_seconds, config, revision, COALESCE(updated_by,''), update_at
		FROM approval_policies WHERE id=$1
	`, id)
	var policy Policy
	err := row.Scan(&policy.ID, &policy.ScopeType, &policy.ScopeID, &policy.ActionType,
		&policy.Enabled, &policy.ReviewerPermission, &policy.ApprovalsRequired,
		&policy.ForbidSelfApproval, &policy.ExpiresAfterSeconds, &policy.Config,
		&policy.Revision, &policy.UpdatedBy, &policy.UpdateAt)
	return &policy, err
}

func (s *Service) ListPolicies(ctx context.Context) ([]Policy, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, scope_type, COALESCE(scope_id,''), action_type, enabled,
		       reviewer_permission, approvals_required, forbid_self_approval,
		       expires_after_seconds, config, revision, COALESCE(updated_by,''), update_at
		FROM approval_policies ORDER BY scope_type, scope_id, action_type
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		var policy Policy
		if err := rows.Scan(&policy.ID, &policy.ScopeType, &policy.ScopeID, &policy.ActionType,
			&policy.Enabled, &policy.ReviewerPermission, &policy.ApprovalsRequired,
			&policy.ForbidSelfApproval, &policy.ExpiresAfterSeconds, &policy.Config,
			&policy.Revision, &policy.UpdatedBy, &policy.UpdateAt); err != nil {
			return nil, err
		}
		out = append(out, policy)
	}
	return out, rows.Err()
}

func (s *Service) UpsertPolicy(ctx context.Context, policy Policy, actorID string) (*Policy, error) {
	policy.ScopeType = strings.ToLower(strings.TrimSpace(policy.ScopeType))
	policy.ActionType = strings.TrimSpace(policy.ActionType)
	if policy.ScopeType != "system" && policy.ScopeType != "team" {
		return nil, ErrInvalid
	}
	if policy.ScopeType == "team" && policy.ScopeID == "" {
		return nil, ErrInvalid
	}
	if policy.ScopeType == "system" {
		policy.ScopeID = ""
	}
	if policy.ActionType == "" {
		return nil, ErrInvalid
	}
	if policy.ReviewerPermission == "" {
		policy.ReviewerPermission = "review_approval"
	}
	if policy.ApprovalsRequired <= 0 {
		policy.ApprovalsRequired = 1
	}
	if len(policy.Config) == 0 {
		policy.Config = json.RawMessage(`{}`)
	}
	if policy.ID == "" {
		policy.ID = uuid.NewString()
	}
	now := time.Now().UnixMilli()
	err := s.db.Pool.QueryRow(ctx, `
		INSERT INTO approval_policies (
			id, scope_type, scope_id, action_type, enabled, reviewer_permission,
			approvals_required, forbid_self_approval, expires_after_seconds,
			config, revision, updated_by, update_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,1,$11,$12)
		ON CONFLICT (scope_type, scope_id, action_type) DO UPDATE
		SET enabled=EXCLUDED.enabled,
		    reviewer_permission=EXCLUDED.reviewer_permission,
		    approvals_required=EXCLUDED.approvals_required,
		    forbid_self_approval=EXCLUDED.forbid_self_approval,
		    expires_after_seconds=EXCLUDED.expires_after_seconds,
		    config=EXCLUDED.config,
		    revision=approval_policies.revision+1,
		    updated_by=EXCLUDED.updated_by,
		    update_at=EXCLUDED.update_at
		RETURNING id, revision
	`, policy.ID, policy.ScopeType, policy.ScopeID, policy.ActionType,
		policy.Enabled, policy.ReviewerPermission, policy.ApprovalsRequired,
		policy.ForbidSelfApproval, policy.ExpiresAfterSeconds, policy.Config, actorID, now).
		Scan(&policy.ID, &policy.Revision)
	if err != nil {
		return nil, err
	}
	policy.UpdatedBy, policy.UpdateAt = actorID, now
	return &policy, nil
}

// ReplaceSystemPoliciesAndSetting atomically replaces the system-scoped
// policy set and the administrator-facing aggregate JSON. Keeping both views
// in one transaction prevents the UI from reporting a policy different from
// the one enforced by Submit after a partial database failure.
func (s *Service) ReplaceSystemPoliciesAndSetting(
	ctx context.Context,
	policies []Policy,
	aggregateSetting json.RawMessage,
	actorID string,
) error {
	if !json.Valid(aggregateSetting) || len(aggregateSetting) == 0 {
		return ErrInvalid
	}
	tx, err := s.db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	now := time.Now().UnixMilli()
	wanted := make([]string, 0, len(policies))
	seen := map[string]struct{}{}
	for _, policy := range policies {
		policy.ScopeType = "system"
		policy.ScopeID = ""
		policy.ActionType = strings.TrimSpace(policy.ActionType)
		if policy.ActionType == "" {
			return ErrInvalid
		}
		if _, duplicate := seen[policy.ActionType]; duplicate {
			return ErrInvalid
		}
		seen[policy.ActionType] = struct{}{}
		wanted = append(wanted, policy.ActionType)
		if policy.ReviewerPermission == "" {
			policy.ReviewerPermission = "review_approval"
		}
		if policy.ApprovalsRequired <= 0 {
			policy.ApprovalsRequired = 1
		}
		if len(policy.Config) == 0 || !json.Valid(policy.Config) {
			return ErrInvalid
		}
		if policy.ID == "" {
			policy.ID = uuid.NewString()
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO approval_policies (
				id, scope_type, scope_id, action_type, enabled, reviewer_permission,
				approvals_required, forbid_self_approval, expires_after_seconds,
				config, revision, updated_by, update_at
			) VALUES ($1,'system','',$2,$3,$4,$5,$6,$7,$8,1,$9,$10)
			ON CONFLICT (scope_type, scope_id, action_type) DO UPDATE
			SET enabled=EXCLUDED.enabled,
			    reviewer_permission=EXCLUDED.reviewer_permission,
			    approvals_required=EXCLUDED.approvals_required,
			    forbid_self_approval=EXCLUDED.forbid_self_approval,
			    expires_after_seconds=EXCLUDED.expires_after_seconds,
			    config=EXCLUDED.config,
			    revision=approval_policies.revision+1,
			    updated_by=EXCLUDED.updated_by,
			    update_at=EXCLUDED.update_at
		`, policy.ID, policy.ActionType, policy.Enabled, policy.ReviewerPermission,
			policy.ApprovalsRequired, policy.ForbidSelfApproval, policy.ExpiresAfterSeconds,
			policy.Config, actorID, now)
		if err != nil {
			return err
		}
	}
	if len(wanted) == 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE approval_policies
			SET enabled=FALSE, revision=revision+1, updated_by=NULLIF($1,''), update_at=$2
			WHERE scope_type='system' AND enabled
		`, actorID, now); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE approval_policies
		SET enabled=FALSE, revision=revision+1, updated_by=NULLIF($1,''), update_at=$2
		WHERE scope_type='system' AND enabled AND NOT (action_type = ANY($3::text[]))
	`, actorID, now, wanted); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO system_settings (
			section, setting_key, value_json, secret_ciphertext, secret_nonce,
			key_id, revision, updated_by, update_at
		) VALUES ('approval','workflow',$1::jsonb,NULL,NULL,NULL,1,NULLIF($2,''),$3)
		ON CONFLICT (section, setting_key) DO UPDATE
		SET value_json=EXCLUDED.value_json,
		    secret_ciphertext=NULL, secret_nonce=NULL, key_id=NULL,
		    revision=system_settings.revision+1,
		    updated_by=EXCLUDED.updated_by,
		    update_at=EXCLUDED.update_at
	`, string(aggregateSetting), actorID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) matchPolicy(ctx context.Context, actionType, teamID string) (*Policy, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT id, scope_type, COALESCE(scope_id,''), action_type, enabled,
		       reviewer_permission, approvals_required, forbid_self_approval,
		       expires_after_seconds, config, revision, COALESCE(updated_by,''), update_at
		FROM approval_policies
		WHERE enabled AND action_type=$1
		  AND ((scope_type='team' AND scope_id=$2) OR scope_type='system')
		ORDER BY CASE WHEN scope_type='team' THEN 0 ELSE 1 END
		LIMIT 1
	`, actionType, teamID)
	var policy Policy
	err := row.Scan(&policy.ID, &policy.ScopeType, &policy.ScopeID, &policy.ActionType,
		&policy.Enabled, &policy.ReviewerPermission, &policy.ApprovalsRequired,
		&policy.ForbidSelfApproval, &policy.ExpiresAfterSeconds, &policy.Config,
		&policy.Revision, &policy.UpdatedBy, &policy.UpdateAt)
	return &policy, err
}

func (s *Service) byIdempotency(ctx context.Context, requesterID, actionType, key string) (*Request, error) {
	return scanRequest(s.db.Pool.QueryRow(ctx, `
		SELECT id, policy_id, action_type, requester_id, COALESCE(team_id,''),
		       resource_type, resource_id, payload, status, COALESCE(idempotency_key,''),
		       create_at, update_at, decided_at, executed_at, expires_at
		FROM approval_requests
		WHERE requester_id=$1 AND action_type=$2 AND idempotency_key=$3
	`, requesterID, actionType, key))
}

func requestAndPolicyForUpdate(ctx context.Context, tx pgx.Tx, requestID string) (*Request, *Policy, error) {
	row := tx.QueryRow(ctx, `
		SELECT r.id, r.policy_id, r.action_type, r.requester_id, COALESCE(r.team_id,''),
		       r.resource_type, r.resource_id, r.payload, r.status,
		       COALESCE(r.idempotency_key,''), r.create_at, r.update_at,
		       r.decided_at, r.executed_at, r.expires_at,
		       p.id, p.scope_type, COALESCE(p.scope_id,''), p.action_type,
		       p.enabled, p.reviewer_permission, p.approvals_required,
		       p.forbid_self_approval, p.expires_after_seconds, p.config,
		       p.revision, COALESCE(p.updated_by,''), p.update_at
		FROM approval_requests r
		JOIN approval_policies p ON p.id=r.policy_id
		WHERE r.id=$1 FOR UPDATE OF r
	`, requestID)
	var request Request
	var policy Policy
	err := row.Scan(
		&request.ID, &request.PolicyID, &request.ActionType, &request.RequesterID,
		&request.TeamID, &request.ResourceType, &request.ResourceID, &request.Payload,
		&request.Status, &request.IdempotencyKey, &request.CreateAt, &request.UpdateAt,
		&request.DecidedAt, &request.ExecutedAt, &request.ExpiresAt,
		&policy.ID, &policy.ScopeType, &policy.ScopeID, &policy.ActionType,
		&policy.Enabled, &policy.ReviewerPermission, &policy.ApprovalsRequired,
		&policy.ForbidSelfApproval, &policy.ExpiresAfterSeconds, &policy.Config,
		&policy.Revision, &policy.UpdatedBy, &policy.UpdateAt,
	)
	return &request, &policy, err
}

func scanRequest(row interface{ Scan(...any) error }) (*Request, error) {
	var request Request
	err := row.Scan(&request.ID, &request.PolicyID, &request.ActionType, &request.RequesterID,
		&request.TeamID, &request.ResourceType, &request.ResourceID, &request.Payload,
		&request.Status, &request.IdempotencyKey, &request.CreateAt, &request.UpdateAt,
		&request.DecidedAt, &request.ExecutedAt, &request.ExpiresAt)
	return &request, err
}

// MarkExecuted closes an approved request after the protected side effect has
// completed successfully. The conditional update makes retries idempotent.
func (s *Service) MarkExecuted(ctx context.Context, requestID string) (*Request, error) {
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	row := tx.QueryRow(ctx, `
		UPDATE approval_requests
		SET status='executed', executed_at=$2, update_at=$2
		WHERE id=$1 AND status='approved'
		RETURNING id, policy_id, action_type, requester_id, COALESCE(team_id,''),
		          resource_type, resource_id, payload, status, COALESCE(idempotency_key,''),
		          create_at, update_at, decided_at, executed_at, expires_at
	`, requestID, now)
	request, err := scanRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAlreadyDecided
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_outbox SET status='succeeded', update_at=$2 WHERE request_id=$1`, requestID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return request, nil
}

func (s *Service) PendingExecutions(ctx context.Context, limit int) ([]Request, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT r.id, r.policy_id, r.action_type, r.requester_id, COALESCE(r.team_id,''),
		       r.resource_type, r.resource_id, r.payload, r.status,
		       COALESCE(r.idempotency_key,''), r.create_at, r.update_at,
		       r.decided_at, r.executed_at, r.expires_at
		FROM approval_requests r
		JOIN workflow_outbox o ON o.request_id=r.id
		WHERE r.status='approved' AND o.status IN ('pending','failed') AND o.available_at <= $1
		ORDER BY o.available_at, o.create_at
		LIMIT $2
	`, time.Now().UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := []Request{}
	for rows.Next() {
		request, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, *request)
	}
	return requests, rows.Err()
}

func (s *Service) RecordExecutionFailure(ctx context.Context, requestID string, retryAfter time.Duration) error {
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	now := time.Now()
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE workflow_outbox
		SET status='failed', attempts=attempts+1, available_at=$2, update_at=$3
		WHERE request_id=$1 AND status<>'succeeded'
	`, requestID, now.Add(retryAfter).UnixMilli(), now.UnixMilli())
	return err
}

func (s *Service) defaultCanReview(ctx context.Context, reviewerID string, policy *Policy, request *Request) (bool, error) {
	var allowed bool
	teamID := policy.ScopeID
	if teamID == "" && request != nil {
		teamID = request.TeamID
	}
	if teamID != "" {
		err := s.db.Pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM users WHERE id=$1 AND delete_at=0
				AND (' ' || roles || ' ') LIKE '% system_admin %'
			) OR EXISTS (
				SELECT 1 FROM team_members
				WHERE team_id=$2 AND user_id=$1
				AND (' ' || roles || ' ') LIKE '% team_admin %'
			)
		`, reviewerID, teamID).Scan(&allowed)
		return allowed, err
	}
	err := s.db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM users WHERE id=$1 AND delete_at=0
			AND (' ' || roles || ' ') LIKE '% system_admin %'
		)
	`, reviewerID).Scan(&allowed)
	return allowed, err
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
