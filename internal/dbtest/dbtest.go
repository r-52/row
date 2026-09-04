// Package dbtest runs one test body against every database row supports.
//
// A single suite that executes on both engines is the only way to keep the
// dialects honest: a behaviour that works on SQLite and not on Postgres is a
// bug in row, and it should be a test failure rather than a surprise in
// production.
package dbtest

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/r-52/row"
	"github.com/r-52/row/pg"
	"github.com/r-52/row/sqlite"
)

// DefaultPGDSN points at the Postgres started by the repository's
// docker-compose.yml. Override it with ROW_PG_DSN.
const DefaultPGDSN = "postgres://row:row@localhost:55432/row_test?sslmode=disable"

// Engine is one database under test.
type Engine struct {
	Name    string
	Dialect row.Dialect

	// Schema is the DDL that creates the fixture tables, in this engine's
	// spelling. Statements are separated by ";\n\n".
	Schema string
}

// pgDSN reports the Postgres DSN to use.
func pgDSN() string {
	if v := os.Getenv("ROW_PG_DSN"); v != "" {
		return v
	}
	return DefaultPGDSN
}

const sqliteSchema = `
CREATE TABLE orgs (
	id   INTEGER PRIMARY KEY,
	name TEXT NOT NULL
);

CREATE TABLE users (
	id         INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL,
	email      TEXT    NOT NULL UNIQUE,
	age        INTEGER NOT NULL,
	score      REAL    NOT NULL,
	active     BOOLEAN NOT NULL,
	data       BLOB,
	bio        TEXT,
	org_id     INTEGER REFERENCES orgs(id),
	created_at TIMESTAMP NOT NULL
);

CREATE TABLE counters (
	name  TEXT PRIMARY KEY,
	value INTEGER NOT NULL CHECK (value >= 0)
)`

const pgSchema = `
CREATE TABLE orgs (
	id   BIGINT PRIMARY KEY,
	name TEXT NOT NULL
);

CREATE TABLE users (
	id         BIGINT PRIMARY KEY,
	name       TEXT   NOT NULL,
	email      TEXT   NOT NULL UNIQUE,
	age        INTEGER NOT NULL,
	score      DOUBLE PRECISION NOT NULL,
	active     BOOLEAN NOT NULL,
	data       BYTEA,
	bio        TEXT,
	org_id     BIGINT REFERENCES orgs(id),
	created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE counters (
	name  TEXT PRIMARY KEY,
	value BIGINT NOT NULL CHECK (value >= 0)
)`

// Each runs body once per available engine, as a subtest named after it.
//
// The database handed to body is empty, has the fixture schema applied, and is
// discarded afterwards.
//
// SQLite always runs. Postgres runs when one is reachable; when it is not, the
// subtest is skipped with an explanation unless ROW_PG_REQUIRED is set, in
// which case it fails. CI sets ROW_PG_REQUIRED so a missing container can never
// quietly reduce coverage.
func Each(t *testing.T, body func(t *testing.T, db *row.DB), opts ...row.Option) {
	t.Helper()

	t.Run("sqlite", func(t *testing.T) {
		db := OpenSQLite(t, opts...)
		body(t, db)
	})

	t.Run("postgres", func(t *testing.T) {
		db := OpenPG(t, opts...)
		if db == nil {
			return
		}
		body(t, db)
	})
}

// OpenSQLite returns a fresh in-memory SQLite database with the fixture schema.
func OpenSQLite(t *testing.T, opts ...row.Option) *row.DB {
	t.Helper()
	ctx := context.Background()

	db, err := sqlite.Open(ctx, ":memory:", opts...)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	applySchema(t, db, sqliteSchema)
	return db
}

// OpenSQLiteB is OpenSQLite for a benchmark. testing.B and testing.T share no
// interface with both Fatalf and Cleanup on it, so this is a small duplicate
// rather than a generic helper with a reflective escape hatch.
func OpenSQLiteB(b *testing.B, opts ...row.Option) *row.DB {
	b.Helper()
	ctx := context.Background()

	db, err := sqlite.Open(ctx, ":memory:", opts...)
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	b.Cleanup(func() { db.Close() })

	for _, stmt := range strings.Split(sqliteSchema, ";\n\n") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := row.Exec(ctx, db, stmt); err != nil {
			b.Fatalf("apply schema: %v", err)
		}
	}
	return db
}

// OpenPG returns a Postgres database with the fixture schema, or nil after
// skipping the test when no server is reachable.
func OpenPG(t *testing.T, opts ...row.Option) *row.DB {
	t.Helper()
	ctx := context.Background()

	db, err := pg.Open(ctx, pgDSN(), opts...)
	if err != nil {
		if os.Getenv("ROW_PG_REQUIRED") != "" {
			t.Fatalf("ROW_PG_REQUIRED is set but Postgres at %s is unreachable: %v", pgDSN(), err)
		}
		t.Skipf("no Postgres at %s (%v); start one with `make pg-up`", pgDSN(), err)
		return nil
	}
	t.Cleanup(func() { db.Close() })

	// Each test gets the fixture tables to itself. Dropping first makes a run
	// recoverable after a previous failure left tables behind.
	dropPG(t, db)
	t.Cleanup(func() { dropPG(t, db) })
	applySchema(t, db, pgSchema)
	return db
}

func dropPG(t *testing.T, db *row.DB) {
	t.Helper()
	for _, tbl := range []string{"users", "orgs", "counters"} {
		if _, err := row.Exec(context.Background(), db, "DROP TABLE IF EXISTS "+tbl+" CASCADE"); err != nil {
			t.Fatalf("drop %s: %v", tbl, err)
		}
	}
}

func applySchema(t *testing.T, db *row.DB, schema string) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range strings.Split(schema, ";\n\n") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := row.Exec(ctx, db, stmt); err != nil {
			t.Fatalf("apply schema: %v\n%s", err, stmt)
		}
	}
}
