package row_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/r-52/row"
	"github.com/r-52/row/qb"
	"github.com/r-52/row/sqlite"
)

// User is a typical mapped struct. Column names come from the field names by
// default, so only the exceptions need a tag.
type User struct {
	ID     int64 `db:"id,pk"`
	Name   string
	Email  string
	Active bool
}

func openExampleDB() *row.DB {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	_, err = row.Exec(ctx, db, `
		CREATE TABLE users (
			id     INTEGER PRIMARY KEY,
			name   TEXT NOT NULL,
			email  TEXT NOT NULL UNIQUE,
			active BOOLEAN NOT NULL
		)`)
	if err != nil {
		log.Fatal(err)
	}
	users := []User{
		{1, "ada", "ada@example.com", true},
		{2, "grace", "grace@example.com", true},
		{3, "alan", "alan@example.com", false},
	}
	if err := row.InsertMany(ctx, db, "users", users); err != nil {
		log.Fatal(err)
	}
	return db
}

func ExampleAll() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	users, err := row.All[User](ctx, db,
		`SELECT id, name, email, active FROM users WHERE active = :active ORDER BY id`,
		row.Args{"active": true})
	if err != nil {
		log.Fatal(err)
	}
	for _, u := range users {
		fmt.Println(u.ID, u.Name)
	}
	// Output:
	// 1 ada
	// 2 grace
}

func ExampleOne() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	u, err := row.One[User](ctx, db,
		`SELECT id, name, email, active FROM users WHERE id = :id`, row.Args{"id": 2})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(u.Name)

	// A query that matches nothing returns ErrNoRows.
	_, err = row.One[User](ctx, db,
		`SELECT id, name, email, active FROM users WHERE id = :id`, row.Args{"id": 99})
	fmt.Println(errors.Is(err, row.ErrNoRows))
	// Output:
	// grace
	// true
}

// A slice argument expands into a placeholder list, so IN needs no special
// handling: no separate expansion and rebinding step.
func ExampleAll_inClause() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	names, err := row.All[string](ctx, db,
		`SELECT name FROM users WHERE id IN (:ids) ORDER BY id`,
		row.Args{"ids": []int64{1, 3}})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(names)
	// Output: [ada alan]
}

// Iter streams rows rather than collecting them, and closes the underlying
// result even when the loop breaks early.
func ExampleIter() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	for u, err := range row.Iter[User](ctx, db, `SELECT id, name, email, active FROM users ORDER BY id`) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(u.Name)
		if u.Name == "grace" {
			break
		}
	}
	// Output:
	// ada
	// grace
}

func ExampleInsert() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	u := User{ID: 4, Name: "edsger", Email: "edsger@example.com"}
	if err := row.Insert(ctx, db, "users", &u); err != nil {
		log.Fatal(err)
	}

	// Update writes back using the field tagged pk.
	u.Name = "edsger dijkstra"
	if err := row.Update(ctx, db, "users", &u, row.Only("name")); err != nil {
		log.Fatal(err)
	}

	name, err := row.One[string](ctx, db, `SELECT name FROM users WHERE id = :id`, row.Args{"id": 4})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(name)
	// Output: edsger dijkstra
}

func ExampleInTx() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	// The transaction rolls back because the callback returns an error, and
	// the error is returned unchanged.
	sentinel := errors.New("changed my mind")
	err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
		u := User{ID: 9, Name: "temp", Email: "temp@example.com"}
		if err := row.Insert(ctx, tx, "users", &u); err != nil {
			return err
		}
		return sentinel
	})
	fmt.Println(errors.Is(err, sentinel))

	n, err := row.One[int64](ctx, db, `SELECT count(*) FROM users`)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(n)
	// Output:
	// true
	// 3
}

