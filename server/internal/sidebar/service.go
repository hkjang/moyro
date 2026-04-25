// Package sidebar implements Mattermost v4-shaped channel sidebar categories.
//
// Each user has one or more categories per team that drive how channels are
// grouped and ordered in the sidebar UI. The four canonical type values are:
//
//	favorites        — channels the user has starred (one auto-created per team)
//	channels         — public/private channels not classified elsewhere (one per team)
//	direct_messages  — DMs and group DMs (one per team)
//	custom           — user-defined groups; arbitrary display_name
//
// On the first ListForTeam call we auto-bootstrap the three default categories
// for that (user, team) pair. Existing channels are sorted into them based on
// type and any pre-existing `favorite_channel/<id>=true` rows in preferences
// (so users upgrading from Phase 21 keep their stars).
//
// All multi-row writes (Update, UpdateOrder) run inside a single transaction
// because Mattermost's webapp issues atomic batch updates after drag-drop and
// expects either-all-or-nothing semantics.
package sidebar

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/moddle/moddle/server/internal/store"
)

const (
	TypeFavorites      = "favorites"
	TypeChannels       = "channels"
	TypeDirectMessages = "direct_messages"
	TypeCustom         = "custom"

	SortingAlpha  = "alpha"
	SortingRecent = "recent"
	SortingManual = "manual"
)

// Category mirrors Mattermost's `SidebarCategoryWithChannels` shape exactly.
// `ChannelIDs` is always a non-nil slice (Mattermost clients tolerate `null`
// poorly — they iterate without a guard).
type Category struct {
	ID          string   `json:"id"`
	UserID      string   `json:"user_id"`
	TeamID      string   `json:"team_id"`
	Type        string   `json:"type"`
	DisplayName string   `json:"display_name"`
	SortOrder   int      `json:"sort_order"`
	Sorting     string   `json:"sorting"`
	Muted       bool     `json:"muted"`
	Collapsed   bool     `json:"collapsed"`
	ChannelIDs  []string `json:"channel_ids"`
}

