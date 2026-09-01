package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/workitems"
	"github.com/hkjang/moyro/server/internal/ws"
	"github.com/jackc/pgx/v5"
)

const (
	runLeaseDuration  = 2 * time.Minute
	runPollInterval   = 5 * time.Second
	runMaxAttempts    = 5
	runInitialBackoff = 30 * time.Second
	runMaximumBackoff = 15 * time.Minute
)

var (
	ErrStaleClaim         = errors.New("automation run claim is stale")
	ErrNoLongerAuthorized = errors.New("automation owner can no longer access the source channel")
	ErrRuleInactive       = errors.New("automation rule is disabled or deleted")
)

const runColumns = `
	run.id, run.rule_id, run.action_id, run.post_id, run.actor_id, run.action_type, run.action_config,
	run.status, run.attempt_count, run.next_attempt_at, run.claimed_at, run.lease_until, run.claim_token,
	run.result_type, run.result_id, run.last_error_code, run.last_error_text, run.create_at, run.update_at, run.completed_at`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var run Run
	err := row.Scan(
		&run.ID, &run.RuleID, &run.ActionID, &run.PostID, &run.ActorID,
		&run.ActionType, &run.ActionConfig, &run.Status, &run.AttemptCount,
		&run.NextAttemptAt, &run.ClaimedAt, &run.LeaseUntil, &run.ClaimToken,
		&run.ResultType, &run.ResultID, &run.LastErrorCode, &run.LastErrorText,
		&run.CreateAt, &run.UpdateAt, &run.CompletedAt,
	)
	return &run, err
}

