// Package qb builds SQL statements programmatically.
//
// It is a light builder, not an ORM and not a replacement for writing SQL. Its
// job is the case hand-written SQL handles badly: a query whose shape depends
// on which filters the caller supplied. For a query with a fixed shape, write
// the SQL — it will be clearer than any builder call chain.
//
//	q := qb.Select("id", "name").From("users").Where(qb.Eq{"org_id": orgID})
//	if search != "" {
//	    q = q.Where(qb.ILike{"name": "%" + search + "%"})
//	}
//	users, err := row.AllOf[User](ctx, db, q)
//
// Every value passed to a condition becomes a bind parameter, never text
// spliced into the statement, so a builder query cannot be injected through its
// arguments. The exception is Raw, which is documented as trusted input.
package qb

import (
	"fmt"
	"sort"
	"strings"

	"github.com/r-52/row"
)

// Expr is a fragment of SQL together with its arguments.
//
// The interface is sealed: its method is unexported, so every expression comes
// from this package and there is no way to smuggle unparameterised text in
// except through Raw, which says so in its name.
type Expr interface {
	write(w *writer) error
}

// writer accumulates SQL text and bind arguments for one dialect.
type writer struct {
	d    row.Dialect
	b    []byte
	args []any
}

func (w *writer) str(s string) { w.b = append(w.b, s...) }
func (w *writer) byte(c byte)  { w.b = append(w.b, c) }

// bind appends v as a bind parameter and writes its placeholder.
func (w *writer) bind(v any) {
	w.args = append(w.args, v)
	w.b = w.d.AppendPlaceholder(w.b, len(w.args))
}

// ident writes a column or table reference.
//
// A plain identifier, a dotted qualification such as "u.name", and "*" are
// quoted properly. Anything else — a function call, an expression, a cast — is
// written through untouched, because quoting it would break it. That means an
// identifier taken from untrusted input must be validated by the caller; qb
// parameterises values, not identifiers, and no SQL builder can do otherwise.
func (w *writer) ident(name string) {
	if !isQuotableIdent(name) {
		w.str(name)
		return
	}
	for i, part := range strings.Split(name, ".") {
		if i > 0 {
			w.byte('.')
		}
		if part == "*" {
			w.byte('*')
			continue
		}
		w.str(w.d.QuoteIdent(part))
	}
}

// isQuotableIdent reports whether name is a simple, possibly dotted identifier
// (optionally ending in "*") that can safely be quoted.
func isQuotableIdent(name string) bool {
	if name == "" {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if part == "*" {
			continue
		}
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			ok := c == '_' ||
				(c >= 'a' && c <= 'z') ||
				(c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9' && i > 0)
			if !ok {
				return false
			}
		}
	}
	return true
}

// sortedKeys returns a map's keys in a stable order.
//
// Map iteration order is random, and a query whose text changes run to run
// would defeat every statement cache between here and the server.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cmp is the shared implementation of the comparison maps.
type cmp struct {
	op   string
	m    map[string]any
	null string // the form to use when the value is nil, e.g. "IS NULL"
}

func (c cmp) write(w *writer) error {
	if len(c.m) == 0 {
		// An empty condition must not silently disappear and widen the query.
		return fmt.Errorf("qb: empty %s condition", strings.TrimSpace(c.op))
	}
	keys := sortedKeys(c.m)
	for i, k := range keys {
		if i > 0 {
			w.str(" AND ")
		}
		v := c.m[k]

		if v == nil {
			if c.null == "" {
				return fmt.Errorf("qb: %s cannot compare %q against NULL", strings.TrimSpace(c.op), k)
			}
			w.ident(k)
			w.byte(' ')
			w.str(c.null)
			continue
		}

		// A slice on an equality comparison means membership.
		if elems, ok := expand(v); ok {
			if err := writeIn(w, k, elems, c.op == " = "); err != nil {
				return err
			}
			continue
		}

		w.ident(k)
		w.str(c.op)
		w.bind(v)
	}
	return nil
}

// Eq matches columns against values: {"a": 1, "b": "x"} becomes
// a = $1 AND b = $2.
//
// A nil value becomes IS NULL, and a slice value becomes IN (...), because
// those are what the caller meant. An empty slice becomes a condition that
// matches nothing.
type Eq map[string]any

func (e Eq) write(w *writer) error { return cmp{op: " = ", m: e, null: "IS NULL"}.write(w) }

// NotEq is the negation of Eq. A nil value becomes IS NOT NULL and a slice
// becomes NOT IN (...).
type NotEq map[string]any

func (e NotEq) write(w *writer) error { return cmp{op: " <> ", m: e, null: "IS NOT NULL"}.write(w) }

// Lt renders col < value.
type Lt map[string]any

func (e Lt) write(w *writer) error { return cmp{op: " < ", m: e}.write(w) }

// Lte renders col <= value.
type Lte map[string]any

func (e Lte) write(w *writer) error { return cmp{op: " <= ", m: e}.write(w) }

// Gt renders col > value.
type Gt map[string]any

func (e Gt) write(w *writer) error { return cmp{op: " > ", m: e}.write(w) }

// Gte renders col >= value.
type Gte map[string]any

func (e Gte) write(w *writer) error { return cmp{op: " >= ", m: e}.write(w) }

// Like renders col LIKE pattern. The pattern is a bind parameter, so wildcards
// belong in the value: qb.Like{"name": "%ada%"}.
type Like map[string]any

func (e Like) write(w *writer) error { return cmp{op: " LIKE ", m: e}.write(w) }

