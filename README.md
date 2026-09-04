# row

[![CI](https://github.com/r-52/row/actions/workflows/ci.yml/badge.svg)](https://github.com/r-52/row/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/r-52/row.svg)](https://pkg.go.dev/github.com/r-52/row)
[![Go 1.24+](https://img.shields.io/badge/go-1.24%2B-00ADD8)](https://go.dev/dl/)
[![MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

A small, typed layer over `database/sql` for Go.

`row` takes care of the mechanical part of talking to a SQL database — turning
result rows into Go values, binding parameters, managing transactions — and
leaves the SQL to you. It is not an ORM, and it doesn't want to be. You write
the query; `row` handles everything between the query and your structs.

```go
users, err := row.All[User](ctx, db,
    `SELECT id, name FROM users WHERE org_id = :org AND id IN (:ids)`,
    row.Args{"org": 7, "ids": []int64{1, 2, 3}})
```

That's the whole idea. The rest of this page is the details.

## Contents

- [Install](#install)
- [Getting started](#getting-started)
- [Reading rows](#reading-rows)
- [Parameters](#parameters)
- [Mapping structs](#mapping-structs)
- [Writing rows](#writing-rows)
- [Transactions](#transactions)
- [Errors](#errors)
- [Query builder](#query-builder)
- [Logging and tracing](#logging-and-tracing)
- [Performance](#performance)
- [How it works](#how-it-works)
- [Contributing](#contributing)

## Install

```bash
go get github.com/r-52/row
```

Requires Go 1.24 or later.

Then import the adapter for your database. The root package imports neither
driver, so your binary only includes the one you actually use.

| Database | Import | Driver |
|---|---|---|
| PostgreSQL | `github.com/r-52/row/pg` | [`jackc/pgx/v5`](https://github.com/jackc/pgx) |
| SQLite | `github.com/r-52/row/sqlite` | [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite) — pure Go, no cgo |

Those two drivers are the only third-party dependencies in the project.

## Getting started

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/r-52/row"
    "github.com/r-52/row/sqlite"
)

type User struct {
    ID     int64 `db:"id,pk"`
    Name   string
    Email  string
    Active bool
}

func main() {
    ctx := context.Background()

    db, err := sqlite.Open(ctx, ":memory:")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    if _, err := row.Exec(ctx, db, `
        CREATE TABLE users (
            id     INTEGER PRIMARY KEY,
            name   TEXT NOT NULL,
            email  TEXT NOT NULL UNIQUE,
            active BOOLEAN NOT NULL
        )`); err != nil {
        log.Fatal(err)
    }

    u := User{ID: 1, Name: "ada", Email: "ada@example.com", Active: true}
    if err := row.Insert(ctx, db, "users", &u); err != nil {
        log.Fatal(err)
    }

    active, err := row.All[User](ctx, db,
        `SELECT id, name, email, active FROM users WHERE active = :active`,
        row.Args{"active": true})
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println(active) // [{1 ada ada@example.com true}]
}
```

That program runs as written. Change `":memory:"` to a filename such as
`"app.db"` to keep the data. To talk to PostgreSQL instead, import
`github.com/r-52/row/pg` and call `pg.Open(ctx, dsn)` — everything below that
line stays exactly the same.

## Reading rows

Four functions cover reading. Each takes the destination as a type parameter,
so the compiler checks it for you.

```go
one,   err := row.One[User](ctx, db, q, args)    // exactly one row
first, err := row.First[User](ctx, db, q, args)  // the first row, ignore the rest
users, err := row.All[User](ctx, db, q, args)    // every row
total, err := row.One[int64](ctx, db, `SELECT count(*) FROM users`)

for u, err := range row.Iter[User](ctx, db, q) { // stream, don't collect
    if err != nil {
        return err
    }
    ...
}
```

The destination type can be:

- a struct or a `*struct`
- a scalar — `int`, `string`, `time.Time`, `[]byte`, `sql.Null[T]`, or anything
  implementing `sql.Scanner`
- `map[string]any`, when the columns aren't known ahead of time

`One` returns an error if the query matched more than one row. That's usually a
sign the `WHERE` clause is broader than intended, and it seemed better to say so
than to quietly take the first result. When extra rows are expected and you want
the first anyway, use `First`.

`Iter` returns an `iter.Seq2[T, error]` and closes the underlying result when the
loop ends — including when you `break` out early.

### Two helpful strictnesses

**Unmatched columns are an error.** If a query selects a column no struct field
maps to, `row` says so, and lists both the columns it couldn't place and the
fields that were available:

```
row.All: no field in main.User for result column "emial"
  main.User has columns: active, email, id, name
  fix the query, add a db tag, or pass row.Lax() to ignore extra columns
  sql: SELECT id, name, email, active, id AS emial FROM users
```

This catches a renamed column or a typo at the first query rather than as a
mysteriously empty field later. If you deliberately select more than you map,
pass `row.Lax()` when opening the database.

**`NULL` into a non-nullable Go type is an error.** A `NULL` arriving at a plain
`string` field would otherwise become `""`, which is indistinguishable from a
real empty string. `row` tells you to use a `*string` or `sql.Null[string]`
instead.

## Parameters

There are three ways to write placeholders. `row` works out which one a
statement uses; mixing two in one statement is an error.

```go
// Named — the recommended default.
row.All[User](ctx, db, `... WHERE id = :id`, row.Args{"id": 7})

// Named, filled from a struct's fields.
row.All[User](ctx, db, `... WHERE id = :id AND org_id = :org_id`, someUser)

// Ordinal.
row.All[User](ctx, db, `... WHERE id = ?`, 7)

// Your database's native placeholders, passed through untouched.
row.All[User](ctx, db, `... WHERE id = $1`, 7)
```

### Slices expand automatically

A slice argument becomes a placeholder list, so `IN` needs no special handling:

```go
row.All[User](ctx, db, `SELECT * FROM users WHERE id IN (:ids)`,
    row.Args{"ids": []int64{1, 2, 3}})
// SELECT * FROM users WHERE id IN ($1,$2,$3)
```

An empty slice becomes `IN (NULL)`, which matches nothing — the right answer for
membership in an empty set, and valid SQL either way.

`[]byte`, `string`, and any type implementing `driver.Valuer` (PostgreSQL array
types, for instance) are treated as single values and passed through whole.

### Your SQL is read properly

`row` parses each statement with a real SQL scanner rather than searching for
punctuation, so parameters are found and everything else is left alone:

- `'strings'` with `''` escapes, and PostgreSQL `E'\''` escape strings
- `$tag$ dollar-quoted bodies $tag$`, including nested lookalike tags
- `"quoted"` and `` `quoted` `` identifiers
- `-- line comments` and `/* nested /* block */ comments */`
- `::casts`, which are not parameters
- PostgreSQL's `?`, `?|` and `?&` JSON operators, which are not placeholders

That last point is worth spelling out. In a statement that uses named
parameters, a bare `?` is always the JSON existence operator, never a
placeholder — so this works as written:

```go
row.All[Doc](ctx, db, `SELECT * FROM docs WHERE data ? 'key' AND org = :org`,
    row.Args{"org": 7})
```

Write `??` anywhere you want a literal question mark.

Parsed statements are cached, so a statement is scanned once however many
times you run it.

## Mapping structs

Column names come from field names by default, converted to snake case
(`UserID` → `user_id`, `HTTPCode` → `http_code`). Add a `db` tag for the
exceptions.

```go
type User struct {
    ID        int64     `db:"id,pk"`               // primary key, used by Update and Delete
    Name      string                               // -> name
    CreatedAt time.Time `db:"created_at,readonly"` // read, never written
    Secret    string    `db:"-"`                   // ignored entirely
    Address   *Address                             // -> address_city, address_zip
    Audit     Audit     `db:",inline"`             // merged in, no prefix
    Home      Address   `db:",prefix=home_"`       // -> home_city, home_zip
}
```

Tag options: `pk`, `readonly`, `inline`, `prefix=`, and `-` to skip.

Embedded structs merge into the parent's namespace. Named struct fields get a
prefix derived from their column name, which lines up with what
`SELECT a.city AS address_city` produces. If two fields end up claiming the same
column, `row` reports it and names both — no silent first-wins.

You can change the naming rule per database with
`row.WithNameMapper(myFunc)`. It's a per-`DB` setting, so two databases in the
same process can use different conventions.

### Joined structs can be nil

A `*Struct` field is left `nil` when every column belonging to it came back
`NULL`:

```go
type Member struct {
    ID   int64 `db:"id,pk"`
    Name string
    Org  *Org  // nil when the LEFT JOIN found no match
}

members, _ := row.All[Member](ctx, db, `
    SELECT u.id AS id, u.name AS name, o.id AS org_id, o.name AS org_name
    FROM users u LEFT JOIN orgs o ON o.id = u.org_id`)
```

A zeroed `Org{ID: 0, Name: ""}` looks exactly like a real row that happens to
have those values. `nil` doesn't, which is why it's worth the trouble.

## Writing rows

```go
err := row.Insert(ctx, db, "users", &u)
err = row.Insert(ctx, db, "users", &u, row.Returning("id", "created_at"))
err = row.InsertMany(ctx, db, "users", users) // batched automatically
err = row.Update(ctx, db, "users", &u)        // matches on the pk field
err = row.Update(ctx, db, "users", &u, row.Only("name", "email"))
err = row.Delete(ctx, db, "users", &u)
```

`Returning` scans the result back into your struct, which is how a
database-generated id or timestamp finds its way home. Both PostgreSQL and
SQLite support it. Mark generated columns `readonly` so they're never written:

```go
type Note struct {
    ID        int64     `db:"id,pk,readonly"`
    Body      string
    CreatedAt time.Time `db:"created_at,readonly"`
}

n := Note{Body: "hello"}
err := row.Insert(ctx, db, "notes", &n, row.Returning("id", "created_at"))
// n.ID and n.CreatedAt are now filled in
```

`InsertMany` groups rows into multi-row `VALUES` clauses and splits them so no
single statement exceeds the engine's parameter limit. It isn't atomic by
itself — wrap it in `InTx` when all the rows need to land together.

Use `Only` and `Omit` to narrow which columns a write touches.

## Transactions

```go
err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
    if err := row.Insert(ctx, tx, "users", &u); err != nil {
        return err
    }
    return row.Insert(ctx, tx, "audit", &a)
})
```

Commits when the function returns `nil`. Rolls back and returns your error
unchanged when it doesn't. Rolls back and re-panics if it panics.

### Nesting works

Calling `InTx` with a `*Tx` opens a `SAVEPOINT` instead of a new transaction, so
an inner failure undoes only the inner work and the outer transaction carries
on:

```go
row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
    if err := row.Insert(ctx, tx, "users", &u); err != nil {
        return err
    }
    // This block gets its own savepoint. If it fails, only it is undone.
    if err := row.InTx(ctx, tx, recordOptionalThing); err != nil {
        log.Printf("optional step failed: %v", err)
    }
    return nil
})
```

This means a function that needs a transaction can just ask for one, without
knowing whether its caller already started one.

### Retrying

```go
err := row.InTxRetry(ctx, db, row.DefaultRetryPolicy, func(ctx context.Context, tx *row.Tx) error {
    ...
})
```

Retries with jittered exponential backoff, but only for failures a retry can
actually fix — serialization failures, deadlocks, and a busy SQLite. Anything
else is returned immediately, because retrying a unique-key violation just
fails again. Under `SERIALIZABLE` isolation this is what the database expects
you to do.

## Errors

Constraint violations are classified into portable codes, so you can branch on
one without importing a driver:

```go
if err := row.Insert(ctx, db, "users", &u); err != nil {
    if row.IsCode(err, row.UniqueViolation) {
        return ErrEmailTaken
    }
    return err
}
```

The codes are `UniqueViolation`, `ForeignKeyViolation`, `NotNullViolation`,
`CheckViolation`, `Deadlock`, `SerializationFailure`, `Busy` and `Timeout`. Each
one is verified against a live server for both databases in the test suite,
rather than transcribed from documentation.

An error `row` doesn't recognise stays `Unknown` — it never guesses.

`row.ErrNoRows` *is* `sql.ErrNoRows`, so `errors.Is` matches either spelling and
existing code keeps working.

Error messages carry the operation and the statement that produced them:

```
row.Insert [unique_violation]: ERROR: duplicate key value violates unique constraint "users_email_key" (SQLSTATE 23505)
  sql: INSERT INTO "users" ("id", "name", "email", "active") VALUES ($1, $2, $3, $4)
```

Every error names the operation, the portable code where there is one, and the
statement as it was actually sent.

## Query builder

Most queries read better written out as SQL. The `row/qb` subpackage is for the
ones whose *shape* changes with the request — a search form where each filter
may or may not apply:

```go
q := qb.Select("id", "name").From("users").Where(qb.Eq{"org_id": orgID})

if search != "" {
    q = q.Where(qb.ILike{"name": "%" + search + "%"})
}
if onlyActive {
    q = q.Where(qb.Eq{"active": true})
}

users, err := row.AllOf[User](ctx, db, q.OrderBy("created_at DESC").Limit(50))
```

Builders are immutable — every method returns a new value — so a partly-built
query is safe to keep as a template and branch from.

Conditions include `Eq`, `NotEq`, `Lt`, `Lte`, `Gt`, `Gte`, `Like`, `ILike`,
`In`, `NotIn`, `IsNull`, `NotNull`, `Between`, `And`, `Or` and `Not`. In an `Eq`,
a `nil` becomes `IS NULL` and a slice becomes `IN (...)`, because that's what
you meant.

Every value becomes a bind parameter. `qb.Raw` is the escape hatch for anything
the builder can't express, and is the one place text is passed through as-is:

```go
qb.Raw("data @> ?::jsonb", filter)
```

`UPDATE` and `DELETE` without a `WHERE` clause are refused, since that's rarely
what anyone meant to write. Say `.Where(qb.Raw("1 = 1"))` when it is.

## Logging and tracing

```go
db, err := pg.Open(ctx, dsn,
    row.WithHook(row.SlogHook(logger, slog.LevelDebug, 100*time.Millisecond)))
```

Statements slower than the threshold log at warn level, failures at error.

For tracing, implement `row.Hook` yourself. The context you return from
`BeforeQuery` is the one used for the call, so a span opened there is available
again in `AfterQuery`.

## Performance

Compared with writing the scan loop out by hand (SQLite, in memory, Apple M3):

```
BenchmarkScan/row/1000-8            1855833 ns/op   685146 B/op   9794 allocs/op
BenchmarkScan/handwritten/1000-8    1382845 ns/op   556326 B/op   8787 allocs/op
```

Roughly 34% more time and 11% more allocations — about half a microsecond per
row. That's the cost of reflection-driven mapping.

Worth knowing where that actually goes, though. Profiling the benchmark above
attributes about three quarters of the allocations to the SQLite driver itself,
and `row`'s own share comes to exactly two per row: one to allocate the struct,
one to hand it back as a `T`. So the number above says as much about the driver
as it does about `row`, and it will look different on PostgreSQL.

If the overhead matters in a particular hot path, write those `Scan` calls by
hand. `db.SQL()` returns the underlying `*sql.DB`, and `row` never hides it from
you.

## How it works

Worth reading before your first change. Each file has one job:

| File | Job |
|---|---|
| `row.go` | `DB`, `Conn`, the `Session` interface, options |
| `dialect.go` | the `Dialect` interface — the only thing databases differ by |
| `lex.go` | the SQL scanner |
| `bind.go` | compiling statements and binding arguments |
| `cache.go` | the compiled-statement cache |
| `mapper.go` | struct ⇄ column mapping, and the reflection cache |
| `convert.go` | assigning driver values to Go fields |
| `scan.go` | `One`, `All`, `First`, `Iter` |
| `write.go` | `Insert`, `Update`, `Delete`, `InsertMany` |
| `tx.go` | transactions, savepoints, retries |
| `errors.go` | error types and portable codes |
| `hook.go` | the observability interface |
| `builder.go` | the `Builder` seam and the `*Of` executors |
| `qb/` | the query builder |
| `pg/`, `sqlite/` | the two dialect adapters |

Two design notes that explain most of the structure:

**Everything sits on `database/sql`.** Connection pooling, transactions and
prepared statements come from the standard library. A `Dialect` supplies only
what genuinely differs between engines: placeholder syntax, identifier quoting,
a feature list, and how to read the engine's error codes. That's why there is
one code path rather than one per database.

**Value conversion is ours** rather than the standard library's, because
`row` needs to see when every column of a nested struct came back `NULL`. That's
what makes nil-able joined structs possible, and it's also where scan errors get
the detail that makes them useful.

## Contributing

Contributions are welcome, and small ones are just as welcome as large ones.
[CONTRIBUTING.md](CONTRIBUTING.md) has the details; the short version:

```bash
git clone https://github.com/r-52/row
cd row
make test        # unit tests and SQLite integration — nothing else needed

make pg-up       # start PostgreSQL 17 in Docker
make test-pg     # run everything against both databases
make pg-down
```

Tests that touch real SQL go in `integration/`, where `dbtest.Each` runs them
against **both** databases. That's deliberate: something that works on SQLite
but not PostgreSQL is a bug in `row`, and it should surface as a red test rather
than as a surprise later.

Adding another database is a nicely self-contained first project — a dialect is
one type with six methods, and registering it in `internal/dbtest` runs the
entire existing suite against it. [The guide walks through
it.](CONTRIBUTING.md#adding-support-for-another-database)

A failing test case is the most useful bug report there is, and usually most of
the fix.

## Non-goals

`row` maps rows. It deliberately doesn't do migrations, schema reflection,
relation loading, caching, or code generation. There are good focused tools for
each of those, and they compose better than one library trying to do everything.

## License

MIT. See [LICENSE](LICENSE).