func (s *Service) ClaimDue(ctx context.Context, now int64, limit int) ([]Run, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.Pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM automation_runs
			WHERE (status IN ('pending','retry') AND next_attempt_at<=$1)
			   OR (status='processing' AND lease_until<=$1)
			ORDER BY CASE WHEN status='processing' THEN lease_until ELSE next_attempt_at END,
			         create_at, id
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		), claimed AS (
			SELECT id, gen_random_uuid()::text AS token FROM due
		)
		UPDATE automation_runs run
		SET status='processing', attempt_count=run.attempt_count+1,
		    claimed_at=$1, lease_until=$3, claim_token=claimed.token, update_at=$1
		FROM claimed WHERE run.id=claimed.id
		RETURNING `+runColumns+`
	`, now, limit, now+runLeaseDuration.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

func (s *Service) MarkSucceeded(ctx context.Context, run *Run, resultType, resultID string, at int64) error {
	if run == nil || run.ClaimToken == "" {
		return ErrStaleClaim
	}
	result, err := s.db.Pool.Exec(ctx, `
		UPDATE automation_runs
		SET status='succeeded', result_type=$1, result_id=$2, completed_at=$3,
		    update_at=$3, next_attempt_at=0, claimed_at=0, lease_until=0,
		    claim_token='', last_error_code='', last_error_text=''
		WHERE id=$4 AND status='processing' AND claim_token=$5
	`, resultType, resultID, at, run.ID, run.ClaimToken)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrStaleClaim
	}
	return nil
}

func retryDelay(attempt int) time.Duration {
	delay := runInitialBackoff
	for index := 1; index < attempt && delay < runMaximumBackoff; index++ {
		delay *= 2
		if delay > runMaximumBackoff {
			return runMaximumBackoff
		}
	}
	return delay
}

func truncateError(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 1000 {
		return string(runes[:1000])
	}
	return value
}

func (s *Service) MarkFailed(ctx context.Context, run *Run, code string, cause error, permanent bool, at int64) error {
	if run == nil || run.ClaimToken == "" {
		return ErrStaleClaim
	}
	status := StatusRetry
	next := at + retryDelay(run.AttemptCount).Milliseconds()
	completedAt := int64(0)
	if permanent || run.AttemptCount >= runMaxAttempts {
		status = StatusDead
		next = 0
		completedAt = at
	}
	message := ""
	if cause != nil {
		message = truncateError(cause.Error())
	}
	result, err := s.db.Pool.Exec(ctx, `
		UPDATE automation_runs
		SET status=$1, next_attempt_at=$2, completed_at=$3, update_at=$4,
		    claimed_at=0, lease_until=0, claim_token='', last_error_code=$5, last_error_text=$6
		WHERE id=$7 AND status='processing' AND claim_token=$8
	`, status, next, completedAt, at, code, message, run.ID, run.ClaimToken)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrStaleClaim
	}
	return nil
}

func (s *Service) ListRunsForPrincipal(ctx context.Context, principal rbac.Principal, ruleID string, limit int) ([]Run, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	constraints := rbac.ResourceConstraintsFor(principal)
	var visible bool
	if err := s.db.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM automation_rules rule
			JOIN channels channel ON channel.id=rule.channel_id AND channel.delete_at=0
			JOIN channel_members member ON member.channel_id=rule.channel_id AND member.user_id=$2
			JOIN users owner ON owner.id=member.user_id AND owner.delete_at=0
			  AND (
				NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(owner.roles), E'\\s+')))
				OR owner.guest_expires_at>$6
			  )
			WHERE rule.id=$1 AND rule.created_by=$2 AND rule.delete_at=0
			  AND (NOT $3::boolean OR cardinality($4::text[])=0 OR rule.team_id=ANY($4::text[]))
			  AND (NOT $3::boolean OR cardinality($5::text[])=0 OR rule.channel_id=ANY($5::text[]))
		)
	`, strings.TrimSpace(ruleID), strings.TrimSpace(principal.UserID), constraints.Restricted,
		constraints.TeamIDs, constraints.ChannelIDs, time.Now().UnixMilli()).Scan(&visible); err != nil {
		return nil, err
	}
	if !visible {
		return nil, ErrNotFound
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+runColumns+`
		FROM automation_runs run
		JOIN automation_rules rule ON rule.id=run.rule_id
		WHERE run.rule_id=$1 AND rule.created_by=$2
		ORDER BY run.create_at DESC, run.id DESC
		LIMIT $3
	`, strings.TrimSpace(ruleID), strings.TrimSpace(principal.UserID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		run.ActionConfig = nil
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

type postReader interface {
	Get(context.Context, string) (*posts.Post, error)
}

type workItemCreator interface {
	Create(context.Context, string, workitems.CreateInput) (*workitems.Item, bool, error)
}

type broadcaster interface {
	Broadcast(ws.Event)
}

type Worker struct {
	service   *Service
	posts     postReader
	workItems workItemCreator
	events    broadcaster
	logger    *slog.Logger
	tickEvery time.Duration
	batchSize int
}

func NewWorker(service *Service, postStore postReader, workItemService workItemCreator, events broadcaster, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		service: service, posts: postStore, workItems: workItemService, events: events,
		// Each action has a 20 second deadline. Four sequential actions plus
		// finalization remain inside the two-minute lease even at the deadline.
		logger: logger, tickEvery: runPollInterval, batchSize: 4,
	}
}

func (w *Worker) Run(ctx context.Context) {
	initial := time.NewTimer(3 * time.Second)
	defer initial.Stop()
	ticker := time.NewTicker(w.tickEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-initial.C:
			w.tick(ctx)
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	runs, err := w.service.ClaimDue(claimCtx, time.Now().UnixMilli(), w.batchSize)
	cancel()
	if err != nil {
		w.logger.Warn("automation claim", "err", err)
		return
	}
	for index := range runs {
		w.execute(ctx, &runs[index])
	}
}

func (w *Worker) execute(ctx context.Context, run *Run) {
	executionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	resultType, resultID, item, err := w.executeAction(executionCtx, run)
	cancel()
	if err != nil {
		permanent := errors.Is(err, ErrNoLongerAuthorized) || errors.Is(err, ErrInvalid) ||
			errors.Is(err, ErrRuleInactive) ||
			errors.Is(err, workitems.ErrInvalid) || errors.Is(err, workitems.ErrForbidden) ||
			errors.Is(err, workitems.ErrSourceNotAccessible)
		finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		markErr := w.service.MarkFailed(finalizeCtx, run, automationErrorCode(err), err, permanent, time.Now().UnixMilli())
		finalizeCancel()
		if markErr != nil && !errors.Is(markErr, ErrStaleClaim) {
			w.logger.Warn("automation mark failed", "run_id", run.ID, "err", markErr)
		}
		return
	}
	finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	err = w.service.MarkSucceeded(finalizeCtx, run, resultType, resultID, time.Now().UnixMilli())
	finalizeCancel()
	if err != nil {
		if !errors.Is(err, ErrStaleClaim) {
			w.logger.Warn("automation mark succeeded", "run_id", run.ID, "err", err)
		}
		return
	}
	w.broadcastSuccess(run, item, resultType, resultID)
}

func automationErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrRuleInactive):
		return "rule_inactive"
	case errors.Is(err, ErrNoLongerAuthorized):
		return "authorization_revoked"
	case errors.Is(err, workitems.ErrSourceNotAccessible):
		return "source_unavailable"
	case errors.Is(err, workitems.ErrForbidden):
		return "assignee_forbidden"
	case errors.Is(err, workitems.ErrInvalid), errors.Is(err, ErrInvalid):
		return "invalid_action"
	default:
		return "execution_failed"
	}
}

func actionTitle(config ActionConfig, message string) string {
	title := config.Title
	if title == "" {
		title = message
	} else {
		title = strings.ReplaceAll(title, "{message}", message)
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = "자동화로 만든 업무"
	}
	runes := []rune(title)
	if len(runes) > 240 {
		title = string(runes[:240])
	}
	return title
}

