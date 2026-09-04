// Package row maps SQL result rows onto Go values.
//
// It is a thin layer over database/sql that removes the scanning boilerplate
// without becoming an ORM. Destinations are generic type parameters rather than
// interface{}, parameter binding is driven by a real SQL scanner instead of a
// search-and-replace, and errors carry a portable classification.
//
// A minimal program:
//
//	db, err := sqlite.Open(ctx, ":memory:")
//	...
//	users, err := row.All[User](ctx, db,
//	    `SELECT id, name FROM users WHERE org_id = :org`,
//	    row.Args{"org": 7})
//
// row supports Postgres (through github.com/jackc/pgx/v5) and SQLite (through
// modernc.org/sqlite). The root package imports neither, so a program compiles
// only the driver it actually uses.
package row

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// defaultPlanCacheSize is the number of compiled statements kept per database.
// Applications reuse a small, fixed set of statements, so this is generous.
const defaultPlanCacheSize = 1024

// config holds the settings shared by a DB and everything derived from it.
//
// They are per-database rather than package-level, so two databases in one
// process can use different naming conventions without fighting over a global.
type config struct {
	nameMapper func(string) string
	strict     bool
	cacheSize  int
	hooks      []Hook
}

// Option configures a DB.
type Option func(*config)

// WithNameMapper sets the function that derives a column name from a struct
// field name when the field carries no db tag. The default is SnakeCase.
func WithNameMapper(f func(string) string) Option {
	return func(c *config) { c.nameMapper = f }
}

// Lax stops row from treating an unmatched result column as an error.
//
// By default a column with no corresponding struct field fails the scan, which
// catches typos and stale queries. Lax is the escape hatch for queries that
// deliberately select more than they map.
func Lax() Option {
	return func(c *config) { c.strict = false }
}

// WithPlanCacheSize sets how many compiled statements to keep. Zero disables
// caching entirely, which is only useful when generating unbounded distinct SQL.
func WithPlanCacheSize(n int) Option {
	return func(c *config) { c.cacheSize = n }
}

// WithHook registers an observer of statement execution. Hooks run in
// registration order.
func WithHook(h Hook) Option {
	return func(c *config) { c.hooks = append(c.hooks, h) }
}