// ILike renders a case-insensitive match.
//
// Postgres has ILIKE; SQLite's LIKE is already case-insensitive for ASCII, so
// this renders as LIKE there.
type ILike map[string]any

func (e ILike) write(w *writer) error {
	op := " ILIKE "
	if w.d.Name() != "postgres" {
		op = " LIKE "
	}
	return cmp{op: op, m: e}.write(w)
}

// writeIn renders a membership test. An empty set renders as a contradiction
// (or a tautology when negated) rather than invalid SQL.
func writeIn(w *writer, col string, elems []any, positive bool) error {
	if len(elems) == 0 {
		if positive {
			w.str("1 = 0")
		} else {
			w.str("1 = 1")
		}
		return nil
	}
	w.ident(col)
	if positive {
		w.str(" IN (")
	} else {
		w.str(" NOT IN (")
	}
	for i, e := range elems {
		if i > 0 {
			w.byte(',')
		}
		w.bind(e)
	}
	w.byte(')')
	return nil
}

type inExpr struct {
	col      string
	vals     []any
	positive bool
}

// In renders col IN (...). An empty list matches nothing.
func In(column string, values ...any) Expr {
	return inExpr{col: column, vals: flatten(values), positive: true}
}

// NotIn renders col NOT IN (...). An empty list matches everything.
func NotIn(column string, values ...any) Expr {
	return inExpr{col: column, vals: flatten(values), positive: false}
}

func (e inExpr) write(w *writer) error { return writeIn(w, e.col, e.vals, e.positive) }

// flatten lets In take either a variadic list or a single slice.
func flatten(values []any) []any {
	if len(values) == 1 {
		if elems, ok := expand(values[0]); ok {
			return elems
		}
	}
	return values
}

type nullExpr struct {
	col string
	not bool
}

// IsNull renders col IS NULL.
func IsNull(column string) Expr { return nullExpr{col: column} }

// NotNull renders col IS NOT NULL.
func NotNull(column string) Expr { return nullExpr{col: column, not: true} }

func (e nullExpr) write(w *writer) error {
	w.ident(e.col)
	if e.not {
		w.str(" IS NOT NULL")
	} else {
		w.str(" IS NULL")
	}
	return nil
}

type betweenExpr struct {
	col      string
	lo, hi   any
	negative bool
}

// Between renders col BETWEEN lo AND hi, with both bounds parameterised.
func Between(column string, lo, hi any) Expr {
	return betweenExpr{col: column, lo: lo, hi: hi}
}

// NotBetween renders col NOT BETWEEN lo AND hi.
func NotBetween(column string, lo, hi any) Expr {
	return betweenExpr{col: column, lo: lo, hi: hi, negative: true}
}

func (e betweenExpr) write(w *writer) error {
	w.ident(e.col)
	if e.negative {
		w.str(" NOT BETWEEN ")
	} else {
		w.str(" BETWEEN ")
	}
	w.bind(e.lo)
	w.str(" AND ")
	w.bind(e.hi)
	return nil
}

type group struct {
	sep   string
	parts []Expr
}

// And joins expressions with AND. Where already does this, so And is for
// nesting inside an Or.
func And(exprs ...Expr) Expr { return group{sep: " AND ", parts: exprs} }

// Or joins expressions with OR.
func Or(exprs ...Expr) Expr { return group{sep: " OR ", parts: exprs} }

func (g group) write(w *writer) error {
	if len(g.parts) == 0 {
		return fmt.Errorf("qb: empty %s group", strings.TrimSpace(g.sep))
	}
	if len(g.parts) == 1 {
		return g.parts[0].write(w)
	}
	w.byte('(')
	for i, p := range g.parts {
		if i > 0 {
			w.str(g.sep)
		}
		if p == nil {
			return fmt.Errorf("qb: nil expression in %s group", strings.TrimSpace(g.sep))
		}
		if err := p.write(w); err != nil {
			return err
		}
	}
	w.byte(')')
	return nil
}

type notExpr struct{ inner Expr }

// Not negates an expression.
func Not(e Expr) Expr { return notExpr{inner: e} }

func (n notExpr) write(w *writer) error {
	if n.inner == nil {
		return fmt.Errorf("qb: Not(nil)")
	}
	w.str("NOT (")
	if err := n.inner.write(w); err != nil {
		return err
	}
	w.byte(')')
	return nil
}

type rawExpr struct {
	sql  string
	args []any
}

// Raw inserts SQL text verbatim, with ? standing for each argument.
//
// It is the escape hatch for anything qb cannot express — a window function, a
// vendor operator, a hand-tuned subquery. The text is trusted and never
// escaped, so it must never be built from user input; the arguments, however,
// are bound normally and are safe.
//
//	qb.Raw("data @> ?::jsonb", filter)
func Raw(sql string, args ...any) Expr { return rawExpr{sql: sql, args: args} }

func (r rawExpr) write(w *writer) error {
	n := strings.Count(r.sql, "?")
	if n != len(r.args) {
		return fmt.Errorf("qb: Raw(%q) has %d placeholders but %d arguments", r.sql, n, len(r.args))
	}
	rest := r.sql
	for _, a := range r.args {
		before, after, _ := strings.Cut(rest, "?")
		w.str(before)
		w.bind(a)
		rest = after
	}
	w.str(rest)
	return nil
}

// expand reports whether v is a slice that should become a value list, and if
// so returns its elements. It mirrors row's rule: []byte and strings are
// scalars, and anything that marshals itself is left alone.
func expand(v any) ([]any, bool) { return row.ExpandSlice(v) }
