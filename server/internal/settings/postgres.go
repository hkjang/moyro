package settings

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
}

func (r *PostgresRepository) PutBatch(ctx context.Context, input []Record) ([]Record, error) {
	if len(input) == 0 {
		return []Record{}, nil
	}
	records := append([]Record(nil), input...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].Section == records[j].Section {
			return records[i].Key < records[j].Key
		}
		return records[i].Section < records[j].Section
	})
	for i := 1; i < len(records); i++ {
		if records[i-1].Section == records[i].Section && records[i-1].Key == records[i].Key {
			return nil, errors.New("settings: duplicate key in batch")
		}
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	for i := range records {
		record := &records[i]
		if _, err := tx.Exec(ctx, `
			SELECT pg_advisory_xact_lock(hashtextextended($1::text || ':' || $2::text, 0))
		`, record.Section, record.Key); err != nil {
			return nil, err
		}
		var current int64
		err := tx.QueryRow(ctx, `
			SELECT revision FROM system_settings
			WHERE section=$1 AND setting_key=$2 FOR UPDATE
		`, record.Section, record.Key).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			current = 0
		} else if err != nil {
			return nil, err
		}
		record.Revision = current + 1
		_, err = tx.Exec(ctx, `
			INSERT INTO system_settings
			    (section, setting_key, value_json, secret_ciphertext, secret_nonce,
			     key_id, revision, updated_by, update_at)
			VALUES ($1,$2,$3::jsonb,$4,$5,NULLIF($6,''),$7,NULLIF($8,''),$9)
			ON CONFLICT (section, setting_key) DO UPDATE SET
			    value_json=EXCLUDED.value_json,
			    secret_ciphertext=EXCLUDED.secret_ciphertext,
			    secret_nonce=EXCLUDED.secret_nonce,
			    key_id=EXCLUDED.key_id,
			    revision=EXCLUDED.revision,
			    updated_by=EXCLUDED.updated_by,
			    update_at=EXCLUDED.update_at
		`, record.Section, record.Key, nullableJSON(record.ValueJSON), nullableBytes(record.Ciphertext),
			nullableBytes(record.Nonce), record.KeyID, record.Revision, record.UpdatedBy, record.UpdatedAt)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return records, nil
}

func NewPostgres(pool *pgxpool.Pool, cipher Cipher) (*Service, error) {
	if pool == nil {
		return nil, errors.New("settings: nil postgres pool")
	}
	return New(&PostgresRepository{pool: pool}, cipher)
}

func (r *PostgresRepository) Get(ctx context.Context, section, key string) (Record, error) {
	var record Record
	err := r.pool.QueryRow(ctx, `
		SELECT section, setting_key, value_json, secret_ciphertext, secret_nonce,
		       COALESCE(key_id,''), revision, COALESCE(updated_by,''), update_at
		FROM system_settings WHERE section=$1 AND setting_key=$2
	`, section, key).Scan(
		&record.Section, &record.Key, &record.ValueJSON, &record.Ciphertext,
		&record.Nonce, &record.KeyID, &record.Revision, &record.UpdatedBy, &record.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return record, err
}

func (r *PostgresRepository) List(ctx context.Context, section string) ([]Record, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT section, setting_key, value_json, secret_ciphertext, secret_nonce,
		       COALESCE(key_id,''), revision, COALESCE(updated_by,''), update_at
		FROM system_settings WHERE section=$1 ORDER BY setting_key
	`, section)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var record Record
		if err := rows.Scan(
			&record.Section, &record.Key, &record.ValueJSON, &record.Ciphertext,
			&record.Nonce, &record.KeyID, &record.Revision, &record.UpdatedBy, &record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) Put(ctx context.Context, record Record, expectedRevision *int64) (Record, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback(ctx)
	// A missing row cannot be locked with FOR UPDATE. A transaction-scoped
	// advisory lock serializes the create path as well as updates, preserving
	// expectedRevision=0 under concurrent first writes.
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1::text || ':' || $2::text, 0))
	`, record.Section, record.Key); err != nil {
		return Record{}, err
	}

	var current int64
	err = tx.QueryRow(ctx, `
		SELECT revision FROM system_settings
		WHERE section=$1 AND setting_key=$2 FOR UPDATE
	`, record.Section, record.Key).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		current = 0
	case err != nil:
		return Record{}, err
	}
	if expectedRevision != nil && *expectedRevision != current {
		return Record{}, ErrRevisionConflict
	}

	record.Revision = current + 1
	_, err = tx.Exec(ctx, `
		INSERT INTO system_settings
		    (section, setting_key, value_json, secret_ciphertext, secret_nonce,
		     key_id, revision, updated_by, update_at)
		VALUES ($1,$2,$3::jsonb,$4,$5,NULLIF($6,''),$7,NULLIF($8,''),$9)
		ON CONFLICT (section, setting_key) DO UPDATE SET
		    value_json=EXCLUDED.value_json,
		    secret_ciphertext=EXCLUDED.secret_ciphertext,
		    secret_nonce=EXCLUDED.secret_nonce,
		    key_id=EXCLUDED.key_id,
		    revision=EXCLUDED.revision,
		    updated_by=EXCLUDED.updated_by,
		    update_at=EXCLUDED.update_at
	`, record.Section, record.Key, nullableJSON(record.ValueJSON), nullableBytes(record.Ciphertext),
		nullableBytes(record.Nonce), record.KeyID, record.Revision, record.UpdatedBy, record.UpdatedAt)
	if err != nil {
		return Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (r *PostgresRepository) Delete(ctx context.Context, section, key string, expectedRevision *int64) error {
	var (
		tag pgconn.CommandTag
		err error
	)
	if expectedRevision == nil {
		tag, err = r.pool.Exec(ctx, `DELETE FROM system_settings WHERE section=$1 AND setting_key=$2`, section, key)
	} else {
		tag, err = r.pool.Exec(ctx, `DELETE FROM system_settings WHERE section=$1 AND setting_key=$2 AND revision=$3`, section, key, *expectedRevision)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if expectedRevision != nil {
			var exists bool
			if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM system_settings WHERE section=$1 AND setting_key=$2)`, section, key).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return ErrRevisionConflict
			}
		}
		return ErrNotFound
	}
	return nil
}

func nullableBytes(value []byte) any {
	if value == nil {
		return nil
	}
	return value
}

func nullableJSON(value []byte) any {
	if value == nil {
		return nil
	}
	return string(value)
}
