package audit

import (
	"context"
	"database/sql"
)

// rebindDB wraps a *sql.DB so every query executed through it has '?'
// placeholders automatically rewritten to Postgres's '$1', '$2', ... when
// the backing store is Postgres (rebind is a no-op on SQLite). Every store
// in this package holds one of these instead of a raw *sql.DB specifically
// so a store author can never forget to call rebind() at a given call site
// — a real bug found live during v0.30 Postgres-backend validation: roughly
// a third of this package's parameterized queries had never actually been
// exercised against Postgres and used bare '?' placeholders its driver
// doesn't understand. Calling rebind() twice on an already-rebound query is
// harmless (no '?' remain to convert), so this wrapper is safe to layer on
// top of call sites that already rebind explicitly.
type rebindDB struct {
	*sql.DB
	isPostgres bool
}

func newRebindDB(db *sql.DB, isPostgres bool) *rebindDB {
	return &rebindDB{DB: db, isPostgres: isPostgres}
}

func (d *rebindDB) Exec(query string, args ...any) (sql.Result, error) {
	return d.DB.Exec(rebind(d.isPostgres, query), args...)
}

func (d *rebindDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.DB.ExecContext(ctx, rebind(d.isPostgres, query), args...)
}

func (d *rebindDB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.DB.Query(rebind(d.isPostgres, query), args...)
}

func (d *rebindDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, rebind(d.isPostgres, query), args...)
}

func (d *rebindDB) QueryRow(query string, args ...any) *sql.Row {
	return d.DB.QueryRow(rebind(d.isPostgres, query), args...)
}

func (d *rebindDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, rebind(d.isPostgres, query), args...)
}

// BeginTx starts a transaction whose Exec/Query/QueryRow methods rebind the
// same way, so a store using tx.ExecContext etc. gets the same guarantee
// without needing to know it's inside a transaction.
func (d *rebindDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*rebindTx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &rebindTx{Tx: tx, isPostgres: d.isPostgres}, nil
}

// rebindTx is rebindDB's transaction-scoped counterpart. Commit/Rollback are
// promoted from the embedded *sql.Tx unchanged.
type rebindTx struct {
	*sql.Tx
	isPostgres bool
}

func (t *rebindTx) Exec(query string, args ...any) (sql.Result, error) {
	return t.Tx.Exec(rebind(t.isPostgres, query), args...)
}

func (t *rebindTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, rebind(t.isPostgres, query), args...)
}

func (t *rebindTx) Query(query string, args ...any) (*sql.Rows, error) {
	return t.Tx.Query(rebind(t.isPostgres, query), args...)
}

func (t *rebindTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, rebind(t.isPostgres, query), args...)
}

func (t *rebindTx) QueryRow(query string, args ...any) *sql.Row {
	return t.Tx.QueryRow(rebind(t.isPostgres, query), args...)
}

func (t *rebindTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, rebind(t.isPostgres, query), args...)
}
