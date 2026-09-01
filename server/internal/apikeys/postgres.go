package apikeys

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgres(pool *pgxpool.Pool, digester Digester, validator GrantValidator, opts Options) (*Service, error) {
	if pool == nil {
		return nil, errors.New("apikeys: nil postgres pool")
	}
	return New(&PostgresRepository{pool: pool}, digester, validator, opts)
}

func (r *PostgresRepository) Create(ctx context.Context, key Key, digest []byte) (Key, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Key{}, err
	}
	defer tx.Rollback(ctx)
	if err := insertKey(ctx, tx, key, digest); err != nil {
		return Key{}, err
	}
	if err := replacePermissions(ctx, tx, key.ID, key.Permissions); err != nil {
		return Key{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Key{}, err
	}
	return key, nil
}

func (r *PostgresRepository) GetForOwner(ctx context.Context, keyID, ownerID string) (Key, error) {
	key, err := scanKey(r.pool.QueryRow(ctx, keyQuery+`
		JOIN users u ON u.id=k.owner_user_id AND u.delete_at=0
		  AND (
			u.roles !~ '(^|[[:space:]])system_guest([[:space:]]|$)'
			OR u.guest_expires_at > (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT
		  )
		WHERE k.id=$1 AND k.owner_user_id=$2 GROUP BY k.id
	`, keyID, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, ErrNotFound
	}
	return key, err
}

func (r *PostgresRepository) ListForOwner(ctx context.Context, ownerID string) ([]Key, error) {
	rows, err := r.pool.Query(ctx, keyQuery+` WHERE k.owner_user_id=$1 GROUP BY k.id ORDER BY k.create_at DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		key, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ResolveByDigest(ctx context.Context, digest []byte) (Key, error) {
	key, err := scanKey(r.pool.QueryRow(ctx, keyQuery+`
		JOIN users u ON u.id=k.owner_user_id AND u.delete_at=0
		  AND (
			u.roles !~ '(^|[[:space:]])system_guest([[:space:]]|$)'
			OR u.guest_expires_at > (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT
		  )
		WHERE k.secret_hash=$1 GROUP BY k.id
	`, digest))
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, ErrInvalidCredential
	}
	return key, err
}

func (r *PostgresRepository) Rotate(ctx context.Context, oldKeyID, ownerID string, replacement Key, replacementDigest []byte, graceUntil, now int64) (Key, Key, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Key{}, Key{}, err
	}
	defer tx.Rollback(ctx)

	var lockedStatus string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM api_keys WHERE id=$1 AND owner_user_id=$2 FOR UPDATE
	`, oldKeyID, ownerID).Scan(&lockedStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Key{}, Key{}, ErrNotFound
		}
		return Key{}, Key{}, err
	}
	old, err := scanKey(tx.QueryRow(ctx, keyQuery+`
		WHERE k.id=$1 AND k.owner_user_id=$2 GROUP BY k.id
	`, oldKeyID, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, Key{}, ErrNotFound
	}
	if err != nil {
		return Key{}, Key{}, err
	}
	if lockedStatus != StatusActive || old.Status != StatusActive || old.RevokedAt != 0 || (old.ExpiresAt != 0 && old.ExpiresAt <= now) {
		return Key{}, Key{}, ErrRotationConflict
	}
	// Hydrate policy-bearing fields from the locked row. A concurrent scope or
	// permission edit that won the lock before rotation must be inherited by
	// the replacement instead of the stale preflight snapshot.
	replacement.OwnerUserID = old.OwnerUserID
	replacement.Name = old.Name
	replacement.Description = old.Description
	replacement.Kind = old.Kind
	replacement.Constraints = old.Constraints
	replacement.ExpiresAt = old.ExpiresAt
	replacement.RotationGroupID = firstNonEmpty(old.RotationGroupID, old.ID)
	replacement.Version = old.Version + 1
	replacement.RotatedFromID = old.ID

	old.Status = StatusRetiring
	old.ValidUntil = graceUntil
	old.UpdateAt = now
	old.Revision++
	tag, err := tx.Exec(ctx, `
		UPDATE api_keys
		   SET status=$3, valid_until=$4, update_at=$5, revision=revision+1
		 WHERE id=$1 AND owner_user_id=$2 AND status='active' AND revoked_at=0
	`, oldKeyID, ownerID, StatusRetiring, graceUntil, now)
	if err != nil {
		return Key{}, Key{}, err
	}
	if tag.RowsAffected() != 1 {
		return Key{}, Key{}, ErrRotationConflict
	}
	if err := insertKey(ctx, tx, replacement, replacementDigest); err != nil {
		return Key{}, Key{}, err
	}
	if err := replacePermissions(ctx, tx, replacement.ID, old.Permissions); err != nil {
		return Key{}, Key{}, err
	}
	replacement.Permissions = append([]string(nil), old.Permissions...)
	if err := tx.Commit(ctx); err != nil {
		return Key{}, Key{}, err
	}
	return old, replacement, nil
}

func (r *PostgresRepository) ReplacePermissions(ctx context.Context, keyID, ownerID string, permissions []string, constraints Constraints, updateAt int64, expectedRevision *int64) (Key, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Key{}, err
	}
	defer tx.Rollback(ctx)
	var revision int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT revision, status FROM api_keys WHERE id=$1 AND owner_user_id=$2 FOR UPDATE
	`, keyID, ownerID).Scan(&revision, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Key{}, ErrNotFound
		}
		return Key{}, err
	}
	if status == StatusRevoked {
		return Key{}, ErrNotFound
	}
	if expectedRevision != nil && *expectedRevision != revision {
		return Key{}, ErrRevisionConflict
	}
	if err := replacePermissions(ctx, tx, keyID, permissions); err != nil {
		return Key{}, err
	}
	rawConstraints, err := json.Marshal(canonicalConstraints(constraints))
	if err != nil {
		return Key{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE api_keys SET constraints=$3::jsonb, update_at=$4, revision=revision+1
		WHERE id=$1 AND owner_user_id=$2
	`, keyID, ownerID, string(rawConstraints), updateAt); err != nil {
		return Key{}, err
	}
	key, err := scanKey(tx.QueryRow(ctx, keyQuery+` WHERE k.id=$1 GROUP BY k.id`, keyID))
	if err != nil {
		return Key{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Key{}, err
	}
	return key, nil
}

func (r *PostgresRepository) Revoke(ctx context.Context, keyID, ownerID string, revokedAt int64) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE api_keys
		   SET status='revoked', revoked_at=$3, valid_until=0,
		       update_at=$3, revision=revision+1
		 WHERE id=$1 AND owner_user_id=$2 AND status<>'revoked'
	`, keyID, ownerID, revokedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) MarkUsed(ctx context.Context, keyID string, usedAt int64) error {
	// At most one write per minute per key to avoid turning a hot API key into
	// a database write bottleneck.
	_, err := r.pool.Exec(ctx, `
		UPDATE api_keys SET last_used_at=$2
		 WHERE id=$1 AND last_used_at < $2-60000
	`, keyID, usedAt)
	return err
}

const keyQuery = `
	SELECT k.id, k.owner_user_id, k.name, k.description, k.key_prefix,
	       k.kind, k.status, k.constraints::text, k.expires_at, k.valid_until,
	       k.rotation_group_id, k.version, COALESCE(k.rotated_from_id,''),
	       k.created_by, k.create_at, k.update_at, k.last_used_at, k.revoked_at,
	       k.revision,
	       COALESCE(array_agg(akp.permission_name ORDER BY akp.permission_name)
	           FILTER (WHERE akp.permission_name IS NOT NULL), '{}')
	  FROM api_keys k
	  LEFT JOIN api_key_permissions akp ON akp.api_key_id=k.id`

type scanner interface{ Scan(...any) error }

func scanKey(row scanner) (Key, error) {
	var key Key
	var rawConstraints string
	err := row.Scan(
		&key.ID, &key.OwnerUserID, &key.Name, &key.Description, &key.Prefix,
		&key.Kind, &key.Status, &rawConstraints, &key.ExpiresAt, &key.ValidUntil,
		&key.RotationGroupID, &key.Version, &key.RotatedFromID, &key.CreatedBy,
		&key.CreateAt, &key.UpdateAt, &key.LastUsedAt, &key.RevokedAt, &key.Revision,
		&key.Permissions,
	)
	if err != nil {
		return Key{}, err
	}
	if rawConstraints != "" {
		if err := json.Unmarshal([]byte(rawConstraints), &key.Constraints); err != nil {
			return Key{}, err
		}
	}
	return key, nil
}

func insertKey(ctx context.Context, tx pgx.Tx, key Key, digest []byte) error {
	rawConstraints, err := json.Marshal(canonicalConstraints(key.Constraints))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO api_keys
		    (id, owner_user_id, name, description, key_prefix, secret_hash,
		     kind, status, constraints, expires_at, valid_until,
		     rotation_group_id, version, rotated_from_id, created_by,
		     create_at, update_at, last_used_at, revoked_at, revision)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12,$13,NULLIF($14,''),
		        $15,$16,$17,$18,$19,$20)
	`, key.ID, key.OwnerUserID, key.Name, key.Description, key.Prefix, digest,
		key.Kind, key.Status, string(rawConstraints), key.ExpiresAt, key.ValidUntil,
		key.RotationGroupID, key.Version, key.RotatedFromID, key.CreatedBy,
		key.CreateAt, key.UpdateAt, key.LastUsedAt, key.RevokedAt, key.Revision)
	return err
}

func replacePermissions(ctx context.Context, tx pgx.Tx, keyID string, permissions []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM api_key_permissions WHERE api_key_id=$1`, keyID); err != nil {
		return err
	}
	if len(permissions) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO api_key_permissions (api_key_id, permission_name)
		SELECT $1, unnest($2::text[])
	`, keyID, permissions)
	return err
}
