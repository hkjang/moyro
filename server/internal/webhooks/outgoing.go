package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

// OutgoingService owns CRUD for outgoing webhooks.
type OutgoingService struct{ db *store.DB }

func NewOutgoing(db *store.DB) *OutgoingService { return &OutgoingService{db: db} }

type OutgoingHook struct {
	ID           string   `json:"id"`
	Token        string   `json:"token"`
	CreatorID    string   `json:"creator_id"`
	TeamID       string   `json:"team_id"`
	ChannelID    string   `json:"channel_id"`
	TriggerWords []string `json:"trigger_words"`
	TriggerWhen  int      `json:"trigger_when"`
	CallbackURLs []string `json:"callback_urls"`
	DisplayName  string   `json:"display_name"`
	ContentType  string   `json:"content_type"`
	CreateAt     int64    `json:"create_at"`
	UpdateAt     int64    `json:"update_at"`
	DeleteAt     int64    `json:"delete_at"`
}

func (s *OutgoingService) Create(ctx context.Context, creatorID, teamID, channelID string, triggerWords, callbackURLs []string, triggerWhen int, displayName, contentType string) (*OutgoingHook, error) {
	id := uuid.NewString()
	token := uuid.NewString()
	if contentType == "" {
		contentType = "application/json"
	}
	now := time.Now().UnixMilli()
	rawTriggers, _ := json.Marshal(triggerWords)
	rawCallbacks, _ := json.Marshal(callbackURLs)
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO outgoing_webhooks
			(id, token, creator_id, team_id, channel_id, trigger_words, trigger_when, callback_urls, display_name, content_type, create_at, update_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11,$11)
	`, id, token, creatorID, teamID, channelID, rawTriggers, triggerWhen, rawCallbacks, displayName, contentType, now)
	if err != nil {
		return nil, err
	}
	return &OutgoingHook{
		ID: id, Token: token, CreatorID: creatorID, TeamID: teamID, ChannelID: channelID,
		TriggerWords: triggerWords, TriggerWhen: triggerWhen, CallbackURLs: callbackURLs,
		DisplayName: displayName, ContentType: contentType, CreateAt: now, UpdateAt: now,
	}, nil
}

func (s *OutgoingService) List(ctx context.Context) ([]OutgoingHook, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, token, creator_id, team_id, COALESCE(channel_id,''), trigger_words, trigger_when, callback_urls,
		       display_name, content_type, create_at, update_at, delete_at
		FROM outgoing_webhooks
		WHERE delete_at = 0
		ORDER BY create_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutgoingHook{}
	for rows.Next() {
		var h OutgoingHook
		var triggers, callbacks []byte
		if err := rows.Scan(&h.ID, &h.Token, &h.CreatorID, &h.TeamID, &h.ChannelID,
			&triggers, &h.TriggerWhen, &callbacks, &h.DisplayName, &h.ContentType,
			&h.CreateAt, &h.UpdateAt, &h.DeleteAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(triggers, &h.TriggerWords)
		_ = json.Unmarshal(callbacks, &h.CallbackURLs)
		out = append(out, h)
	}
	return out, rows.Err()
}

// Get returns a single outgoing hook by id (active only). Used by handlers
// that need to verify a hook exists / belongs to a creator before mutating.
func (s *OutgoingService) Get(ctx context.Context, id string) (*OutgoingHook, error) {
	var h OutgoingHook
	var triggers, callbacks []byte
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, token, creator_id, team_id, COALESCE(channel_id,''), trigger_words, trigger_when, callback_urls,
		       display_name, content_type, create_at, update_at, delete_at
		FROM outgoing_webhooks WHERE id = $1 AND delete_at = 0
	`, id).Scan(&h.ID, &h.Token, &h.CreatorID, &h.TeamID, &h.ChannelID, &triggers, &h.TriggerWhen,
		&callbacks, &h.DisplayName, &h.ContentType, &h.CreateAt, &h.UpdateAt, &h.DeleteAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHookNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(triggers, &h.TriggerWords)
	_ = json.Unmarshal(callbacks, &h.CallbackURLs)
	return &h, nil
}

// Update modifies the trigger / callback / display fields of an outgoing
// hook. Token is immutable here (rotate via a separate flow if needed); the
// id and creator are also immutable. Returns ErrHookNotFound if missing or
// already deleted.
func (s *OutgoingService) Update(ctx context.Context, id string, triggerWords, callbackURLs []string, triggerWhen int, displayName, contentType string) (*OutgoingHook, error) {
	if contentType == "" {
		contentType = "application/json"
	}
	now := time.Now().UnixMilli()
	rawTriggers, _ := json.Marshal(triggerWords)
	rawCallbacks, _ := json.Marshal(callbackURLs)
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE outgoing_webhooks
		SET trigger_words = $2,
		    callback_urls = $3,
		    trigger_when  = $4,
		    display_name  = $5,
		    content_type  = $6,
		    update_at     = $7
		WHERE id = $1 AND delete_at = 0
	`, id, rawTriggers, rawCallbacks, triggerWhen, displayName, contentType, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrHookNotFound
	}
	return s.Get(ctx, id)
}

// RegenerateToken rotates an outgoing webhook's token to a fresh UUID and
// returns the updated hook. Used by ops tooling to invalidate a leaked
// token without tearing down + recreating the entire hook (which would
// reset trigger words, callbacks, etc.). Returns ErrHookNotFound if the
// hook is missing or soft-deleted.
func (s *OutgoingService) RegenerateToken(ctx context.Context, id string) (*OutgoingHook, error) {
	now := time.Now().UnixMilli()
	newToken := uuid.NewString()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE outgoing_webhooks
		   SET token = $2, update_at = $3
		 WHERE id = $1 AND delete_at = 0
	`, id, newToken, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrHookNotFound
	}
	return s.Get(ctx, id)
}

