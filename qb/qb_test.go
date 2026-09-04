package qb_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/r-52/row"
	"github.com/r-52/row/pg"
	"github.com/r-52/row/qb"
	"github.com/r-52/row/sqlite"
)

func build(t *testing.T, d row.Dialect, b row.Builder) (string, string) {
	t.Helper()
	sql, args, err := b.BuildSQL(d)
	if err != nil {
		t.Fatalf("BuildSQL: %v", err)
	}
	return sql, fmt.Sprint(args)
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name     string
		q        row.Builder
		wantSQL  string
		wantArgs string
	}{
		{
			name:    "star",
			q:       qb.Select().From("users"),
			wantSQL: `SELECT * FROM "users"`,
		},
		{
			name:    "columns are quoted",
			q:       qb.Select("id", "name").From("users"),
			wantSQL: `SELECT "id", "name" FROM "users"`,
		},
		{
			name:    "expressions pass through unquoted",
			q:       qb.Select("count(*)", "max(age)").From("users"),
			wantSQL: `SELECT count(*), max(age) FROM "users"`,
		},
		{
			name:    "dotted columns and star",
			q:       qb.Select("u.id", "o.*").From("users u"),
			wantSQL: `SELECT "u"."id", "o".* FROM "users" "u"`,
		},
		{
			name:     "eq condition",
			q:        qb.Select("id").From("users").Where(qb.Eq{"org_id": 7}),
			wantSQL:  `SELECT "id" FROM "users" WHERE "org_id" = $1`,
			wantArgs: `[7]`,
		},
		{
			name:     "eq keys are ordered deterministically",
			q:        qb.Select("id").From("users").Where(qb.Eq{"z": 1, "a": 2, "m": 3}),
			wantSQL:  `SELECT "id" FROM "users" WHERE "a" = $1 AND "m" = $2 AND "z" = $3`,
			wantArgs: `[2 3 1]`,
		},
		{
			name:    "nil becomes IS NULL",
			q:       qb.Select("id").From("users").Where(qb.Eq{"bio": nil}),
			wantSQL: `SELECT "id" FROM "users" WHERE "bio" IS NULL`,
		},
		{
			name:    "not equal nil becomes IS NOT NULL",
			q:       qb.Select("id").From("users").Where(qb.NotEq{"bio": nil}),
			wantSQL: `SELECT "id" FROM "users" WHERE "bio" IS NOT NULL`,
		},
		{
			name:     "slice becomes IN",
			q:        qb.Select("id").From("users").Where(qb.Eq{"id": []int{1, 2, 3}}),
			wantSQL:  `SELECT "id" FROM "users" WHERE "id" IN ($1,$2,$3)`,
			wantArgs: `[1 2 3]`,
		},
		{
			name:    "empty IN matches nothing",
			q:       qb.Select("id").From("users").Where(qb.In("id")),
			wantSQL: `SELECT "id" FROM "users" WHERE 1 = 0`,
		},
		{
			name:    "empty NOT IN matches everything",
			q:       qb.Select("id").From("users").Where(qb.NotIn("id")),
			wantSQL: `SELECT "id" FROM "users" WHERE 1 = 1`,
		},
		{
			name:     "In accepts a slice",
			q:        qb.Select("id").From("users").Where(qb.In("id", []int64{4, 5})),
			wantSQL:  `SELECT "id" FROM "users" WHERE "id" IN ($1,$2)`,
			wantArgs: `[4 5]`,
		},
		{
			name:     "repeated Where clauses AND together",
			q:        qb.Select("id").From("users").Where(qb.Eq{"a": 1}).Where(qb.Gt{"b": 2}),
			wantSQL:  `SELECT "id" FROM "users" WHERE "a" = $1 AND "b" > $2`,
			wantArgs: `[1 2]`,
		},
		{
			name: "or groups are parenthesised",
			q: qb.Select("id").From("users").
				Where(qb.Eq{"active": true}, qb.Or(qb.Eq{"role": "admin"}, qb.Gt{"age": 65})),
			wantSQL:  `SELECT "id" FROM "users" WHERE "active" = $1 AND ("role" = $2 OR "age" > $3)`,
			wantArgs: `[true admin 65]`,
		},
		{
			name:     "not",
			q:        qb.Select("id").From("users").Where(qb.Not(qb.Eq{"a": 1})),
			wantSQL:  `SELECT "id" FROM "users" WHERE NOT ("a" = $1)`,
			wantArgs: `[1]`,
		},
		{
			name:     "between",
			q:        qb.Select("id").From("users").Where(qb.Between("age", 18, 65)),
			wantSQL:  `SELECT "id" FROM "users" WHERE "age" BETWEEN $1 AND $2`,
			wantArgs: `[18 65]`,
		},
		{
			name:     "null checks",
			q:        qb.Select("id").From("users").Where(qb.IsNull("bio"), qb.NotNull("email")),
			wantSQL:  `SELECT "id" FROM "users" WHERE "bio" IS NULL AND "email" IS NOT NULL`,
			wantArgs: `[]`,
		},
		{
			name:     "like",
			q:        qb.Select("id").From("users").Where(qb.Like{"name": "%ada%"}),
			wantSQL:  `SELECT "id" FROM "users" WHERE "name" LIKE $1`,
			wantArgs: `[%ada%]`,
		},
		{
			name:     "raw fragment binds its arguments",
			q:        qb.Select("id").From("users").Where(qb.Raw("data @> ?::jsonb", `{"a":1}`)),
			wantSQL:  `SELECT "id" FROM "users" WHERE data @> $1::jsonb`,
			wantArgs: `[{"a":1}]`,
		},
		{
			name: "joins",
			q: qb.Select("u.id", "o.name").From("users u").
				LeftJoin("orgs o", qb.Raw("o.id = u.org_id")).
				Where(qb.Eq{"u.active": true}),
			wantSQL:  `SELECT "u"."id", "o"."name" FROM "users" "u" LEFT JOIN "orgs" "o" ON o.id = u.org_id WHERE "u"."active" = $1`,
			wantArgs: `[true]`,
		},
		{
			name: "group by and having",
			q: qb.Select("org_id", "count(*)").From("users").
				GroupBy("org_id").Having(qb.Raw("count(*) > ?", 3)),
			wantSQL:  `SELECT "org_id", count(*) FROM "users" GROUP BY "org_id" HAVING count(*) > $1`,
			wantArgs: `[3]`,
		},
		{
			name:    "order, limit and offset",
			q:       qb.Select("id").From("users").OrderBy("created_at DESC", "id").Limit(10).Offset(20),
			wantSQL: `SELECT "id" FROM "users" ORDER BY "created_at" DESC, "id" LIMIT 10 OFFSET 20`,
		},
		{
			name:    "order by an expression is untouched",
			q:       qb.Select("id").From("users").OrderBy("lower(name) ASC"),
			wantSQL: `SELECT "id" FROM "users" ORDER BY lower(name) ASC`,
		},
		{
			name:    "distinct",
			q:       qb.Select("org_id").From("users").Distinct(),
			wantSQL: `SELECT DISTINCT "org_id" FROM "users"`,
		},
		{
			name:    "aliased table with AS",
			q:       qb.Select("*").From("public.users AS u"),
			wantSQL: `SELECT * FROM "public"."users" AS "u"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := build(t, pg.Dialect, tt.q)
			if gotSQL != tt.wantSQL {
				t.Errorf("sql\n got: %s\nwant: %s", gotSQL, tt.wantSQL)
			}
			if tt.wantArgs != "" && gotArgs != tt.wantArgs {
				t.Errorf("args\n got: %s\nwant: %s", gotArgs, tt.wantArgs)
			}
		})
	}
}

// The same query must render with each dialect's own placeholders.
func TestDialectPlaceholders(t *testing.T) {
	q := qb.Select("id").From("users").Where(qb.Eq{"a": 1}, qb.In("b", 2, 3))

	pgSQL, _ := build(t, pg.Dialect, q)
	if want := `SELECT "id" FROM "users" WHERE "a" = $1 AND "b" IN ($2,$3)`; pgSQL != want {
		t.Errorf("postgres:\n got %s\nwant %s", pgSQL, want)
	}

	liteSQL, _ := build(t, sqlite.Dialect, q)
	if want := `SELECT "id" FROM "users" WHERE "a" = ? AND "b" IN (?,?)`; liteSQL != want {
		t.Errorf("sqlite:\n got %s\nwant %s", liteSQL, want)
	}
}

// ILIKE exists on Postgres; SQLite's LIKE is already case-insensitive.
func TestILikePerDialect(t *testing.T) {
	q := qb.Select("id").From("users").Where(qb.ILike{"name": "ada%"})

	if got, _ := build(t, pg.Dialect, q); !strings.Contains(got, "ILIKE") {
		t.Errorf("postgres should use ILIKE: %s", got)
	}
	got, _ := build(t, sqlite.Dialect, q)
	if strings.Contains(got, "ILIKE") {
		t.Errorf("sqlite has no ILIKE: %s", got)
	}
	if !strings.Contains(got, "LIKE") {
		t.Errorf("sqlite should still use LIKE: %s", got)
	}
}

// A builder value must be safe to branch from without the branches affecting
// one another.
func TestBuildersAreImmutable(t *testing.T) {
	base := qb.Select("id").From("users").Where(qb.Eq{"active": true})
	admins := base.Where(qb.Eq{"role": "admin"})
	olds := base.Where(qb.Gt{"age": 65})

	baseSQL, _ := build(t, pg.Dialect, base)
	if strings.Contains(baseSQL, "role") || strings.Contains(baseSQL, "age") {
		t.Errorf("the base query was mutated: %s", baseSQL)
	}
	adminSQL, _ := build(t, pg.Dialect, admins)
	if !strings.Contains(adminSQL, "role") || strings.Contains(adminSQL, "age") {
		t.Errorf("admins query = %s", adminSQL)
	}
	oldSQL, _ := build(t, pg.Dialect, olds)
	if !strings.Contains(oldSQL, "age") || strings.Contains(oldSQL, "role") {
		t.Errorf("olds query = %s", oldSQL)
	}
}

func TestInsert(t *testing.T) {
	q := qb.InsertInto("users").Columns("id", "name").
		Values(1, "ada").Values(2, "grace").
		OnConflict(`("id") DO NOTHING`).Returning("id")

	gotSQL, gotArgs := build(t, pg.Dialect, q)
	want := `INSERT INTO "users" ("id", "name") VALUES ($1, $2), ($3, $4) ON CONFLICT ("id") DO NOTHING RETURNING "id"`
	if gotSQL != want {
		t.Errorf("\n got: %s\nwant: %s", gotSQL, want)
	}
	if gotArgs != `[1 ada 2 grace]` {
		t.Errorf("args = %s", gotArgs)
	}
}

func TestUpdate(t *testing.T) {
	q := qb.Update("counters").
		Set("name", "hits").
		SetExpr("value", qb.Raw("value + ?", 1)).
		Where(qb.Eq{"name": "hits"}).
		Returning("value")

	gotSQL, gotArgs := build(t, pg.Dialect, q)
	want := `UPDATE "counters" SET "name" = $1, "value" = value + $2 WHERE "name" = $3 RETURNING "value"`
	if gotSQL != want {
		t.Errorf("\n got: %s\nwant: %s", gotSQL, want)
	}
	if gotArgs != `[hits 1 hits]` {
		t.Errorf("args = %s", gotArgs)
	}
}

func TestUpdateSetMapIsOrdered(t *testing.T) {
	q := qb.Update("t").SetMap(map[string]any{"z": 1, "a": 2}).Where(qb.Raw("1 = 1"))
	gotSQL, gotArgs := build(t, pg.Dialect, q)
	if want := `UPDATE "t" SET "a" = $1, "z" = $2 WHERE 1 = 1`; gotSQL != want {
		t.Errorf("got %s want %s", gotSQL, want)
	}
	if gotArgs != `[2 1]` {
		t.Errorf("args = %s", gotArgs)
	}
}

func TestDelete(t *testing.T) {
	q := qb.DeleteFrom("users").Where(qb.Eq{"id": 3})
	gotSQL, gotArgs := build(t, pg.Dialect, q)
	if want := `DELETE FROM "users" WHERE "id" = $1`; gotSQL != want {
		t.Errorf("got %s want %s", gotSQL, want)
	}
	if gotArgs != `[3]` {
		t.Errorf("args = %s", gotArgs)
	}
}

// An UPDATE or DELETE without a WHERE clause hits every row. That is almost
// always a mistake, so qb refuses unless the caller says so explicitly.
func TestUnboundedWritesAreRefused(t *testing.T) {
	if _, _, err := qb.Update("users").Set("a", 1).BuildSQL(pg.Dialect); err == nil {
		t.Error("UPDATE without WHERE should be refused")
	}
	if _, _, err := qb.DeleteFrom("users").BuildSQL(pg.Dialect); err == nil {
		t.Error("DELETE without WHERE should be refused")
	}
	// ...and the escape hatch works.
	if _, _, err := qb.DeleteFrom("users").Where(qb.Raw("1 = 1")).BuildSQL(pg.Dialect); err != nil {
		t.Errorf("explicit unbounded delete should build: %v", err)
	}
}

func TestBuildErrors(t *testing.T) {
	cases := []struct {
		name string
		q    row.Builder
		want string
	}{
		{"empty Eq", qb.Select("a").From("t").Where(qb.Eq{}), "empty = condition"},
		{"Lt against nil", qb.Select("a").From("t").Where(qb.Lt{"a": nil}), "cannot compare"},
		{"raw arity", qb.Select("a").From("t").Where(qb.Raw("a = ? AND b = ?", 1)), "2 placeholders but 1 arguments"},
		{"insert without values", qb.InsertInto("t").Columns("a"), "no values"},
		{"insert arity", qb.InsertInto("t").Columns("a", "b").Values(1), "2 columns were named"},
		{"insert without table", qb.InsertInto("").Values(1), "needs a table"},
		{"update sets nothing", qb.Update("t").Where(qb.Raw("1 = 1")), "sets nothing"},
		{"nil expression", qb.Select("a").From("t").Where(nil), "nil expression"},
		{"empty Or", qb.Select("a").From("t").Where(qb.Or()), "empty OR group"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := c.q.BuildSQL(pg.Dialect)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should contain %q", err, c.want)
			}
		})
	}
}

// Values reach the server as bind parameters, never as text in the statement.
func TestValuesAreAlwaysBound(t *testing.T) {
	nasty := "'; DROP TABLE users; --"
	gotSQL, gotArgs := build(t, pg.Dialect,
		qb.Select("id").From("users").Where(qb.Eq{"name": nasty}))
	if strings.Contains(gotSQL, "DROP TABLE") {
		t.Fatalf("the value was spliced into the statement: %s", gotSQL)
	}
	if !strings.Contains(gotArgs, "DROP TABLE") {
		t.Errorf("the value should have become an argument: %s", gotArgs)
	}
}

func BenchmarkBuildSelect(b *testing.B) {
	q := qb.Select("id", "name", "email").From("users u").
		LeftJoin("orgs o", qb.Raw("o.id = u.org_id")).
		Where(qb.Eq{"u.active": true}, qb.In("u.id", 1, 2, 3)).
		OrderBy("u.created_at DESC").Limit(50)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := q.BuildSQL(pg.Dialect); err != nil {
			b.Fatal(err)
		}
	}
}

// The join variants and the smaller builder methods, which the main table does
// not reach.
func TestJoinVariantsAndModifiers(t *testing.T) {
	cases := []struct {
		name string
		q    row.Builder
		want string
	}{
		{
			"inner join",
			qb.Select("a").From("t").InnerJoin("u", qb.Raw("u.id = t.id")),
			`SELECT "a" FROM "t" INNER JOIN "u" ON u.id = t.id`,
		},
		{
			"plain join",
			qb.Select("a").From("t").Join("u", qb.Raw("u.id = t.id")),
			`SELECT "a" FROM "t" JOIN "u" ON u.id = t.id`,
		},
		{
			"right join",
			qb.Select("a").From("t").RightJoin("u", qb.Raw("u.id = t.id")),
			`SELECT "a" FROM "t" RIGHT JOIN "u" ON u.id = t.id`,
		},
		{
			"join without a condition",
			qb.Select("a").From("t").Join("u", nil),
			`SELECT "a" FROM "t" JOIN "u"`,
		},
		{
			"Columns replaces the projection",
			qb.Select("a", "b").From("t").Columns("count(*)"),
			`SELECT count(*) FROM "t"`,
		},
		{
			"ClearOrder drops ordering",
			qb.Select("a").From("t").OrderBy("a DESC").ClearOrder(),
			`SELECT "a" FROM "t"`,
		},
		{
			"And nested inside Or",
			qb.Select("a").From("t").Where(qb.Or(qb.And(qb.Eq{"a": 1}, qb.Eq{"b": 2}), qb.Eq{"c": 3})),
			`SELECT "a" FROM "t" WHERE (("a" = $1 AND "b" = $2) OR "c" = $3)`,
		},
		{
			"And with one member is not parenthesised",
			qb.Select("a").From("t").Where(qb.And(qb.Eq{"a": 1})),
			`SELECT "a" FROM "t" WHERE "a" = $1`,
		},
		{
			"NotBetween",
			qb.Select("a").From("t").Where(qb.NotBetween("age", 1, 2)),
			`SELECT "a" FROM "t" WHERE "age" NOT BETWEEN $1 AND $2`,
		},
		{
			"NotEq with a slice becomes NOT IN",
			qb.Select("a").From("t").Where(qb.NotEq{"id": []int{1, 2}}),
			`SELECT "a" FROM "t" WHERE "id" NOT IN ($1,$2)`,
		},
		{
			"Lte and Gte",
			qb.Select("a").From("t").Where(qb.Lte{"a": 1}, qb.Gte{"b": 2}),
			`SELECT "a" FROM "t" WHERE "a" <= $1 AND "b" >= $2`,
		},
		{
			"delete with returning",
			qb.DeleteFrom("t").Where(qb.Eq{"id": 1}).Returning("id", "name"),
			`DELETE FROM "t" WHERE "id" = $1 RETURNING "id", "name"`,
		},
		{
			"insert without a column list",
			qb.InsertInto("t").Values(1, 2),
			`INSERT INTO "t" VALUES ($1, $2)`,
		},
		{
			"offset without limit",
			qb.Select("a").From("t").Offset(5),
			`SELECT "a" FROM "t" OFFSET 5`,
		},
		{
			"schema-qualified table",
			qb.Select("a").From("public.t"),
			`SELECT "a" FROM "public"."t"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := build(t, pg.Dialect, c.q)
			if got != c.want {
				t.Errorf("\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}

func TestNilDialectIsReported(t *testing.T) {
	for _, b := range []row.Builder{
		qb.Select("a").From("t"),
		qb.InsertInto("t").Values(1),
		qb.Update("t").Set("a", 1).Where(qb.Raw("1 = 1")),
		qb.DeleteFrom("t").Where(qb.Raw("1 = 1")),
	} {
		if _, _, err := b.BuildSQL(nil); err == nil {
			t.Errorf("%T should reject a nil dialect", b)
		}
	}
}

// SQLite has no RETURNING before 3.35; the feature flag is what qb consults,
// so a dialect that reports false must be refused rather than emitting SQL
// that fails at the server.
func TestReturningRequiresSupport(t *testing.T) {
	if _, _, err := qb.InsertInto("t").Values(1).Returning("id").BuildSQL(noReturning{}); err == nil {
		t.Error("RETURNING should be refused on a dialect that lacks it")
	}
}

type noReturning struct{}

func (noReturning) Name() string       { return "toy" }
func (noReturning) DriverName() string { return "toy" }
func (noReturning) AppendPlaceholder(dst []byte, n int) []byte {
	return append(dst, '?')
}
func (noReturning) QuoteIdent(s string) string           { return row.QuoteWith(s, '"') }
func (noReturning) Features() row.Features               { return row.Features{} }
func (noReturning) ClassifyError(error) (row.Code, bool) { return row.Unknown, false }
