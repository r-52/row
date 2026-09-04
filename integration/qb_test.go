package integration

import (
	"context"
	"testing"

	"github.com/r-52/row"
	"github.com/r-52/row/internal/dbtest"
	"github.com/r-52/row/qb"
)

// The builder's output has to be valid on the server, not just look right in a
// string comparison, so every shape it can emit is executed here.

func TestBuilderSelectExecutes(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 5)

		q := qb.Select("id", "name").From("users").
			Where(qb.In("id", 1, 3, 5)).
			OrderBy("id DESC").
			Limit(2)

		got, err := row.AllOf[struct {
			ID   int64 `db:"id,pk"`
			Name string
		}](ctx, db, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].ID != 5 || got[1].ID != 3 {
			t.Errorf("got %+v", got)
		}
	})
}

func TestBuilderConditionalFiltering(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 5)

		// The shape a builder is actually for: filters that may or may not apply.
		for _, tc := range []struct {
			name   string
			search string
			active *bool
			want   int64
		}{
			{"no filters", "", nil, 5},
			{"name filter", "ada", nil, 1},
			{"active filter", "", ptr(true), 3},
			{"both", "a", ptr(true), 3},
		} {
			t.Run(tc.name, func(t *testing.T) {
				q := qb.Select("count(*)").From("users")
				if tc.search != "" {
					q = q.Where(qb.Like{"name": "%" + tc.search + "%"})
				}
				if tc.active != nil {
					q = q.Where(qb.Eq{"active": *tc.active})
				}
				n, err := row.OneOf[int64](ctx, db, q)
				if err != nil {
					t.Fatal(err)
				}
				if n != tc.want {
					t.Errorf("count = %d, want %d", n, tc.want)
				}
			})
		}
	})
}

func TestBuilderJoinExecutes(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedOrgs(t, db)
		one := int64(1)
		seedUserWithOrg(t, db, 1, "ada", &one)
		seedUserWithOrg(t, db, 2, "solo", nil)

		q := qb.Select("u.id AS id", "u.name AS name", "o.id AS org_id", "o.name AS org_name").
			From("users u").
			LeftJoin("orgs o", qb.Raw("o.id = u.org_id")).
			OrderBy("u.id")

		got, err := row.AllOf[UserWithOrg](ctx, db, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d rows", len(got))
		}
		if got[0].Org == nil || got[0].Org.Name != "acme" {
			t.Errorf("row 0 = %+v", got[0])
		}
		if got[1].Org != nil {
			t.Errorf("row 1 should have a nil Org")
		}
	})
}

func TestBuilderInsertUpdateDeleteExecute(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()

		ins := qb.InsertInto("counters").Columns("name", "value").
			Values("a", 1).Values("b", 2).Returning("name", "value")
		got, err := row.AllOf[struct {
			Name  string `db:"name,pk"`
			Value int64  `db:"value"`
		}](ctx, db, ins)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("RETURNING gave %d rows", len(got))
		}

		upd := qb.Update("counters").
			SetExpr("value", qb.Raw("value + ?", 10)).
			Where(qb.Eq{"name": "a"}).
			Returning("value")
		v, err := row.OneOf[int64](ctx, db, upd)
		if err != nil {
			t.Fatal(err)
		}
		if v != 11 {
			t.Errorf("value = %d, want 11", v)
		}

		del := qb.DeleteFrom("counters").Where(qb.Eq{"name": "b"})
		if _, err := row.ExecOf(ctx, db, del); err != nil {
			t.Fatal(err)
		}
		if n := count(t, db, "counters"); n != 1 {
			t.Errorf("counters = %d", n)
		}
	})
}

func TestBuilderUpsertExecutes(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()

		// ON CONFLICT is spelled the same on both engines.
		up := func(v int64) qb.InsertQuery {
			return qb.InsertInto("counters").Columns("name", "value").
				Values("hits", v).
				OnConflict(`("name") DO UPDATE SET value = excluded.value`).
				Returning("value")
		}
		if _, err := row.OneOf[int64](ctx, db, up(1)); err != nil {
			t.Fatal(err)
		}
		got, err := row.OneOf[int64](ctx, db, up(9))
		if err != nil {
			t.Fatal(err)
		}
		if got != 9 {
			t.Errorf("value = %d, want 9", got)
		}
		if n := count(t, db, "counters"); n != 1 {
			t.Errorf("the upsert inserted a second row: %d", n)
		}
	})
}

func TestBuilderIterExecutes(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 4)

		q := qb.Select("name").From("users").OrderBy("id")
		var got []string
		for name, err := range row.IterOf[string](ctx, db, q) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, name)
		}
		if len(got) != 4 || got[0] != "ada" {
			t.Errorf("got %v", got)
		}
	})
}

func TestBuilderEmptyInMatchesNothing(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 3)

		n, err := row.OneOf[int64](ctx, db,
			qb.Select("count(*)").From("users").Where(qb.In("id")))
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("an empty IN matched %d rows", n)
		}
	})
}

func ptr[T any](v T) *T { return &v }
