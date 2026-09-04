package sqlite_test

import (
	"context"
	"strings"
	"testing"

	"github.com/r-52/row"
	"github.com/r-52/row/sqlite"
)

func TestDialectBasics(t *testing.T) {
	d := sqlite.Dialect
	if d.Name() != "sqlite" {
		t.Errorf("Name = %q", d.Name())
	}
	if d.DriverName() != "sqlite" {
		t.Errorf("DriverName = %q", d.DriverName())
	}
	// SQLite binds positionally, so every placeholder is the same.
	if got := string(d.AppendPlaceholder([]byte("a="), 12)); got != "a=?" {
		t.Errorf("placeholder = %q", got)
	}
	f := d.Features()
	if !f.Returning || !f.Savepoints || !f.Upsert {
		t.Errorf("features = %+v", f)
	}
	if f.MaxPlaceholders != 32766 {
		t.Errorf("MaxPlaceholders = %d", f.MaxPlaceholders)
	}
}

// The result codes are asserted against a live database rather than copied
// from a header file.
func TestClassifyErrorAgainstLiveDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := row.Exec(ctx, db, `
		CREATE TABLE parent (id INTEGER PRIMARY KEY);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := row.Exec(ctx, db, `
		CREATE TABLE t (
			id    INTEGER PRIMARY KEY,
			uniq  TEXT UNIQUE,
			nn    TEXT NOT NULL,
			chk   INTEGER CHECK (chk > 0),
			pid   INTEGER REFERENCES parent(id)
		)`); err != nil {
		t.Fatal(err)
	}
	if _, err := row.Exec(ctx, db, `INSERT INTO t (id, uniq, nn, chk) VALUES (1, 'a', 'x', 1)`); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		stmt string
		want row.Code
	}{
		{"primary key", `INSERT INTO t (id, uniq, nn, chk) VALUES (1, 'b', 'x', 1)`, row.UniqueViolation},
		{"unique", `INSERT INTO t (id, uniq, nn, chk) VALUES (2, 'a', 'x', 1)`, row.UniqueViolation},
		{"not null", `INSERT INTO t (id, uniq, nn, chk) VALUES (3, 'c', NULL, 1)`, row.NotNullViolation},
		{"check", `INSERT INTO t (id, uniq, nn, chk) VALUES (4, 'd', 'x', 0)`, row.CheckViolation},
		{"foreign key", `INSERT INTO t (id, uniq, nn, chk, pid) VALUES (5, 'e', 'x', 1, 99)`, row.ForeignKeyViolation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := row.Exec(ctx, db, c.stmt)
			if err == nil {
				t.Fatal("expected a constraint failure")
			}
			if got := row.CodeOf(err); got != c.want {
				t.Errorf("code = %v, want %v (err: %v)", got, c.want, err)
			}
		})
	}
}

// Open applies the pragmas a server-side application wants and SQLite does not
// enable on its own.
func TestOpenAppliesDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mode, err := row.One[string](ctx, db, `PRAGMA journal_mode`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	fk, err := row.One[int64](ctx, db, `PRAGMA foreign_keys`)
	if err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}

	busy, err := row.One[int64](ctx, db, `PRAGMA busy_timeout`)
	if err != nil {
		t.Fatal(err)
	}
	if busy != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busy)
	}
}

// Raw applies nothing, leaving the DSN exactly as given.
func TestRawAppliesNoDefaults(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Raw(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fk, err := row.One[int64](ctx, db, `PRAGMA foreign_keys`)
	if err != nil {
		t.Fatal(err)
	}
	if fk != 0 {
		t.Errorf("foreign_keys = %d; Raw should not have enabled it", fk)
	}
}

// A caller's own pragma must win over row's default.
func TestOpenRespectsCallerPragmas(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir()+"/test.db?_pragma=busy_timeout(1234)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	busy, err := row.One[int64](ctx, db, `PRAGMA busy_timeout`)
	if err != nil {
		t.Fatal(err)
	}
	if busy != 1234 {
		t.Errorf("busy_timeout = %d, want the caller's 1234", busy)
	}
}
