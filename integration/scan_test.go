package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/r-52/row"
	"github.com/r-52/row/internal/dbtest"
	"github.com/r-52/row/qb"
)

// User mirrors the fixture users table.
type User struct {
	ID        int64 `db:"id,pk"`
	Name      string
	Email     string
	Age       int
	Score     float64
	Active    bool
	Data      []byte
	Bio       *string
	OrgID     *int64
	CreatedAt time.Time
}

var epoch = time.Date(2026, 3, 1, 12, 30, 0, 0, time.UTC)

func seedUsers(t *testing.T, db *row.DB, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 1; i <= n; i++ {
		_, err := row.Exec(ctx, db, `
			INSERT INTO users (id, name, email, age, score, active, data, bio, org_id, created_at)
			VALUES (:id, :name, :email, :age, :score, :active, :data, :bio, NULL, :created_at)`,
			row.Args{
				"id": i, "name": names[i-1], "email": names[i-1] + "@example.com",
				"age": 20 + i, "score": float64(i) * 1.5, "active": i%2 == 1,
				"data": []byte{byte(i)}, "bio": nil,
				"created_at": epoch.Add(time.Duration(i) * time.Hour),
			})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

var names = []string{"ada", "grace", "alan", "edsger", "barbara"}

func TestScanStruct(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 3)

		u, err := row.One[User](ctx, db,
			`SELECT id, name, email, age, score, active, data, bio, org_id, created_at
			 FROM users WHERE id = :id`, row.Args{"id": 2})
		if err != nil {
			t.Fatal(err)
		}
		if u.ID != 2 || u.Name != "grace" || u.Age != 22 {
			t.Errorf("got %+v", u)
		}
		if u.Score != 3.0 {
			t.Errorf("score = %v, want 3", u.Score)
		}
		if u.Active {
			t.Errorf("user 2 should be inactive")
		}
		if len(u.Data) != 1 || u.Data[0] != 2 {
			t.Errorf("data = %v", u.Data)
		}
		if u.Bio != nil {
			t.Errorf("bio should be nil, got %v", *u.Bio)
		}
		if u.OrgID != nil {
			t.Errorf("org_id should be nil")
		}
		if !u.CreatedAt.UTC().Equal(epoch.Add(2 * time.Hour)) {
			t.Errorf("created_at = %v, want %v", u.CreatedAt.UTC(), epoch.Add(2*time.Hour))
		}
	})
}

func TestScanAllAndPointerElement(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 4)

		vals, err := row.All[User](ctx, db, `SELECT id, name FROM users ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		if len(vals) != 4 {
			t.Fatalf("got %d rows", len(vals))
		}
		if vals[0].Name != "ada" || vals[3].Name != "edsger" {
			t.Errorf("unexpected order: %+v", vals)
		}

		ptrs, err := row.All[*User](ctx, db, `SELECT id, name FROM users ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		if len(ptrs) != 4 || ptrs[1].Name != "grace" {
			t.Errorf("pointer scan gave %v", ptrs)
		}
	})
}

func TestScanScalars(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 5)

		n, err := row.One[int64](ctx, db, `SELECT count(*) FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if n != 5 {
			t.Errorf("count = %d", n)
		}

		names, err := row.All[string](ctx, db, `SELECT name FROM users ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) != 5 || names[0] != "ada" {
			t.Errorf("names = %v", names)
		}

		when, err := row.One[time.Time](ctx, db, `SELECT created_at FROM users WHERE id = 1`)
		if err != nil {
			t.Fatal(err)
		}
		if !when.UTC().Equal(epoch.Add(time.Hour)) {
			t.Errorf("time = %v", when.UTC())
		}
	})
}