// OrderedCategories is the list-endpoint envelope shape. `Order` is the array
// of category IDs in display order; `Categories` carries the full row data.
// Splitting them mirrors Mattermost's contract — the desktop client reads
// `order` independently when reconciling local cache.
type OrderedCategories struct {
	Categories []Category `json:"categories"`
	Order      []string   `json:"order"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// ListForTeam returns the user's categories for a team, bootstrapping the
// three defaults on first call. Channel membership is recomputed from the
// authoritative `channel_members` table on every call so categories never
// surface stale rows when membership drifts (channel archive, kick, leave).
func (s *Service) ListForTeam(ctx context.Context, userID, teamID string) (*OrderedCategories, error) {
	if err := s.ensureDefaults(ctx, userID, teamID); err != nil {
		return nil, err
	}
	cats, err := s.loadCategories(ctx, userID, teamID)
	if err != nil {
		return nil, err
	}
	if err := s.fillChannelIDs(ctx, userID, teamID, cats); err != nil {
		return nil, err
	}
	out := &OrderedCategories{Categories: cats, Order: make([]string, 0, len(cats))}
	for _, c := range cats {
		out.Order = append(out.Order, c.ID)
	}
	return out, nil
}

// ensureDefaults creates favorites/channels/direct_messages categories the
// first time a user touches the sidebar for a team. Idempotent: a row count
// of zero means "fresh user" and we INSERT the three defaults; non-zero
// means "already bootstrapped" and we no-op.
func (s *Service) ensureDefaults(ctx context.Context, userID, teamID string) error {
	var count int
	if err := s.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM sidebar_categories WHERE user_id=$1 AND team_id=$2
	`, userID, teamID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	defaults := []struct {
		typ, display string
		order        int
	}{
		{TypeFavorites, "Favorites", 0},
		{TypeChannels, "Channels", 10},
		{TypeDirectMessages, "Direct Messages", 20},
	}
	for _, d := range defaults {
		if _, err := tx.Exec(ctx, `
			INSERT INTO sidebar_categories (id, user_id, team_id, type, display_name, sort_order, sorting, muted, collapsed, create_at, update_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,FALSE,FALSE,$8,$8)
		`, uuid.NewString(), userID, teamID, d.typ, d.display, d.order, SortingAlpha, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// loadCategories pulls every category row for (user, team) ordered by the
// stable (sort_order, create_at) tuple. create_at is the tie-breaker so a
// freshly-created custom category always appears at a deterministic spot
// even before the client sends an explicit reorder.
func (s *Service) loadCategories(ctx context.Context, userID, teamID string) ([]Category, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, user_id, team_id, type, display_name, sort_order, sorting, muted, collapsed
		FROM sidebar_categories
		WHERE user_id=$1 AND team_id=$2
		ORDER BY sort_order ASC, create_at ASC
	`, userID, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.UserID, &c.TeamID, &c.Type, &c.DisplayName,
			&c.SortOrder, &c.Sorting, &c.Muted, &c.Collapsed); err != nil {
			return nil, err
		}
		c.ChannelIDs = []string{}
		out = append(out, c)
	}
	return out, rows.Err()
}

// fillChannelIDs hydrates every category's ChannelIDs slice. Membership is
// recomputed each call from `channel_members` joined against the team's
// channels; channels not yet placed in a custom category fall into either
// "direct_messages" (D/G) or "channels" (O/P). Favorites are read from the
// explicit join table only (never auto-classified).
func (s *Service) fillChannelIDs(ctx context.Context, userID, teamID string, cats []Category) error {
	if len(cats) == 0 {
		return nil
	}

	// Build category lookup tables.
	byID := make(map[string]*Category, len(cats))
	var favCat, defChannels, defDM *Category
	for i := range cats {
		c := &cats[i]
		byID[c.ID] = c
		switch c.Type {
		case TypeFavorites:
			favCat = c
		case TypeChannels:
			defChannels = c
		case TypeDirectMessages:
			defDM = c
		}
	}

	// Pull explicit category memberships first.
	explicit := make(map[string]string) // channel_id -> category_id
	rows, err := s.db.Pool.Query(ctx, `
		SELECT scc.category_id, scc.channel_id
		FROM sidebar_category_channels scc
		JOIN sidebar_categories sc ON sc.id = scc.category_id
		WHERE sc.user_id=$1 AND sc.team_id=$2
		ORDER BY scc.sort_order ASC
	`, userID, teamID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var catID, chanID string
		if err := rows.Scan(&catID, &chanID); err != nil {
			rows.Close()
			return err
		}
		if c, ok := byID[catID]; ok {
			c.ChannelIDs = append(c.ChannelIDs, chanID)
			explicit[chanID] = catID
		}
	}
	rows.Close()

	// Pull every channel the user is a member of in this team (or DM/G,
	// which have NULL team_id) and route the unrouted ones into the right
	// default category.
	rows, err = s.db.Pool.Query(ctx, `
		SELECT c.id, c.type
		FROM channels c
		JOIN channel_members m ON m.channel_id = c.id
		WHERE m.user_id = $1 AND c.delete_at = 0
		  AND (c.team_id = $2 OR c.type IN ('D','G'))
		ORDER BY c.display_name ASC
	`, userID, teamID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, typ string
		if err := rows.Scan(&id, &typ); err != nil {
			return err
		}
		if _, placed := explicit[id]; placed {
			continue
		}
		if typ == "D" || typ == "G" {
			if defDM != nil {
				defDM.ChannelIDs = append(defDM.ChannelIDs, id)
			}
		} else {
			if defChannels != nil {
				defChannels.ChannelIDs = append(defChannels.ChannelIDs, id)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Favorites: also populate from `favorite_channel` preference rows so
	// Phase 21 stars survive the upgrade. Idempotent: rows already in the
	// explicit join table for this category aren't double-added.
	if favCat != nil {
		seen := make(map[string]bool, len(favCat.ChannelIDs))
		for _, id := range favCat.ChannelIDs {
			seen[id] = true
		}
		favRows, err := s.db.Pool.Query(ctx, `
			SELECT name FROM preferences
			WHERE user_id=$1 AND category='favorite_channel' AND value='true'
		`, userID)
		if err != nil {
			return err
		}
		defer favRows.Close()
		for favRows.Next() {
			var chanID string
			if err := favRows.Scan(&chanID); err != nil {
				return err
			}
			if !seen[chanID] {
				favCat.ChannelIDs = append(favCat.ChannelIDs, chanID)
				seen[chanID] = true
			}
		}
	}

	return nil
}

// Get returns a single category with its channel IDs filled.
func (s *Service) Get(ctx context.Context, userID, teamID, categoryID string) (*Category, error) {
	var c Category
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, user_id, team_id, type, display_name, sort_order, sorting, muted, collapsed
		FROM sidebar_categories
		WHERE id=$1 AND user_id=$2 AND team_id=$3
	`, categoryID, userID, teamID).Scan(&c.ID, &c.UserID, &c.TeamID, &c.Type, &c.DisplayName,
		&c.SortOrder, &c.Sorting, &c.Muted, &c.Collapsed)
	if err != nil {
		return nil, err
	}
	c.ChannelIDs = []string{}
	cats := []Category{c}
	if err := s.fillChannelIDs(ctx, userID, teamID, cats); err != nil {
		return nil, err
	}
	return &cats[0], nil
}

// Create makes a new custom category. Mattermost only allows the client to
// create custom categories; the three defaults are minted automatically.
// `channelIDs` may be empty.
func (s *Service) Create(ctx context.Context, userID, teamID, displayName string, channelIDs []string) (*Category, error) {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return nil, errors.New("sidebar: display_name required")
	}
	now := time.Now().UnixMilli()
	id := uuid.NewString()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// New custom categories appear at the end of the existing list. We read
	// MAX(sort_order)+1 so the client doesn't need to compute it.
	var maxOrder int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(sort_order), 0) FROM sidebar_categories
		WHERE user_id=$1 AND team_id=$2
	`, userID, teamID).Scan(&maxOrder); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO sidebar_categories (id, user_id, team_id, type, display_name, sort_order, sorting, muted, collapsed, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,FALSE,FALSE,$8,$8)
	`, id, userID, teamID, TypeCustom, displayName, maxOrder+10, SortingManual, now); err != nil {
		return nil, err
	}
	if err := s.replaceChannelsTx(ctx, tx, id, channelIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, teamID, id)
}

