package row

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync/atomic"
	"time"
)

// Tx is a transaction, or — when opened inside another transaction — a
// savepoint within one.
type Tx struct {
	tx *sql.Tx
	d  Dialect
	c  *config
	pc *planCache

	// savepoint is empty for a real transaction and set for a nested one.
	savepoint string

	// depth is 0 for the outermost transaction.
	depth int

	done bool
}

// Dialect returns the dialect this transaction speaks.
func (t *Tx) Dialect() Dialect { return t.d }

func (t *Tx) conf() *config     { return t.c }
func (t *Tx) cache() *planCache { return t.pc }

func (t *Tx) queryContext(ctx context.Context, q string, args []any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, q, args...)
}

func (t *Tx) execContext(ctx context.Context, q string, args []any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, q, args...)
}

// SQL returns the underlying *sql.Tx.
func (t *Tx) SQL() *sql.Tx { return t.tx }

// Depth reports how deeply nested this transaction is; 0 is the outermost.
func (t *Tx) Depth() int { return t.depth }

// txConfig holds the settings for one transaction.
type txConfig struct {
	iso      sql.IsolationLevel
	readOnly bool
}

// TxOption configures a transaction.
type TxOption func(*txConfig)

// Isolation sets the transaction isolation level. Nested transactions inherit
// the outermost level; a savepoint cannot change it, and passing Isolation to
// a nested InTx is an error rather than a silently ignored request.
func Isolation(level sql.IsolationLevel) TxOption {
	return func(c *txConfig) { c.iso = level }
}

// ReadOnly marks the transaction read-only, which lets the engine take cheaper
// locks and catches accidental writes.
func ReadOnly() TxOption {
	return func(c *txConfig) { c.readOnly = true }
}

// beginner is implemented by the sessions a transaction can be started from.
type beginner interface {
	begin(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

func (db *DB) begin(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return db.db.BeginTx(ctx, opts)
}

func (c *Conn) begin(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return c.cn.BeginTx(ctx, opts)
}

// savepointSeq makes savepoint names unique within a process, so a name can
// never collide with one from an enclosing scope.
var savepointSeq atomic.Uint64

// InTx runs fn inside a transaction.
//
// The transaction commits when fn returns nil and rolls back when it returns an
// error, which is returned unchanged. If fn panics, the transaction is rolled
// back and the panic continues to propagate — a panicking handler must never
// leave a transaction open.
//
// Calling InTx with a *Tx nests: it opens a savepoint, so an inner failure
// undoes only the inner work. That composes, which means a function that needs
// a transaction can simply ask for one without knowing whether its caller
// already started one.
//
//	err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
//	    if err := row.Insert(ctx, tx, "users", &u); err != nil {
//	        return err
//	    }
//	    return row.InTx(ctx, tx, func(ctx context.Context, tx *row.Tx) error {
//	        return row.Insert(ctx, tx, "audit", &a) // its own savepoint
//	    })
//	})
func InTx(ctx context.Context, s Session, fn func(context.Context, *Tx) error, opts ...TxOption) error {
	const op = "row.InTx"
	if err := mustSession(op, s); err != nil {
		return err
	}

	var cfg txConfig
	for _, o := range opts {
		o(&cfg)
	}

	// Nested case: a savepoint inside the transaction already in progress.
	if parent, ok := s.(*Tx); ok {
		if len(opts) > 0 {
			return fmt.Errorf("%s: isolation and read-only apply to the outermost "+
				"transaction only; this call is nested at depth %d", op, parent.depth+1)
		}
		if !parent.d.Features().Savepoints {
			return fmt.Errorf("%s: %s does not support savepoints, so transactions "+
				"cannot nest", op, parent.d.Name())
		}
		return parent.nest(ctx, fn)
	}

	b, ok := s.(beginner)
	if !ok {
		return fmt.Errorf("%s: cannot start a transaction from %T", op, s)
	}

	sqlTx, err := b.begin(ctx, &sql.TxOptions{Isolation: cfg.iso, ReadOnly: cfg.readOnly})
	if err != nil {
		return wrap(op, "", s.Dialect(), err)
	}
	tx := &Tx{tx: sqlTx, d: s.Dialect(), c: s.conf(), pc: s.cache()}

	defer func() {
		if p := recover(); p != nil {
			_ = sqlTx.Rollback()
			panic(p)
		}
	}()

	if err := fn(ctx, tx); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
			return fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
		}
		return err
	}
	tx.done = true
	return wrap(op, "", tx.d, sqlTx.Commit())
}