func (w *Worker) executeAction(ctx context.Context, run *Run) (string, string, *workitems.Item, error) {
	if run == nil || w.service == nil || w.posts == nil {
		return "", "", nil, ErrInvalid
	}
	post, err := w.posts.Get(ctx, run.PostID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", nil, ErrNoLongerAuthorized
		}
		return "", "", nil, err
	}
	if post == nil || post.DeleteAt != 0 {
		return "", "", nil, ErrNoLongerAuthorized
	}
	var authorized, ruleActive bool
	if err := w.service.db.Pool.QueryRow(ctx, `
		SELECT
			EXISTS(
				SELECT 1 FROM channel_members cm
				JOIN users u ON u.id=cm.user_id AND u.delete_at=0
				  AND (
					NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(u.roles), E'\\s+')))
					OR u.guest_expires_at>$4
				  )
				JOIN channels c ON c.id=cm.channel_id AND c.delete_at=0
				WHERE cm.channel_id=$1 AND cm.user_id=$2
			),
			EXISTS(
				SELECT 1 FROM automation_rules rule
				WHERE rule.id=$3 AND rule.created_by=$2 AND rule.channel_id=$1
				  AND rule.enabled=TRUE AND rule.delete_at=0
			)
	`, post.ChannelID, run.ActorID, run.RuleID, time.Now().UnixMilli()).Scan(&authorized, &ruleActive); err != nil {
		return "", "", nil, err
	}
	if !authorized {
		return "", "", nil, ErrNoLongerAuthorized
	}
	if !ruleActive {
		return "", "", nil, ErrRuleInactive
	}
	var config ActionConfig
	if err := json.Unmarshal(run.ActionConfig, &config); err != nil {
		return "", "", nil, ErrInvalid
	}
	action := Action{Type: run.ActionType, Config: config}
	if err := normalizeAction(&action); err != nil {
		return "", "", nil, err
	}
	config = action.Config
	switch run.ActionType {
	case ActionTask, ActionDecision:
		if w.workItems == nil {
			return "", "", nil, errors.New("work item service is unavailable")
		}
		dueAt := int64(0)
		if run.ActionType == ActionTask && config.DueOffsetMinutes > 0 {
			dueAt = post.CreateAt + int64(config.DueOffsetMinutes)*time.Minute.Milliseconds()
		}
		item, _, err := w.workItems.Create(ctx, run.ActorID, workitems.CreateInput{
			Kind: run.ActionType, Title: actionTitle(config, post.Message), Description: config.Description,
			AssigneeID: config.AssigneeID, ReviewerID: config.ReviewerID,
			SourcePostID: post.ID, DueAt: dueAt, Priority: config.Priority,
			RecurrenceUnit: config.RecurrenceUnit, RecurrenceInterval: config.RecurrenceInterval,
			InitialStatus: config.InitialStatus, IdempotencyKey: "automation:" + run.ID,
		})
		if err != nil {
			return "", "", nil, err
		}
		return "work_item", item.ID, item, nil
	case ActionReminder:
		remindAt := post.CreateAt + int64(config.RemindOffsetMinutes)*time.Minute.Milliseconds()
		if remindAt <= time.Now().UnixMilli() {
			remindAt = time.Now().Add(time.Minute).UnixMilli()
		}
		reminderID := "automation-" + run.ID
		_, err := w.service.db.Pool.Exec(ctx, `
			INSERT INTO post_reminders (id, user_id, post_id, remind_at, create_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (id) DO NOTHING
		`, reminderID, run.ActorID, post.ID, remindAt, time.Now().UnixMilli())
		if err != nil {
			return "", "", nil, err
		}
		return "reminder", reminderID, nil, nil
	default:
		return "", "", nil, ErrInvalid
	}
}

func (w *Worker) broadcastSuccess(run *Run, item *workitems.Item, resultType, resultID string) {
	if w.events == nil || run == nil {
		return
	}
	if resultType == "reminder" {
		w.events.Broadcast(ws.Event{
			Event:     "reminder_created",
			Data:      map[string]any{"id": resultID, "post_id": run.PostID, "automated": true},
			Broadcast: ws.Broadcast{UserID: run.ActorID},
		})
		return
	}
	if item == nil {
		return
	}
	broadcast := ws.Broadcast{ChannelID: item.ChannelID}
	if item.Kind == workitems.KindTask {
		broadcast.UserID = item.CreatedBy
		broadcast.TeamID = item.TeamID
	}
	w.events.Broadcast(ws.Event{
		Event: "work_item_changed", Data: map[string]any{"work_item": item, "automated": true},
		Broadcast: broadcast,
	})
	if item.Kind == workitems.KindTask && item.AssigneeID != "" && item.AssigneeID != item.CreatedBy {
		w.events.Broadcast(ws.Event{
			Event: "work_item_changed", Data: map[string]any{"work_item": item, "automated": true},
			Broadcast: ws.Broadcast{UserID: item.AssigneeID, ChannelID: item.ChannelID, TeamID: item.TeamID},
		})
	}
}

func deterministicRunID(ruleID, actionID, postID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s\x00%s\x00%s", ruleID, actionID, postID))).String()
}