func TestScanNullableScalar(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 1)

		p, err := row.One[*string](ctx, db, `SELECT bio FROM users WHERE id = 1`)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Errorf("expected nil, got %q", *p)
		}

		// Scanning NULL into a non-nullable destination must be an error, not
		// a silent zero value.
		_, err = row.One[string](ctx, db, `SELECT bio FROM users WHERE id = 1`)
		if err == nil {
			t.Fatal("expected an error scanning NULL into string")
		}
	})
}

func TestScanMap(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 1)

		m, err := row.One[map[string]any](ctx, db, `SELECT id, name FROM users WHERE id = 1`)
		if err != nil {
			t.Fatal(err)
		}
		if len(m) != 2 {
			t.Fatalf("map = %v", m)
		}
		if _, ok := m["name"]; !ok {
			t.Errorf("map missing name: %v", m)
		}
	})
}

func TestOneRejectsWrongCardinality(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 3)

		_, err := row.One[User](ctx, db, `SELECT id, name FROM users WHERE id = 99`)
		if !errors.Is(err, row.ErrNoRows) {
			t.Errorf("want ErrNoRows, got %v", err)
		}

		_, err = row.One[User](ctx, db, `SELECT id, name FROM users`)
		if err == nil {
			t.Fatal("One should reject a multi-row result")
		}

		// First takes the same query happily.
		u, err := row.First[User](ctx, db, `SELECT id, name FROM users ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		if u.ID != 1 {
			t.Errorf("First returned %+v", u)
		}
	})
}

func TestStrictModeRejectsUnmappedColumns(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 1)

		type partial struct {
			ID int64 `db:"id"`
		}
		_, err := row.All[partial](ctx, db, `SELECT id, name FROM users`)
		if err == nil {
			t.Fatal("strict mode should reject the unmapped name column")
		}
	})
}

func TestLaxModeIgnoresUnmappedColumns(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 1)

		type partial struct {
			ID int64 `db:"id"`
		}
		got, err := row.All[partial](ctx, db, `SELECT id, name FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != 1 {
			t.Errorf("got %v", got)
		}
	}, row.Lax())
}

func TestInExpansion(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 5)

		got, err := row.All[string](ctx, db,
			`SELECT name FROM users WHERE id IN (:ids) ORDER BY id`,
			row.Args{"ids": []int{1, 3, 5}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"ada", "alan", "barbara"}
		if len(got) != 3 || got[0] != want[0] || got[2] != want[2] {
			t.Errorf("got %v want %v", got, want)
		}

		// An empty set must produce no rows rather than a syntax error.
		none, err := row.All[string](ctx, db,
			`SELECT name FROM users WHERE id IN (:ids)`, row.Args{"ids": []int{}})
		if err != nil {
			t.Fatal(err)
		}
		if len(none) != 0 {
			t.Errorf("empty IN matched %v", none)
		}
	})
}

func TestIterStreamsAndClosesOnBreak(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 5)

		var seen []string
		for u, err := range row.Iter[User](ctx, db, `SELECT id, name FROM users ORDER BY id`) {
			if err != nil {
				t.Fatal(err)
			}
			seen = append(seen, u.Name)
			if len(seen) == 2 {
				break // must not leak the rows handle
			}
		}
		if len(seen) != 2 {
			t.Errorf("saw %v", seen)
		}

		// If the previous loop leaked a connection, an in-memory SQLite pool
		// limited to one connection would deadlock here.
		n, err := row.One[int64](ctx, db, `SELECT count(*) FROM users`)
		if err != nil {
			t.Fatalf("database unusable after an early break: %v", err)
		}
		if n != 5 {
			t.Errorf("count = %d", n)
		}
	})
}

func TestIterYieldsErrorLast(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		var gotErr error
		count := 0
		for _, err := range row.Iter[User](ctx, db, `SELECT * FROM no_such_table`) {
			count++
			gotErr = err
		}
		if count != 1 || gotErr == nil {
			t.Errorf("expected exactly one failing iteration, got %d iterations, err=%v", count, gotErr)
		}
	})
}

