# Contributing to row

Thanks for taking a look. Contributions are welcome, and small ones are just as
welcome as large ones — a typo fix, a clearer error message, a test for a case
that isn't covered are all genuinely useful.

If you're not sure whether an idea fits, open an issue and ask before writing
code. It's much nicer to discuss a direction early than to turn down a finished
pull request.

## Getting set up

```bash
git clone https://github.com/r-52/row
cd row
make test
```

`make test` runs everything that needs no server: the unit tests, the fuzz seed
corpus, and the SQLite integration suite. SQLite is pure Go here, so there's
nothing to install and no cgo.

For the full suite you'll want PostgreSQL, which the repository can start for
you:

```bash
make pg-up      # PostgreSQL 17 in Docker, on port 55432
make test-pg    # the whole suite, against both databases
make pg-down    # stop it again
```

If you'd rather point at a PostgreSQL you already have, set `ROW_PG_DSN`:

```bash
ROW_PG_DSN='postgres://user:pass@localhost:5432/mydb?sslmode=disable' go test ./...
```

The other targets:

| Command | What it does |
|---|---|
| `make test` | unit tests and SQLite integration, with `-race` |
| `make test-pg` | the same, against both databases, PostgreSQL required |
| `make fuzz` | fuzz the SQL scanner for a minute |
| `make bench` | benchmarks |
| `make vet` | `go vet ./...` |

## How the tests are organised

Three kinds, in three places:

**Unit tests** live beside the code they test (`lex_test.go`, `mapper_test.go`,
and so on). They need no database. Most of `row` is testable this way, and it's
the fastest loop.

**Integration tests** live in `integration/`. They run against *both* databases
through one helper:

```go
func TestSomething(t *testing.T) {
    dbtest.Each(t, func(t *testing.T, db *row.DB) {
        // this body runs once per database
    })
}
```

Anything that touches real SQL belongs here rather than in a unit test. Running
the same assertions on both engines is deliberate: something that works on
SQLite but not PostgreSQL is a bug in `row`, and it should show up as a red test
rather than as a surprise in production later.

**The fuzzer** (`FuzzLex`) checks that the SQL scanner never panics and that its
token ranges always tile the input exactly, given arbitrary bytes. If you change
`lex.go`, run `make fuzz` before opening a pull request.

By default a missing PostgreSQL makes the integration tests skip, so the suite
stays usable without Docker. Set `ROW_PG_REQUIRED=1` to turn that skip into a
failure. CI sets it, so a stopped container can't quietly reduce coverage.

## House conventions

Nothing surprising, but writing it down saves review round-trips:

- **Table-driven tests**, with a `name` on each case. When one fails you should
  be able to tell which case it was from the failure line alone.
- **Comments explain why, not what.** The code already says what it does. A
  comment earns its place by explaining a decision, a constraint, or a
  non-obvious consequence.
- **Errors say what went wrong and, where you can, what to do about it.**
  Compare `no field in User for result column "emial" ... fix the query, add a
  db tag, or pass row.Lax()` with a bare `missing destination name`. The first
  one ends the debugging session.
- **No new dependencies.** The two database drivers are the entire list, and
  keeping it that way is a feature rather than an accident. Everything else —
  the SQL scanner, the struct mapper, value conversion, the query builder — is
  written here on purpose.
- **`gofmt`, `go vet` and `go test -race` clean** before you open a pull
  request. CI checks all three.

## Adding support for another database

This is a nicely self-contained project if you're looking for somewhere to
start. A dialect is one type with six methods; `sqlite/sqlite.go` is a short,
complete example to work from.

1. **Create the package**, e.g. `mysql/`, with a type implementing
   `row.Dialect`:

   | Method | What it answers |
   |---|---|
   | `Name` | a short stable id, used in cache keys |
   | `DriverName` | what the driver registers with `database/sql` |
   | `AppendPlaceholder` | `$1`, `?`, `:1` … |
   | `QuoteIdent` | how identifiers are quoted |
   | `Features` | RETURNING, savepoints, upsert, parameter limit |
   | `ClassifyError` | the engine's error codes → `row.Code` |

2. **Add an `Open` function** that applies whatever defaults make sense for that
   engine. `sqlite.Open` is a good model — it turns on WAL, a busy timeout and
   foreign keys, and documents each one.

3. **Register it in `internal/dbtest`**, with the fixture schema written in that
   engine's DDL. This is where the value is: once it's registered, the entire
   existing integration suite — several hundred assertions — runs against your
   engine for free, and will tell you exactly what doesn't work yet.

4. **Write dialect unit tests**, particularly for the error-code table. Assert
   them against a live server if you can; `sqlite/sqlite_test.go` provokes each
   constraint violation for real rather than trusting the documentation, and
   that has already caught mistakes.

Please don't add the driver to the root module's imports — each adapter lives in
its own package precisely so a program only compiles the driver it uses.

## Pull requests

- One logical change per pull request. Two small ones review faster than one
  large one.
- Say *why* in the description, not just what. The diff shows what.
- Add or update tests in the same pull request. For a bug fix, a test that fails
  before your change and passes after it is the ideal.
- If you change behaviour that the README documents, update the README too.
  The README's code samples are meant to be correct as written.

## Reporting a bug

A failing test case is the most useful bug report there is, and it's often most
of the fix. If writing one isn't practical, then:

- the statement you ran,
- the struct you scanned into,
- the error message in full,
- and which database,

is plenty to work from. `row`'s errors include the operation and the statement
as it was actually sent, so pasting the whole message helps more than
summarising it.

## Security

If you find something with security implications, please report it privately
rather than opening a public issue.

## Code of conduct

Be decent to each other. Assume good faith, critique code rather than people,
and remember that the person on the other end is doing this voluntarily.
Behaviour that makes this an unpleasant place to work isn't welcome, and
maintainers will act on it.
