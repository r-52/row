package pg_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/r-52/row"
	"github.com/r-52/row/pg"
)

func TestDialectBasics(t *testing.T) {
	d := pg.Dialect
	if d.Name() != "postgres" {
		t.Errorf("Name = %q", d.Name())
	}
	if d.DriverName() != "pgx" {
		t.Errorf("DriverName = %q", d.DriverName())
	}
	if got := string(d.AppendPlaceholder([]byte("a="), 12)); got != "a=$12" {
		t.Errorf("placeholder = %q", got)
	}
	if got := d.QuoteIdent(`we"ird`); got != `"we""ird"` {
		t.Errorf("QuoteIdent = %s", got)
	}
	f := d.Features()
	if !f.Returning || !f.Savepoints || !f.Upsert {
		t.Errorf("features = %+v", f)
	}
	if f.MaxPlaceholders != 65535 {
		t.Errorf("MaxPlaceholders = %d, want the protocol's int16 limit", f.MaxPlaceholders)
	}
}

func TestClassifyErrorTable(t *testing.T) {
	cases := map[string]row.Code{
		"23505": row.UniqueViolation,
		"23503": row.ForeignKeyViolation,
		"23502": row.NotNullViolation,
		"23514": row.CheckViolation,
		"40P01": row.Deadlock,
		"40001": row.SerializationFailure,
		"55P03": row.Busy,
		"57014": row.Timeout,
	}
	for sqlstate, want := range cases {
		err := error(&pgconn.PgError{Code: sqlstate, Message: "test"})
		got, ok := pg.Dialect.ClassifyError(err)
		if !ok || got != want {
			t.Errorf("SQLSTATE %s classified as %v (ok=%v), want %v", sqlstate, got, ok, want)
		}
		// Classification must see through wrapping, which is how the error
		// arrives in practice.
		if got, ok := pg.Dialect.ClassifyError(errors.Join(errors.New("ctx"), err)); !ok || got != want {
			t.Errorf("wrapped SQLSTATE %s classified as %v (ok=%v)", sqlstate, got, ok)
		}
	}
}

func TestClassifyErrorLeavesUnknownAlone(t *testing.T) {
	for _, err := range []error{
		errors.New("plain"),
		&pgconn.PgError{Code: "42P01", Message: "relation does not exist"},
	} {
		if got, ok := pg.Dialect.ClassifyError(err); ok || got != row.Unknown {
			t.Errorf("ClassifyError(%v) = %v, %v; row should not guess", err, got, ok)
		}
	}
}
