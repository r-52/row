package integration

import (
	"context"
	"testing"
	"time"

	"github.com/r-52/row"
	"github.com/r-52/row/internal/dbtest"
)

// Org is the joined side of the fixture.
type Org struct {
	ID   int64 `db:"id,pk"`
	Name string
}

// UserWithOrg exercises a nested struct reached through a pointer, populated
// from a LEFT JOIN that may not match.
type UserWithOrg struct {
	ID   int64 `db:"id,pk"`
	Name string
	Org  *Org // columns org_id, org_name
}

func seedOrgs(t *testing.T, db *row.DB) {
	t.Helper()
	ctx := context.Background()
	for _, o := range []Org{{1, "acme"}, {2, "initech"}} {
		if _, err := row.Exec(ctx, db,
			`INSERT INTO orgs (id, name) VALUES (:id, :name)`, o); err != nil {
			t.Fatal(err)
		}
	}
}

func seedUserWithOrg(t *testing.T, db *row.DB, id int64, name string, org *int64) {
	t.Helper()
	_, err := row.Exec(context.Background(), db, `
		INSERT INTO users (id, name, email, age, score, active, data, bio, org_id, created_at)
		VALUES (:id, :name, :email, 1, 1.0, true, NULL, NULL, :org, :at)`,
		row.Args{"id": id, "name": name, "email": name + "@x.com", "org": org, "at": epoch})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLeftJoinLeavesUnmatchedStructNil(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedOrgs(t, db)

		one := int64(1)
		seedUserWithOrg(t, db, 1, "ada", &one)
		seedUserWithOrg(t, db, 2, "solo", nil)

		got, err := row.All[UserWithOrg](ctx, db, `
			SELECT u.id AS id, u.name AS name,
			       o.id AS org_id, o.name AS org_name
			FROM users u
			LEFT JOIN orgs o ON o.id = u.org_id
			ORDER BY u.id`)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d rows", len(got))
		}

		if got[0].Org == nil {
			t.Fatal("the matched row should have an Org")
		}
		if got[0].Org.Name != "acme" || got[0].Org.ID != 1 {
			t.Errorf("Org = %+v", got[0].Org)
		}

		// This is the point of the test: an unmatched LEFT JOIN must leave the
		// pointer nil rather than pointing at a zeroed Org that looks real.
		if got[1].Org != nil {
			t.Errorf("the unmatched row should have a nil Org, got %+v", *got[1].Org)
		}
		if got[1].Name != "solo" {
			t.Errorf("row 2 = %+v", got[1])
		}
	})
}

// A nested struct with a NULL in one column but not all of them is present,
// and the NULL column must still be reported if it cannot hold one.
func TestPartiallyNullNestedStructIsPresent(t *testing.T) {
	type nullableOrg struct {
		ID   int64
		Name *string
	}
	type userOrg struct {
		ID  int64 `db:"id,pk"`
		Org *nullableOrg
	}

	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		got, err := row.All[userOrg](ctx, db,
			`SELECT 1 AS id, 7 AS org_id, NULL AS org_name`)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Org == nil {
			t.Fatalf("expected a present Org, got %+v", got)
		}
		if got[0].Org.ID != 7 {
			t.Errorf("Org.ID = %d", got[0].Org.ID)
		}
		if got[0].Org.Name != nil {
			t.Errorf("Org.Name should be nil")
		}
	})
}

func TestEmbeddedStructScans(t *testing.T) {
	type timestamps struct {
		CreatedAt time.Time
	}
	type u struct {
		ID   int64 `db:"id,pk"`
		Name string
		timestamps
	}
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 1)
		got, err := row.One[u](ctx, db, `SELECT id, name, created_at FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "ada" || got.CreatedAt.IsZero() {
			t.Errorf("got %+v", got)
		}
	})
}
