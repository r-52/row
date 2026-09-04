package row

import (
	"context"
	"database/sql"
	"iter"
)

// Builder is a query that can render itself for a specific dialect.
//
// It is the seam between row and a query builder: row declares the interface
// and never imports a builder, so the two stay independent. The row/qb
// subpackage implements it, and so can anything else.
type Builder interface {
	// BuildSQL renders the query in d's native form. The returned SQL already
	// carries d's placeholders and the arguments are already positional, so
	// row passes both straight to the driver.
	BuildSQL(d Dialect) (query string, args []any, err error)
}

// The *Of functions execute a Builder. They skip row's statement compilation
// entirely — a builder has already produced native SQL, so re-parsing it would
// be wasted work, and caching a plan per generated statement would churn the
// cache for queries whose text varies with their arguments.

// AllOf runs a built query and returns every row as a T.
func AllOf[T any](ctx context.Context, s Session, b Builder) ([]T, error) {
	const op = "row.AllOf"
	q, a, err := buildFor(op, s, b)
	if err != nil {
		return nil, err
	}
	return collect[T](ctx, s, op, q, a)
}

// OneOf runs a built query that must return exactly one row.
func OneOf[T any](ctx context.Context, s Session, b Builder) (T, error) {
	const op = "row.OneOf"
	var zero T
	q, a, err := buildFor(op, s, b)
	if err != nil {
		return zero, err
	}
	return single[T](ctx, s, op, q, a, true)
}

// FirstOf runs a built query and returns its first row, ignoring any others.
func FirstOf[T any](ctx context.Context, s Session, b Builder) (T, error) {
	const op = "row.FirstOf"
	var zero T
	q, a, err := buildFor(op, s, b)
	if err != nil {
		return zero, err
	}
	return single[T](ctx, s, op, q, a, false)
}

// IterOf streams the rows of a built query.
func IterOf[T any](ctx context.Context, s Session, b Builder) iter.Seq2[T, error] {
	const op = "row.IterOf"
	return func(yield func(T, error) bool) {
		var zero T
		q, a, err := buildFor(op, s, b)
		if err != nil {
			yield(zero, err)
			return
		}
		stream[T](ctx, s, op, q, a, yield)
	}
}

// ExecOf runs a built statement that returns no rows.
func ExecOf(ctx context.Context, s Session, b Builder) (sql.Result, error) {
	const op = "row.ExecOf"
	q, a, err := buildFor(op, s, b)
	if err != nil {
		return nil, err
	}
	return runExec(ctx, s, op, q, a)
}

func buildFor(op string, s Session, b Builder) (string, []any, error) {
	if err := mustSession(op, s); err != nil {
		return "", nil, err
	}
	if b == nil {
		return "", nil, wrap(op, "", nil, errNilBuilder)
	}
	q, a, err := b.BuildSQL(s.Dialect())
	if err != nil {
		return "", nil, wrap(op, q, s.Dialect(), err)
	}
	return q, a, nil
}

var errNilBuilder = errorString("nil Builder")

type errorString string

func (e errorString) Error() string { return string(e) }