// A nested InTx opens a savepoint, so an inner failure undoes only the inner
// work and the outer transaction carries on.
func ExampleInTx_nested() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
		u := User{ID: 10, Name: "kept", Email: "kept@example.com"}
		if err := row.Insert(ctx, tx, "users", &u); err != nil {
			return err
		}
		// This inner block fails and is rolled back on its own.
		_ = row.InTx(ctx, tx, func(ctx context.Context, tx *row.Tx) error {
			v := User{ID: 11, Name: "discarded", Email: "discarded@example.com"}
			if err := row.Insert(ctx, tx, "users", &v); err != nil {
				return err
			}
			return errors.New("inner failed")
		})
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	names, err := row.All[string](ctx, db, `SELECT name FROM users WHERE id >= 10 ORDER BY id`)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(names)
	// Output: [kept]
}

// Constraint violations are classified into portable codes, so branching on
// one needs no driver-specific error type.
func ExampleIsCode() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	dup := User{ID: 99, Name: "impostor", Email: "ada@example.com"}
	err := row.Insert(ctx, db, "users", &dup)
	fmt.Println(row.IsCode(err, row.UniqueViolation))
	fmt.Println(row.CodeOf(err))
	// Output:
	// true
	// unique_violation
}

// The builder is for queries whose shape depends on the request.
func ExampleAllOf() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	search := "a"
	onlyActive := true

	q := qb.Select("id", "name", "email", "active").From("users")
	if search != "" {
		q = q.Where(qb.Like{"name": "%" + search + "%"})
	}
	if onlyActive {
		q = q.Where(qb.Eq{"active": true})
	}
	q = q.OrderBy("id")

	users, err := row.AllOf[User](ctx, db, q)
	if err != nil {
		log.Fatal(err)
	}
	for _, u := range users {
		fmt.Println(u.Name)
	}
	// Output:
	// ada
	// grace
}

// A pointer to a nested struct stays nil when a LEFT JOIN matched nothing,
// instead of becoming a zeroed struct that looks like real data.
func ExampleAll_leftJoin() {
	ctx := context.Background()
	db := openExampleDB()
	defer db.Close()

	if _, err := row.Exec(ctx, db, `CREATE TABLE orgs (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		log.Fatal(err)
	}
	if _, err := row.Exec(ctx, db, `INSERT INTO orgs (id, name) VALUES (1, 'acme')`); err != nil {
		log.Fatal(err)
	}
	if _, err := row.Exec(ctx, db, `ALTER TABLE users ADD COLUMN org_id INTEGER`); err != nil {
		log.Fatal(err)
	}
	if _, err := row.Exec(ctx, db, `UPDATE users SET org_id = 1 WHERE id = 1`); err != nil {
		log.Fatal(err)
	}

	type Org struct {
		ID   int64 `db:"id,pk"`
		Name string
	}
	type Member struct {
		ID   int64 `db:"id,pk"`
		Name string
		Org  *Org // reads org_id and org_name
	}

	members, err := row.All[Member](ctx, db, `
		SELECT u.id AS id, u.name AS name, o.id AS org_id, o.name AS org_name
		FROM users u LEFT JOIN orgs o ON o.id = u.org_id
		WHERE u.id <= 2 ORDER BY u.id`)
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range members {
		if m.Org == nil {
			fmt.Println(m.Name, "no org")
			continue
		}
		fmt.Println(m.Name, m.Org.Name)
	}
	// Output:
	// ada acme
	// grace no org
}

// Returning brings database-generated values back into the struct.
func ExampleReturning() {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if _, err := row.Exec(ctx, db, `
		CREATE TABLE notes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			body       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		log.Fatal(err)
	}

	type Note struct {
		// Both columns are filled in by the database, so neither is written.
		ID        int64     `db:"id,pk,readonly"`
		Body      string    `db:"body"`
		CreatedAt time.Time `db:"created_at,readonly"`
	}

	n := Note{Body: "hello"}
	if err := row.Insert(ctx, db, "notes", &n, row.Returning("id", "created_at")); err != nil {
		log.Fatal(err)
	}
	fmt.Println(n.ID, n.CreatedAt.IsZero())
	// Output: 1 false
}
