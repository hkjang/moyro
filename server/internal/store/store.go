package store

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{Pool: pool}, nil
}

func (d *DB) Close() {
	if d.Pool != nil {
		d.Pool.Close()
	}
}

//go:embed schema.sql
var schemaSQL string

func Migrate(ctx context.Context, db *DB) error {
	if db == nil || db.Pool == nil {
		return errors.New("store: nil db")
	}
	_, err := db.Pool.Exec(ctx, schemaSQL)
	return err
}
