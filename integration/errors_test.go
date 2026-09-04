package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/r-52/row"
	"github.com/r-52/row/internal/dbtest"
)

// Every classification below is asserted against a real server, because the
// mapping from SQLSTATE strings and SQLite result codes to row.Code is exactly
// the kind of table that is easy to get subtly wrong from documentation.

func TestClassifyUniqueViolation(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 1)

		// Duplicate the UNIQUE email.
		_, err := row.Exec(ctx, db, `
			INSERT INTO users (id, name, email, age, score, active, data, bio, org_id, created_at)
			VALUES (99, 'dup', :email, 1, 1.0, true, NULL, NULL, NULL, :at)`,
			row.Args{"email": "ada@example.com", "at": epoch})
		if err == nil {
			t.Fatal("expected a unique violation")
		}
		if !row.IsCode(err, row.UniqueViolation) {
			t.Errorf("code = %v, want UniqueViolation (err: %v)", row.CodeOf(err), err)
		}
	})
}

func TestClassifyPrimaryKeyViolation(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		if err := insertOrg(ctx, db, 1, "acme"); err != nil {
			t.Fatal(err)
		}
		err := insertOrg(ctx, db, 1, "again")
		if !row.IsCode(err, row.UniqueViolation) {
			t.Errorf("code = %v, want UniqueViolation (err: %v)", row.CodeOf(err), err)
		}
	})
}

func TestClassifyForeignKeyViolation(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		missing := int64(404)
		_, err := row.Exec(ctx, db, `
			INSERT INTO users (id, name, email, age, score, active, data, bio, org_id, created_at)
			VALUES (1, 'x', 'x@x.com', 1, 1.0, true, NULL, NULL, :org, :at)`,
			row.Args{"org": missing, "at": epoch})
		if err == nil {
			t.Fatal("expected a foreign key violation; is foreign_keys enforcement on?")
		}
		if !row.IsCode(err, row.ForeignKeyViolation) {
			t.Errorf("code = %v, want ForeignKeyViolation (err: %v)", row.CodeOf(err), err)
		}
	})
}

func TestClassifyNotNullViolation(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		_, err := row.Exec(ctx, db, `INSERT INTO orgs (id, name) VALUES (1, NULL)`)
		if err == nil {
			t.Fatal("expected a not-null violation")
		}
		if !row.IsCode(err, row.NotNullViolation) {
			t.Errorf("code = %v, want NotNullViolation (err: %v)", row.CodeOf(err), err)
		}
	})
}

func TestClassifyCheckViolation(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		_, err := row.Exec(ctx, db,
			`INSERT INTO counters (name, value) VALUES ('c', -1)`)
		if err == nil {
			t.Fatal("expected a check violation")
		}
		if !row.IsCode(err, row.CheckViolation) {
			t.Errorf("code = %v, want CheckViolation (err: %v)", row.CodeOf(err), err)
		}
	})
}

func TestUnrelatedErrorsAreUnclassified(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		_, err := row.Exec(ctx, db, `SELECT * FROM definitely_not_a_table`)
		if err == nil {
			t.Fatal("expected an error")
		}
		if got := row.CodeOf(err); got != row.Unknown {
			t.Errorf("a missing table classified as %v; row should not guess", got)
		}
	})
}

func TestErrorMessageCarriesTheStatement(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		_, err := row.Exec(ctx, db, "SELECT *\n  FROM definitely_not_a_table")
		if err == nil {
			t.Fatal("expected an error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "row.Exec") {
			t.Errorf("error should name the operation: %s", msg)
		}
		if !strings.Contains(msg, "definitely_not_a_table") {
			t.Errorf("error should include the statement: %s", msg)
		}
		// The statement is collapsed onto one line so it stays readable.
		if strings.Contains(msg, "SELECT *\n  FROM") {
			t.Errorf("statement should be whitespace-collapsed: %s", msg)
		}
	})
}