func TestStructAsNamedArguments(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()

		u := User{
			ID: 1, Name: "ada", Email: "ada@example.com", Age: 36,
			Score: 9.5, Active: true, Data: []byte("x"), CreatedAt: epoch,
		}
		_, err := row.Exec(ctx, db, `
			INSERT INTO users (id, name, email, age, score, active, data, bio, org_id, created_at)
			VALUES (:id, :name, :email, :age, :score, :active, :data, :bio, :org_id, :created_at)`, u)
		if err != nil {
			t.Fatal(err)
		}
		got, err := row.One[User](ctx, db,
			`SELECT id, name, email, age, score, active, data, bio, org_id, created_at FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "ada" || got.Age != 36 || !got.Active {
			t.Errorf("round trip gave %+v", got)
		}
	})
}

func TestExecAndAffected(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 5)

		res, err := row.Exec(ctx, db, `UPDATE users SET age = age + 1 WHERE id <= :id`, row.Args{"id": 3})
		if err != nil {
			t.Fatal(err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 3 {
			t.Errorf("RowsAffected = %d, %v", n, err)
		}

		n, err := row.Affected(ctx, db, `DELETE FROM users WHERE id > :id`, row.Args{"id": 2})
		if err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Errorf("Affected = %d, want 3", n)
		}
		if got := count(t, db, "users"); got != 2 {
			t.Errorf("remaining = %d", got)
		}
	})
}

func TestFirstOfBuilder(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 3)

		name, err := row.FirstOf[string](ctx, db,
			qb.Select("name").From("users").OrderBy("id DESC"))
		if err != nil {
			t.Fatal(err)
		}
		if name != "alan" {
			t.Errorf("name = %q", name)
		}
	})
}

// Every entry point must report a nil Session rather than dereferencing it.
func TestNilSessionIsReported(t *testing.T) {
	ctx := context.Background()
	u := User{}

	checks := map[string]func() error{
		"All":        func() error { _, err := row.All[User](ctx, nil, `SELECT 1`); return err },
		"One":        func() error { _, err := row.One[User](ctx, nil, `SELECT 1`); return err },
		"First":      func() error { _, err := row.First[User](ctx, nil, `SELECT 1`); return err },
		"Exec":       func() error { _, err := row.Exec(ctx, nil, `SELECT 1`); return err },
		"Affected":   func() error { _, err := row.Affected(ctx, nil, `SELECT 1`); return err },
		"Insert":     func() error { return row.Insert(ctx, nil, "users", &u) },
		"InsertMany": func() error { return row.InsertMany(ctx, nil, "users", []User{u}) },
		"Update":     func() error { return row.Update(ctx, nil, "users", &u) },
		"Delete":     func() error { return row.Delete(ctx, nil, "users", &u) },
		"InTx":       func() error { return row.InTx(ctx, nil, func(context.Context, *row.Tx) error { return nil }) },
		"AllOf": func() error {
			_, err := row.AllOf[User](ctx, nil, qb.Select("id").From("users"))
			return err
		},
		"ExecOf": func() error {
			_, err := row.ExecOf(ctx, nil, qb.Select("id").From("users"))
			return err
		},
	}
	for name, fn := range checks {
		t.Run(name, func(t *testing.T) {
			if err := fn(); err == nil {
				t.Error("a nil Session should be reported as an error")
			}
		})
	}

	t.Run("Iter", func(t *testing.T) {
		var got error
		for _, err := range row.Iter[User](ctx, nil, `SELECT 1`) {
			got = err
		}
		if got == nil {
			t.Error("a nil Session should be yielded as an error")
		}
	})
	t.Run("IterOf", func(t *testing.T) {
		var got error
		for _, err := range row.IterOf[User](ctx, nil, qb.Select("id").From("users")) {
			got = err
		}
		if got == nil {
			t.Error("a nil Session should be yielded as an error")
		}
	})
}