// Update mutates display_name / sorting / muted / collapsed / channel_ids on
// an existing category. Channel membership is replaced wholesale because
// Mattermost's PUT contract is "here is the new full list" rather than a
// diff. ChannelIDs ownership transfer is handled inside the same tx so a
// drag from "channels" → "custom" doesn't leave the channel listed under
// both for any observer.
func (s *Service) Update(ctx context.Context, userID, teamID string, cat Category) (*Category, error) {
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE sidebar_categories
		SET display_name=$1, sorting=$2, muted=$3, collapsed=$4, update_at=$5
		WHERE id=$6 AND user_id=$7 AND team_id=$8
	`, cat.DisplayName, cat.Sorting, cat.Muted, cat.Collapsed, now,
		cat.ID, userID, teamID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	if err := s.replaceChannelsTx(ctx, tx, cat.ID, cat.ChannelIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, teamID, cat.ID)
}

// replaceChannelsTx swaps a category's channel membership inside a tx.
// Channels added here are first removed from any *other* category for the
// same user/team so a channel never ends up double-listed; this matches
// Mattermost's drag-drop reorder semantics.
func (s *Service) replaceChannelsTx(ctx context.Context, tx pgx.Tx, categoryID string, channelIDs []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM sidebar_category_channels WHERE category_id=$1`, categoryID); err != nil {
		return err
	}
	if len(channelIDs) == 0 {
		return nil
	}
	// Strip these channels from *other* categories belonging to the same
	// user/team so a channel can only live in one category at a time.
	if _, err := tx.Exec(ctx, `
		DELETE FROM sidebar_category_channels
		WHERE channel_id = ANY($1)
		  AND category_id IN (
		      SELECT id FROM sidebar_categories
		      WHERE (user_id, team_id) = (
		          SELECT user_id, team_id FROM sidebar_categories WHERE id=$2
		      )
		  )
	`, channelIDs, categoryID); err != nil {
		return err
	}
	for i, chanID := range channelIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO sidebar_category_channels (category_id, channel_id, sort_order)
			VALUES ($1,$2,$3)
			ON CONFLICT (category_id, channel_id) DO UPDATE SET sort_order=EXCLUDED.sort_order
		`, categoryID, chanID, i*10); err != nil {
			return err
		}
	}
	return nil
}

// UpdateOrder rewrites the sort_order of every category in `order` so it
// matches the slice index. Categories not present in `order` keep their
// existing sort_order (so a partial reorder doesn't smash unmentioned rows).
func (s *Service) UpdateOrder(ctx context.Context, userID, teamID string, order []string) error {
	if len(order) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for i, id := range order {
		if _, err := tx.Exec(ctx, `
			UPDATE sidebar_categories
			SET sort_order=$1, update_at=$2
			WHERE id=$3 AND user_id=$4 AND team_id=$5
		`, i*10, now, id, userID, teamID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Delete removes a custom category. The three defaults
// (favorites/channels/direct_messages) cannot be deleted; an attempt returns
// an error. Channel memberships in the deleted category cascade and reappear
// under the appropriate default on next list.
func (s *Service) Delete(ctx context.Context, userID, teamID, categoryID string) error {
	var typ string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT type FROM sidebar_categories WHERE id=$1 AND user_id=$2 AND team_id=$3
	`, categoryID, userID, teamID).Scan(&typ)
	if err != nil {
		return err
	}
	if typ != TypeCustom {
		return errors.New("sidebar: only custom categories can be deleted")
	}
	_, err = s.db.Pool.Exec(ctx, `DELETE FROM sidebar_categories WHERE id=$1`, categoryID)
	return err
}

// Order returns the ordered list of category IDs only — used to back the
// `GET /users/{id}/teams/{tid}/channels/categories/order` endpoint.
func (s *Service) Order(ctx context.Context, userID, teamID string) ([]string, error) {
	if err := s.ensureDefaults(ctx, userID, teamID); err != nil {
		return nil, err
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id FROM sidebar_categories
		WHERE user_id=$1 AND team_id=$2
		ORDER BY sort_order ASC, create_at ASC
	`, userID, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