func newConfig(opts []Option) *config {
	c := &config{
		nameMapper: SnakeCase,
		strict:     true,
		cacheSize:  defaultPlanCacheSize,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Session is what One, All, Iter and Exec accept. It is implemented by *DB,
// *Tx and *Conn.
//
// The interface is sealed: its methods are unexported, so it cannot be
// implemented outside this package. That is deliberate — it is an internal
// dispatch mechanism, not an extension point. To fake a database in tests,
// point row at a real in-memory SQLite or at your own database/sql driver.
type Session interface {
	// Dialect reports the engine this session speaks. Query builders use it to
	// render placeholders and quote identifiers.
	Dialect() Dialect

	conf() *config
	cache() *planCache
	queryContext(ctx context.Context, query string, args []any) (*sql.Rows, error)
	execContext(ctx context.Context, query string, args []any) (sql.Result, error)
}

// DB is a handle to a database. It wraps *sql.DB and is safe for concurrent
// use by multiple goroutines.
type DB struct {
	db *sql.DB
	d  Dialect
	c  *config
	pc *planCache
}

// New wraps an existing *sql.DB.
//
// Use it when the pool is configured elsewhere, or to reach a database row has
// no adapter for. The dialect must match the driver the pool was opened with.
func New(db *sql.DB, d Dialect, opts ...Option) *DB {
	c := newConfig(opts)
	return &DB{db: db, d: d, c: c, pc: newPlanCache(c.cacheSize)}
}

// Open opens a database using the dialect's registered driver name.
//
// It is the low-level form; the adapter packages provide friendlier
// constructors that also apply engine-appropriate defaults.
func Open(d Dialect, dsn string, opts ...Option) (*DB, error) {
	db, err := sql.Open(d.DriverName(), dsn)
	if err != nil {
		return nil, wrap("row.Open", "", d, err)
	}
	return New(db, d, opts...), nil
}

// SQL returns the underlying *sql.DB, for pool tuning and for the occasional
// operation row does not cover. row never hides it.
func (db *DB) SQL() *sql.DB { return db.db }

// Dialect returns the dialect this handle speaks.
func (db *DB) Dialect() Dialect { return db.d }

// Close closes the underlying pool.
func (db *DB) Close() error { return db.db.Close() }

// Ping verifies the database is reachable.
func (db *DB) Ping(ctx context.Context) error {
	return wrap("row.Ping", "", db.d, db.db.PingContext(ctx))
}

func (db *DB) conf() *config     { return db.c }
func (db *DB) cache() *planCache { return db.pc }

func (db *DB) queryContext(ctx context.Context, q string, args []any) (*sql.Rows, error) {
	return db.db.QueryContext(ctx, q, args...)
}

func (db *DB) execContext(ctx context.Context, q string, args []any) (sql.Result, error) {
	return db.db.ExecContext(ctx, q, args...)
}

// Conn is a single database connection reserved from the pool. Use it when a
// sequence of statements must run on the same connection — session settings,
// temporary tables, advisory locks.
type Conn struct {
	cn *sql.Conn
	d  Dialect
	c  *config
	pc *planCache
}

// Conn reserves a connection from the pool. The caller must Close it to return
// the connection.
func (db *DB) Conn(ctx context.Context) (*Conn, error) {
	cn, err := db.db.Conn(ctx)
	if err != nil {
		return nil, wrap("row.Conn", "", db.d, err)
	}
	return &Conn{cn: cn, d: db.d, c: db.c, pc: db.pc}, nil
}

// Close returns the connection to the pool.
func (c *Conn) Close() error { return c.cn.Close() }

// SQL returns the underlying *sql.Conn.
func (c *Conn) SQL() *sql.Conn { return c.cn }

// Dialect returns the dialect this connection speaks.
func (c *Conn) Dialect() Dialect { return c.d }

func (c *Conn) conf() *config     { return c.c }
func (c *Conn) cache() *planCache { return c.pc }

func (c *Conn) queryContext(ctx context.Context, q string, args []any) (*sql.Rows, error) {
	return c.cn.QueryContext(ctx, q, args...)
}

func (c *Conn) execContext(ctx context.Context, q string, args []any) (sql.Result, error) {
	return c.cn.ExecContext(ctx, q, args...)
}

// prepare compiles, caches and binds a statement, returning what the driver
// needs. It is the single path every row operation takes to reach the driver.
func prepare(s Session, query string, args []any) (string, []any, error) {
	d := s.Dialect()

	var p *plan
	pc := s.cache()
	key := d.Name() + "\x00" + query
	if pc != nil {
		if cached, ok := pc.get(key); ok {
			p = cached
		}
	}
	if p == nil {
		var err error
		if p, err = compile(query); err != nil {
			return "", nil, err
		}
		if pc != nil {
			pc.put(key, p)
		}
	}

	src, pos, err := splitArgs(s, args)
	if err != nil {
		return "", nil, err
	}
	return p.render(d, src, pos)
}

// splitArgs decides whether the caller passed named or positional arguments.
//
// A single Args map, or a single struct, means named. Anything else is
// positional. Accepting a struct here is what lets every read and write
// function take named arguments without a separate Named* variant of each.
func splitArgs(s Session, args []any) (namedSource, []any, error) {
	if len(args) != 1 {
		return nil, args, nil
	}
	switch v := args[0].(type) {
	case Args:
		return argsSource(v), nil, nil
	case map[string]any:
		return argsSource(v), nil, nil
	}
	src, ok, err := structSource(args[0], s.conf().nameMapper)
	if err != nil {
		return nil, nil, err
	}
	if ok {
		return src, nil, nil
	}
	return nil, args, nil
}

// runQuery executes a query with hooks applied.
func runQuery(ctx context.Context, s Session, op, query string, args []any) (*sql.Rows, error) {
	info := &QueryInfo{Op: op, SQL: query, Args: args, Started: time.Now(), RowsAffected: -1}
	ctx = fireBefore(ctx, s, info)
	rows, err := s.queryContext(ctx, query, args)
	info.Duration = time.Since(info.Started)
	info.Err = err
	fireAfter(ctx, s, info)
	if err != nil {
		return nil, wrap(op, query, s.Dialect(), err)
	}
	return rows, nil
}

// runExec executes a non-query statement with hooks applied.
func runExec(ctx context.Context, s Session, op, query string, args []any) (sql.Result, error) {
	info := &QueryInfo{Op: op, SQL: query, Args: args, Started: time.Now(), RowsAffected: -1}
	ctx = fireBefore(ctx, s, info)
	res, err := s.execContext(ctx, query, args)
	info.Duration = time.Since(info.Started)
	info.Err = err
	if err == nil && res != nil {
		if n, aerr := res.RowsAffected(); aerr == nil {
			info.RowsAffected = n
		}
	}
	fireAfter(ctx, s, info)
	if err != nil {
		return nil, wrap(op, query, s.Dialect(), err)
	}
	return res, nil
}

func fireBefore(ctx context.Context, s Session, info *QueryInfo) context.Context {
	for _, h := range s.conf().hooks {
		ctx = h.BeforeQuery(ctx, info)
	}
	return ctx
}

func fireAfter(ctx context.Context, s Session, info *QueryInfo) {
	hooks := s.conf().hooks
	for i := len(hooks) - 1; i >= 0; i-- {
		hooks[i].AfterQuery(ctx, info)
	}
}

// Exec runs a statement that returns no rows and reports the result.
func Exec(ctx context.Context, s Session, query string, args ...any) (sql.Result, error) {
	const op = "row.Exec"
	q, a, err := bindFor(op, s, query, args)
	if err != nil {
		return nil, err
	}
	return runExec(ctx, s, op, q, a)
}

// Affected runs a statement and reports how many rows it changed. It is Exec
// for the common case where the count is the only interesting part.
func Affected(ctx context.Context, s Session, query string, args ...any) (int64, error) {
	res, err := Exec(ctx, s, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrap("row.Affected", query, s.Dialect(), err)
	}
	return n, nil
}

// mustSession guards against a nil Session reaching the driver, which would
// otherwise panic somewhere much less informative.
func mustSession(op string, s Session) error {
	if s == nil {
		return fmt.Errorf("%s: nil Session", op)
	}
	return nil
}
