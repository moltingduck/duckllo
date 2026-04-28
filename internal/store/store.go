// Package store is the data-access layer. Each entity lives in its own
// file. All methods take an explicit pgxpool.Pool (or pgx.Tx for txn-scoped
// work) so handlers can compose read/write paths without leaking ORM
// semantics into the routes.
package store

import "github.com/jackc/pgx/v5/pgxpool"

type Store struct {
	Pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool}
}