// nest runs fn inside a savepoint of t.
func (t *Tx) nest(ctx context.Context, fn func(context.Context, *Tx) error) error {
	const op = "row.InTx"
	name := "row_sp_" + strconv.FormatUint(savepointSeq.Add(1), 10)

	if _, err := t.tx.ExecContext(ctx, "SAVEPOINT "+t.d.QuoteIdent(name)); err != nil {
		return wrap(op, "SAVEPOINT", t.d, err)
	}

	inner := &Tx{tx: t.tx, d: t.d, c: t.c, pc: t.pc, savepoint: name, depth: t.depth + 1}

	defer func() {
		if p := recover(); p != nil {
			_, _ = t.tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+t.d.QuoteIdent(name))
			panic(p)
		}
	}()

	if err := fn(ctx, inner); err != nil {
		if _, rbErr := t.tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+t.d.QuoteIdent(name)); rbErr != nil {
			return fmt.Errorf("%w (savepoint rollback also failed: %v)", err, rbErr)
		}
		// Releasing after rollback discards the savepoint without discarding
		// the transaction, leaving the caller free to continue.
		_, _ = t.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+t.d.QuoteIdent(name))
		return err
	}
	inner.done = true
	if _, err := t.tx.ExecContext(ctx, "RELEASE SAVEPOINT "+t.d.QuoteIdent(name)); err != nil {
		return wrap(op, "RELEASE SAVEPOINT", t.d, err)
	}
	return nil
}

// RetryPolicy controls InTxRetry.
type RetryPolicy struct {
	// Attempts is the total number of tries, including the first. Values below
	// 1 are treated as 1.
	Attempts int

	// BaseDelay is the delay before the second attempt. It doubles each time,
	// up to MaxDelay.
	BaseDelay time.Duration

	// MaxDelay caps the backoff. Zero means no cap.
	MaxDelay time.Duration
}

// DefaultRetryPolicy is a reasonable starting point: five attempts with
// exponential backoff from 5ms, capped at 200ms.
var DefaultRetryPolicy = RetryPolicy{Attempts: 5, BaseDelay: 5 * time.Millisecond, MaxDelay: 200 * time.Millisecond}

// InTxRetry runs fn in a transaction, retrying when the engine reports a
// failure that a retry can fix: a serialization failure, a deadlock, or a
// locked SQLite database.
//
// This is what SERIALIZABLE isolation requires in practice — the database is
// entitled to abort a transaction that cannot be ordered, and the application
// is expected to try again. fn must therefore be safe to run more than once.
func InTxRetry(ctx context.Context, s Session, p RetryPolicy, fn func(context.Context, *Tx) error, opts ...TxOption) error {
	attempts := p.Attempts
	if attempts < 1 {
		attempts = 1
	}
	delay := p.BaseDelay

	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			// Full jitter: spreading retries stops contending transactions
			// from colliding again in lockstep.
			d := delay
			if p.MaxDelay > 0 && d > p.MaxDelay {
				d = p.MaxDelay
			}
			wait := time.Duration(rand.Int64N(int64(d) + 1))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			delay *= 2
		}

		err = InTx(ctx, s, fn, opts...)
		if err == nil {
			return nil
		}
		if !CodeOf(err).Retryable() {
			return err
		}
	}
	return fmt.Errorf("row.InTxRetry: giving up after %d attempts: %w", attempts, err)
}