func (s *OutgoingService) Delete(ctx context.Context, id string) error {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE outgoing_webhooks SET delete_at = $2 WHERE id = $1 AND delete_at = 0
	`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrHookNotFound
	}
	return nil
}

// candidatesFor returns every active outgoing hook whose scope includes the
// post's channel/team. We don't match trigger words here — the dispatcher
// does that in-memory so we don't force Postgres to split JSONB arrays.
func (s *OutgoingService) candidatesFor(ctx context.Context, teamID, channelID string) ([]OutgoingHook, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, token, creator_id, team_id, COALESCE(channel_id,''), trigger_words, trigger_when, callback_urls,
		       display_name, content_type, create_at, update_at, delete_at
		FROM outgoing_webhooks
		WHERE delete_at = 0
		  AND team_id = $1
		  AND (channel_id IS NULL OR channel_id = $2)
	`, teamID, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutgoingHook{}
	for rows.Next() {
		var h OutgoingHook
		var triggers, callbacks []byte
		if err := rows.Scan(&h.ID, &h.Token, &h.CreatorID, &h.TeamID, &h.ChannelID,
			&triggers, &h.TriggerWhen, &callbacks, &h.DisplayName, &h.ContentType,
			&h.CreateAt, &h.UpdateAt, &h.DeleteAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(triggers, &h.TriggerWords)
		_ = json.Unmarshal(callbacks, &h.CallbackURLs)
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---- Dispatcher ----

// Dispatcher processes outbound webhook callbacks on a worker pool so a
// slow or hanging endpoint can't delay post creation. It also enforces the
// loop / depth safeguards the plan calls out.
type Dispatcher struct {
	svc    *OutgoingService
	posts  *posts.Service
	teamOf TeamResolver
	logger *slog.Logger

	client  *http.Client
	workers int
	queue   *DeliveryService
	wake    chan struct{}

	allowedMu    sync.RWMutex
	allowedHosts map[string]struct{}
	// allowedConfigured distinguishes the legacy secure-public fallback from
	// an administrator-managed empty list. Once runtime settings load, an
	// empty list means deny all outbound callbacks.
	allowedConfigured bool
}

// TeamResolver maps a channel id to the team id that owns it. The
// dispatcher uses this to filter candidate outgoing hooks by team_id
// without forcing this package to import `channels`.
type TeamResolver func(ctx context.Context, channelID string) (teamID string, err error)

type dispatchJob struct {
	hook OutgoingHook
	post *posts.Post
	user string // post author username, pre-resolved
}

var errOutgoingRedirect = errors.New("outgoing webhook redirects are disabled")

const maximumWebhookDepth = 3

// newOutboundHTTPClient deliberately rejects every redirect. Callback URLs
// are checked against the administrator's allow-list before the request is
// sent, but net/http would otherwise follow a 3xx response to a different
// host without re-entering hostAllowed. Rejecting redirects is simpler and
// safer than attempting to preserve method/body semantics while validating
// every hop.
func newOutboundHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errOutgoingRedirect
		},
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     30 * time.Second,
		},
	}
}

// NewDispatcher spins up the worker pool. workers<=0 defaults to 16. A nil
// `allowedHosts` leaves workers paused until ConfigureAllowedHosts loads the
// durable policy; a non-nil empty list is an explicit deny-all policy. This
// distinction keeps startup fail-closed without turning preserved work dead
// merely because settings have not finished loading yet.
func NewDispatcher(svc *OutgoingService, postSvc *posts.Service, teamOf TeamResolver, logger *slog.Logger, workers int, allowedHosts []string) *Dispatcher {
	if workers <= 0 {
		workers = 16
	}
	if logger == nil {
		logger = slog.Default()
	}
	allow := map[string]struct{}{}
	for _, h := range allowedHosts {
		allow[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	d := &Dispatcher{
		svc: svc, posts: postSvc, teamOf: teamOf, logger: logger,
		client:            newOutboundHTTPClient(),
		workers:           workers,
		wake:              make(chan struct{}, workers),
		allowedHosts:      allow,
		allowedConfigured: allowedHosts != nil,
	}
	if svc != nil && svc.db != nil && svc.db.Pool != nil {
		d.queue = NewDeliveryService(svc.db)
		for i := 0; i < workers; i++ {
			go d.runWorker()
		}
	}
	return d
}

// ConfigureAllowedHosts replaces the outbound callback allow-list at runtime.
// Administrators edit this value through the database-backed site settings,
// so reads and updates must remain race-free while webhook workers are active.
// An empty administrator-managed list denies all outbound callbacks.
func (d *Dispatcher) ConfigureAllowedHosts(hosts []string) {
	if d == nil {
		return
	}
	allow := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			allow[host] = struct{}{}
		}
	}
	d.allowedMu.Lock()
	d.allowedHosts = allow
	d.allowedConfigured = true
	d.allowedMu.Unlock()
	d.signalWorkers(d.workers)
}

