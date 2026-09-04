// Command crossengine runs the same code against SQLite and PostgreSQL to show
// that a row program is portable between them.
//
// It applies one schema (in each engine's DDL spelling), then runs identical
// named-parameter, IN-expansion, LEFT JOIN, builder and write operations
// through both, and finally forces a unique violation to show that both report
// it as the same row.Code.
//
//	go run ./examples/crossengine
//	go run ./examples/crossengine -pg 'postgres://row:row@localhost:55432/row_test?sslmode=disable'
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/r-52/row"
	"github.com/r-52/row/pg"
	"github.com/r-52/row/qb"
	"github.com/r-52/row/sqlite"
)

type Org struct {
	ID   int64 `db:"id,pk"`
	Name string
}

// WriteUser is the write-side view of a user. It is separate from User
// because User's Org field maps to org_id and org_name, which is right for
// reading a join and wrong for writing a table.
type WriteUser struct {
	ID        int64 `db:"id,pk"`
	Name      string
	Email     string
	Active    bool
	CreatedAt time.Time
	OrgID     *int64 `db:"org_id"`
}

type User struct {
	ID        int64 `db:"id,pk"`
	Name      string
	Email     string
	Active    bool
	CreatedAt time.Time
	Org       *Org // filled from org_id / org_name, nil when unmatched
}

const sqliteDDL = `
CREATE TABLE orgs (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE users (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	email      TEXT NOT NULL UNIQUE,
	active     BOOLEAN NOT NULL,
	created_at TIMESTAMP NOT NULL,
	org_id     INTEGER REFERENCES orgs(id)
);`

const pgDDL = `
CREATE TABLE orgs (id BIGINT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE users (
	id         BIGINT PRIMARY KEY,
	name       TEXT NOT NULL,
	email      TEXT NOT NULL UNIQUE,
	active     BOOLEAN NOT NULL,
	created_at TIMESTAMPTZ NOT NULL,
	org_id     BIGINT REFERENCES orgs(id)
);`

func main() {
	pgDSN := flag.String("pg", os.Getenv("ROW_PG_DSN"), "PostgreSQL DSN; skipped when empty")
	flag.Parse()
	ctx := context.Background()

	lite, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		log.Fatalf("sqlite: %v", err)
	}
	defer lite.Close()
	run(ctx, "sqlite", lite, sqliteDDL)

	if *pgDSN == "" {
		fmt.Println("\nno -pg DSN given; skipping PostgreSQL")
		return
	}
	pgdb, err := pg.Open(ctx, *pgDSN)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pgdb.Close()
	for _, t := range []string{"users", "orgs"} {
		if _, err := row.Exec(ctx, pgdb, "DROP TABLE IF EXISTS "+t+" CASCADE"); err != nil {
			log.Fatalf("postgres cleanup: %v", err)
		}
	}
	run(ctx, "postgres", pgdb, pgDDL)
}

// run performs the identical sequence of operations on whichever database it
// is handed. Nothing below is engine-specific.
func run(ctx context.Context, label string, db *row.DB, ddl string) {
	fmt.Printf("\n=== %s ===\n", label)

	for _, stmt := range splitStatements(ddl) {
		if _, err := row.Exec(ctx, db, stmt); err != nil {
			log.Fatalf("%s schema: %v", label, err)
		}
	}

	// Bulk write, in one transaction.
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
		if err := row.InsertMany(ctx, tx, "orgs", []Org{{1, "acme"}, {2, "initech"}}); err != nil {
			return err
		}
		one, two := int64(1), int64(2)
		return row.InsertMany(ctx, tx, "users", []WriteUser{
			{1, "ada", "ada@example.com", true, now, &one},
			{2, "grace", "grace@example.com", true, now, &two},
			{3, "alan", "alan@example.com", false, now, &one},
			{4, "solo", "solo@example.com", true, now, nil},
		})
	})
	if err != nil {
		log.Fatalf("%s seed: %v", label, err)
	}

	// A named parameter and an expanded IN list in one statement.
	names, err := row.All[string](ctx, db,
		`SELECT name FROM users WHERE active = :active AND id IN (:ids) ORDER BY id`,
		row.Args{"active": true, "ids": []int64{1, 3, 4}})
	if err != nil {
		log.Fatalf("%s select: %v", label, err)
	}
	fmt.Printf("named + IN      %v\n", names)

	// A LEFT JOIN whose unmatched rows leave the nested struct nil.
	members, err := row.All[User](ctx, db, `
		SELECT u.id AS id, u.name AS name, u.email AS email, u.active AS active,
		       u.created_at AS created_at, o.id AS org_id, o.name AS org_name
		FROM users u LEFT JOIN orgs o ON o.id = u.org_id
		ORDER BY u.id`)
	if err != nil {
		log.Fatalf("%s join: %v", label, err)
	}
	for _, m := range members {
		org := "<none>"
		if m.Org != nil {
			org = m.Org.Name
		}
		fmt.Printf("join            %-6s %s\n", m.Name, org)
	}

	// The same builder query, rendered in each dialect's own placeholders.
	q := qb.Select("name").From("users").
		Where(qb.Eq{"active": true}, qb.NotIn("id", 4)).
		OrderBy("name")
	sqlText, args, err := q.BuildSQL(db.Dialect())
	if err != nil {
		log.Fatalf("%s build: %v", label, err)
	}
	fmt.Printf("builder sql     %s %v\n", sqlText, args)
	built, err := row.AllOf[string](ctx, db, q)
	if err != nil {
		log.Fatalf("%s builder: %v", label, err)
	}
	fmt.Printf("builder result  %v\n", built)

	// Write helpers with RETURNING.
	u := WriteUser{ID: 5, Name: "edsger", Email: "edsger@example.com", CreatedAt: now}
	if err := row.Insert(ctx, db, "users", &u, row.Returning("id", "name")); err != nil {
		log.Fatalf("%s insert: %v", label, err)
	}
	fmt.Printf("insert+return   id=%d name=%s\n", u.ID, u.Name)

	// The same violation, classified the same way on both engines.
	dup := WriteUser{ID: 6, Name: "impostor", Email: "ada@example.com", CreatedAt: now}
	err = row.Insert(ctx, db, "users", &dup)
	fmt.Printf("unique code     %v (IsCode=%v)\n", row.CodeOf(err), row.IsCode(err, row.UniqueViolation))
	if !row.IsCode(err, row.UniqueViolation) {
		log.Fatalf("%s: expected a unique violation, got %v", label, err)
	}
}

// splitStatements breaks a DDL script on ';', ignoring the trailing remainder.
func splitStatements(ddl string) []string {
	var out []string
	for _, s := range splitOn(ddl, ';') {
		if t := trimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func splitOn(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\n' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\n' || s[j-1] == '\t' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}
