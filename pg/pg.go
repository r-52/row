// Package pg adapts row to PostgreSQL through github.com/jackc/pgx/v5.
//
// Importing it registers nothing globally beyond what pgx's stdlib shim
// already does; the root row package stays driver-free so a program that only
// speaks SQLite never links pgx.
package pg

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver

	"github.com/r-52/row"
)

// Dialect is the PostgreSQL dialect. It is stateless and safe to share.
var Dialect row.Dialect = dialect{}

type dialect struct{}

func (dialect) Name() string { return "postgres" }

// DriverName is pgx's database/sql registration.
func (dialect) DriverName() string { return "pgx" }

// AppendPlaceholder emits $1, $2, and so on.
func (dialect) AppendPlaceholder(dst []byte, n int) []byte {
	return row.AppendOrdinal(dst, '$', n)
}

func (dialect) QuoteIdent(name string) string { return row.QuoteWith(name, '"') }

func (dialect) Features() row.Features {
	return row.Features{
		Returning:  true,
		Savepoints: true,
		Upsert:     true,
		// The wire protocol counts parameters in an int16.
		MaxPlaceholders: 65535,
	}
}

// ClassifyError maps PostgreSQL SQLSTATE codes onto row's portable codes.
// The full list is in the PostgreSQL manual, appendix A.
func (dialect) ClassifyError(err error) (row.Code, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return row.Unknown, false
	}
	switch pgErr.Code {
	case "23505": // unique_violation
		return row.UniqueViolation, true
	case "23503": // foreign_key_violation
		return row.ForeignKeyViolation, true
	case "23502": // not_null_violation
		return row.NotNullViolation, true
	case "23514": // check_violation
		return row.CheckViolation, true
	case "40P01": // deadlock_detected
		return row.Deadlock, true
	case "40001": // serialization_failure
		return row.SerializationFailure, true
	case "55P03": // lock_not_available
		return row.Busy, true
	case "57014": // query_canceled
		return row.Timeout, true
	}
	return row.Unknown, false
}

// Open connects to PostgreSQL.
//
// The DSN is anything pgx accepts, in either URL form
// ("postgres://user:pass@host/db") or keyword form ("host=... dbname=...").
// Open verifies the connection before returning, so a bad DSN fails here
// rather than at the first query.
func Open(ctx context.Context, dsn string, opts ...row.Option) (*row.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	rdb := row.New(db, Dialect, opts...)
	if err := rdb.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return rdb, nil
}