func (d *Dispatcher) policyReady() bool {
	if d == nil {
		return false
	}
	d.allowedMu.RLock()
	defer d.allowedMu.RUnlock()
	return d.allowedConfigured
}

func withDepth(p *posts.Post, depth int) *posts.Post {
	cp := *p
	if cp.Props == nil {
		cp.Props = map[string]any{}
	} else {
		m := make(map[string]any, len(cp.Props)+1)
		for k, v := range cp.Props {
			m[k] = v
		}
		cp.Props = m
	}
	cp.Props["webhook_depth"] = depth
	return &cp
}

// hostAllowed is the outbound request safety check.
//  1. URL must parse + be http/https.
//  2. Host must be on the explicit allow-list. Empty means deny all.
func (d *Dispatcher) hostAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	d.allowedMu.RLock()
	_, explicitlyAllowed := d.allowedHosts[strings.ToLower(host)]
	hasManagedPolicy := d.allowedConfigured
	d.allowedMu.RUnlock()
	return hasManagedPolicy && explicitlyAllowed
}

// outgoingPayload mirrors Mattermost's classic JSON body so existing tools
// that understand that shape work without adaptation.
func outgoingPayload(job dispatchJob) map[string]any {
	return map[string]any{
		"token":               job.hook.Token,
		"team_id":             job.hook.TeamID,
		"channel_id":          job.post.ChannelID,
		"timestamp":           job.post.CreateAt,
		"user_id":             job.post.UserID,
		"user_name":           job.user,
		"post_id":             job.post.ID,
		"text":                job.post.Message,
		"trigger_word":        firstMatch(job.post.Message, job.hook.TriggerWords, job.hook.TriggerWhen),
		"file_ids":            job.post.FileIDs,
		"moyro_webhook_depth": webhookDepth(job.post),
	}
}

// matchTrigger applies the Mattermost semantics:
//
//	triggerWhen == 0 → first word of message equals a trigger
//	triggerWhen == 1 → trigger appears anywhere (case-insensitive)
//
// Empty trigger list = never match (don't accidentally broadcast every post).
func matchTrigger(message string, triggers []string, when int) bool {
	if len(triggers) == 0 {
		return false
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return false
	}
	first := firstWord(msg)
	lower := strings.ToLower(msg)
	for _, t := range triggers {
		tt := strings.ToLower(strings.TrimSpace(t))
		if tt == "" {
			continue
		}
		if when == 0 {
			if strings.EqualFold(first, tt) {
				return true
			}
		} else {
			if strings.Contains(lower, tt) {
				return true
			}
		}
	}
	return false
}

func firstMatch(message string, triggers []string, when int) string {
	if len(triggers) == 0 {
		return ""
	}
	msg := strings.TrimSpace(message)
	first := firstWord(msg)
	lower := strings.ToLower(msg)
	for _, t := range triggers {
		tt := strings.TrimSpace(t)
		if tt == "" {
			continue
		}
		if when == 0 {
			if strings.EqualFold(first, tt) {
				return tt
			}
		} else {
			if strings.Contains(lower, strings.ToLower(tt)) {
				return tt
			}
		}
	}
	return ""
}

func firstWord(s string) string {
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			return s[:i]
		}
	}
	return s
}

// GetByToken is used by a future reply-back path (a callback responding
// with { text: "..." } posts as the hook creator). Unused initially but
// keeps the shape ready.
func (s *OutgoingService) GetByToken(ctx context.Context, token string) (*OutgoingHook, error) {
	var h OutgoingHook
	var triggers, callbacks []byte
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, token, creator_id, team_id, COALESCE(channel_id,''), trigger_words, trigger_when, callback_urls,
		       display_name, content_type, create_at, update_at, delete_at
		FROM outgoing_webhooks WHERE token = $1 AND delete_at = 0
	`, token).Scan(&h.ID, &h.Token, &h.CreatorID, &h.TeamID, &h.ChannelID, &triggers, &h.TriggerWhen,
		&callbacks, &h.DisplayName, &h.ContentType, &h.CreateAt, &h.UpdateAt, &h.DeleteAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHookNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(triggers, &h.TriggerWords)
	_ = json.Unmarshal(callbacks, &h.CallbackURLs)
	return &h, nil
}
