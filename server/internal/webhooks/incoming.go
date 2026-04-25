// Package webhooks implements Mattermost-style incoming and outgoing
// integrations.
//
// Incoming webhook: a public POST endpoint (/hooks/{id}) receives a JSON
// body, resolves the hook's creator + target channel, and persists a post
// on behalf of the creator. Channel locking is on by default so an
// accidentally-leaked URL can only post to the original channel.
//
// Outgoing webhook: a dispatcher (see outgoing.go) watches post creation,
// matches trigger words, and POSTs to each registered callback URL.
package webhooks

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/moddle/moddle/server/internal/posts"
	"github.com/moddle/moddle/server/internal/store"
)

var (
	ErrHookNotFound = errors.New("webhook not found")
	ErrHookDisabled = errors.New("webhook disabled")
)

// IncomingService owns CRUD for incoming webhooks and the fire path.
type IncomingService struct {
	db    *store.DB
	posts *posts.Service
}

func NewIncoming(db *store.DB, postSvc *posts.Service) *IncomingService {
	return &IncomingService{db: db, posts: postSvc}
}

type IncomingHook struct {
	ID             string `json:"id"`
	CreatorID      string `json:"creator_id"`
	ChannelID      string `json:"channel_id"`
	TeamID         string `json:"team_id"`
	DisplayName    string `json:"display_name"`
	Username       string `json:"username"`
	IconURL        string `json:"icon_url"`
	ChannelLocked  bool   `json:"channel_locked"`
	CreateAt       int64  `json:"create_at"`
	UpdateAt       int64  `json:"update_at"`
	DeleteAt       int64  `json:"delete_at"`
}

// Create persists a new hook. `id` is randomly generated and doubles as
// the URL slug (so the URL is public once revealed — callers MUST NOT
// reveal it except in the create response).
func (s *IncomingService) Create(ctx context.Context, creatorID, channelID, teamID, displayName, username, iconURL string, channelLocked bool) (*IncomingHook, error) {
	id := uuid.NewString()
	now := time.Now().UnixMilli()
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO incoming_webhooks
			(id, creator_id, channel_id, team_id, display_name, username, icon_url, channel_locked, create_at, update_at)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,$9)
	`, id, creatorID, channelID, teamID, displayName, username, iconURL, channelLocked, now)
	if err != nil {
		return nil, err
	}
	return &IncomingHook{
		ID: id, CreatorID: creatorID, ChannelID: channelID, TeamID: teamID,
		DisplayName: displayName, Username: username, IconURL: iconURL,
		ChannelLocked: channelLocked, CreateAt: now, UpdateAt: now,
	}, nil
}

func (s *IncomingService) Get(ctx context.Context, id string) (*IncomingHook, error) {
	var h IncomingHook
	var teamID *string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, creator_id, channel_id, team_id, display_name, username, icon_url,
		       channel_locked, create_at, update_at, delete_at
		FROM incoming_webhooks WHERE id=$1
	`, id).Scan(&h.ID, &h.CreatorID, &h.ChannelID, &teamID, &h.DisplayName, &h.Username,
		&h.IconURL, &h.ChannelLocked, &h.CreateAt, &h.UpdateAt, &h.DeleteAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHookNotFound
	}
	if err != nil {
		return nil, err
	}
	if teamID != nil {
		h.TeamID = *teamID
	}
	if h.DeleteAt != 0 {
		return nil, ErrHookDisabled
	}
	return &h, nil
}

func (s *IncomingService) List(ctx context.Context) ([]IncomingHook, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, creator_id, channel_id, COALESCE(team_id,''), display_name, username, icon_url,
		       channel_locked, create_at, update_at, delete_at
		FROM incoming_webhooks
		WHERE delete_at = 0
		ORDER BY create_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IncomingHook{}
	for rows.Next() {
		var h IncomingHook
		if err := rows.Scan(&h.ID, &h.CreatorID, &h.ChannelID, &h.TeamID, &h.DisplayName,
			&h.Username, &h.IconURL, &h.ChannelLocked, &h.CreateAt, &h.UpdateAt, &h.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Update modifies the human-facing fields of an incoming hook. The id and
// creator are immutable; the channel can be changed (admin tools sometimes
// retarget an old hook rather than rotate the URL). Returns ErrHookNotFound
// if the hook is missing or already deleted.
func (s *IncomingService) Update(ctx context.Context, id, channelID, displayName, username, iconURL string, channelLocked bool) (*IncomingHook, error) {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE incoming_webhooks
		SET channel_id = $2,
		    display_name = $3,
		    username = $4,
		    icon_url = $5,
		    channel_locked = $6,
		    update_at = $7
		WHERE id = $1 AND delete_at = 0
	`, id, channelID, displayName, username, iconURL, channelLocked, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrHookNotFound
	}
	return s.Get(ctx, id)
}

func (s *IncomingService) Delete(ctx context.Context, id string) error {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE incoming_webhooks SET delete_at = $2 WHERE id = $1 AND delete_at = 0
	`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrHookNotFound
	}
	return nil
}

// IncomingPayload mirrors the Mattermost-compatible body shape.
type IncomingPayload struct {
	Text        string `json:"text"`
	Username    string `json:"username"`
	IconURL     string `json:"icon_url"`
	ChannelName string `json:"channel"` // ignored unless channel_locked=false
}

// Fire creates a post from an incoming hook. The post is authored by the
// hook's creator; if the payload overrides username/icon those land in
// `props` so the frontend can render them without impersonating users on
// the server side.
func (s *IncomingService) Fire(ctx context.Context, hook *IncomingHook, payload IncomingPayload) (*posts.Post, error) {
	if strings.TrimSpace(payload.Text) == "" {
		return nil, errors.New("empty text")
	}
	props := map[string]any{
		"from_webhook": "true",
	}
	if payload.Username != "" {
		props["override_username"] = payload.Username
	} else if hook.Username != "" {
		props["override_username"] = hook.Username
	}
	if payload.IconURL != "" {
		props["override_icon_url"] = payload.IconURL
	} else if hook.IconURL != "" {
		props["override_icon_url"] = hook.IconURL
	}
	p, err := s.posts.Create(ctx, hook.ChannelID, hook.CreatorID, "", payload.Text, props, nil)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListByChannel returns every active incoming webhook for a channel.
// Used by the admin UI; not on the hot path.
func (s *IncomingService) ListByChannel(ctx context.Context, channelID string) ([]IncomingHook, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, creator_id, channel_id, COALESCE(team_id,''), display_name, username, icon_url,
		       channel_locked, create_at, update_at, delete_at
		FROM incoming_webhooks
		WHERE channel_id = $1 AND delete_at = 0
		ORDER BY create_at DESC
	`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IncomingHook{}
	for rows.Next() {
		var h IncomingHook
		if err := rows.Scan(&h.ID, &h.CreatorID, &h.ChannelID, &h.TeamID, &h.DisplayName,
			&h.Username, &h.IconURL, &h.ChannelLocked, &h.CreateAt, &h.UpdateAt, &h.DeleteAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

