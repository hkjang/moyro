// Package customprofile implements Mattermost's "custom profile attributes"
// feature: a global set of admin-defined fields (text/select/url/date/etc.)
// that every user can fill in. Two storage tables — custom_profile_fields
// holds the global field definitions, custom_profile_values holds per-user
// value rows. Values are stored as raw JSONB so a future field-type
// addition round-trips without a migration.
package customprofile

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hkjang/moyro/server/internal/store"
)

var ErrFieldNotFound = errors.New("custom profile field not found")

type Field struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	TargetID   string          `json:"target_id,omitempty"`
	TargetType string          `json:"target_type,omitempty"`
	Attrs      json.RawMessage `json:"attrs"`
	SortOrder  int             `json:"sort_order"`
	CreateAt   int64           `json:"create_at"`
	UpdateAt   int64           `json:"update_at"`
	DeleteAt   int64           `json:"delete_at"`
}

type Service struct{ db *store.DB }

func New(db *store.DB) *Service { return &Service{db: db} }

// ListFields returns every active field definition ordered by sort_order
// asc, then create_at asc as the deterministic tie-break.
func (s *Service) ListFields(ctx context.Context) ([]Field, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, name, type, target_id, target_type, attrs, sort_order, create_at, update_at, delete_at
		FROM custom_profile_fields
		WHERE delete_at=0
		ORDER BY sort_order ASC, create_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Field{}
	for rows.Next() {
		var f Field
		if err := rows.Scan(&f.ID, &f.Name, &f.Type, &f.TargetID, &f.TargetType, &f.Attrs, &f.SortOrder, &f.CreateAt, &f.UpdateAt, &f.DeleteAt); err != nil {
			return nil, err
		}
		if len(f.Attrs) == 0 {
			f.Attrs = json.RawMessage("{}")
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetField returns a single field row by id. Returns ErrFieldNotFound for
// missing/deleted rows so the handler can 404 without leaking pgx errors.
func (s *Service) GetField(ctx context.Context, id string) (*Field, error) {
	var f Field
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, name, type, target_id, target_type, attrs, sort_order, create_at, update_at, delete_at
		FROM custom_profile_fields
		WHERE id=$1 AND delete_at=0
	`, id).Scan(&f.ID, &f.Name, &f.Type, &f.TargetID, &f.TargetType, &f.Attrs, &f.SortOrder, &f.CreateAt, &f.UpdateAt, &f.DeleteAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrFieldNotFound
		}
		return nil, err
	}
	if len(f.Attrs) == 0 {
		f.Attrs = json.RawMessage("{}")
	}
	return &f, nil
}

// CreateField inserts a fresh field definition. Auto-defaults sort_order
// to MAX+1 so a new field appends to the end of the existing form.
func (s *Service) CreateField(ctx context.Context, name, typ string, attrs json.RawMessage) (*Field, error) {
	if name == "" {
		return nil, errors.New("field name required")
	}
	if typ == "" {
		typ = "text"
	}
	if len(attrs) == 0 {
		attrs = json.RawMessage("{}")
	}
	id := uuid.NewString()
	now := time.Now().UnixMilli()
	var sortOrder int
	_ = s.db.Pool.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order), 0) + 1 FROM custom_profile_fields WHERE delete_at=0`).Scan(&sortOrder)
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO custom_profile_fields (id, name, type, target_id, target_type, attrs, sort_order, create_at, update_at, delete_at)
		VALUES ($1, $2, $3, '', 'user', $4, $5, $6, $6, 0)
	`, id, name, typ, attrs, sortOrder, now)
	if err != nil {
		return nil, err
	}
	return s.GetField(ctx, id)
}

// PatchField is partial-update. Pointer fields (nil = leave alone). Atomic
// CASE-WHEN update so concurrent patches don't read-modify-write each
// other's state.
func (s *Service) PatchField(ctx context.Context, id string, name, typ *string, attrs json.RawMessage, sortOrder *int) (*Field, error) {
	now := time.Now().UnixMilli()
	updateName := name != nil
	updateType := typ != nil
	updateAttrs := attrs != nil
	updateSort := sortOrder != nil
	var nameVal, typeVal string
	var sortVal int
	if updateName {
		nameVal = *name
	}
	if updateType {
		typeVal = *typ
	}
	if updateSort {
		sortVal = *sortOrder
	}
	if !updateAttrs {
		attrs = json.RawMessage(`{}`)
	}
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE custom_profile_fields SET
			name       = CASE WHEN $2 THEN $3 ELSE name       END,
			type       = CASE WHEN $4 THEN $5 ELSE type       END,
			attrs      = CASE WHEN $6 THEN $7::jsonb ELSE attrs END,
			sort_order = CASE WHEN $8 THEN $9 ELSE sort_order END,
			update_at  = $10
		WHERE id=$1 AND delete_at=0
	`, id, updateName, nameVal, updateType, typeVal, updateAttrs, attrs, updateSort, sortVal, now)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrFieldNotFound
	}
	return s.GetField(ctx, id)
}

// DeleteField soft-deletes the field. Existing value rows stay (cascade
// FK fires only when the field row is hard-deleted) so audits can still
// resolve the field name retroactively.
func (s *Service) DeleteField(ctx context.Context, id string) error {
	now := time.Now().UnixMilli()
	tag, err := s.db.Pool.Exec(ctx, `UPDATE custom_profile_fields SET delete_at=$2, update_at=$2 WHERE id=$1 AND delete_at=0`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrFieldNotFound
	}
	return nil
}

// GetUserValues returns the user's value blob: map of field_id → value.
// Missing fields are simply absent from the map (not present-as-null) so
// the client can apply its own defaults.
func (s *Service) GetUserValues(ctx context.Context, userID string) (map[string]json.RawMessage, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT field_id, value FROM custom_profile_values WHERE user_id=$1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var fid string
		var raw json.RawMessage
		if err := rows.Scan(&fid, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			raw = json.RawMessage("null")
		}
		out[fid] = raw
	}
	return out, rows.Err()
}

// PatchUserValues writes the incoming map. nil/null values delete the row;
// non-null values upsert. Runs in a single transaction so a partial write
// never lands in the DB.
func (s *Service) PatchUserValues(ctx context.Context, userID string, values map[string]json.RawMessage) error {
	if len(values) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for fid, raw := range values {
		if len(raw) == 0 || string(raw) == "null" {
			if _, err := tx.Exec(ctx, `DELETE FROM custom_profile_values WHERE user_id=$1 AND field_id=$2`, userID, fid); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO custom_profile_values (user_id, field_id, value, update_at)
			VALUES ($1, $2, $3::jsonb, $4)
			ON CONFLICT (user_id, field_id) DO UPDATE SET value=EXCLUDED.value, update_at=EXCLUDED.update_at
		`, userID, fid, raw, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
