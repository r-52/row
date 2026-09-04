package row

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"sync"
)

// scanKind is how a destination type receives a row.
type scanKind uint8

const (
	scanStruct scanKind = iota // one field per column
	scanLeaf                   // a single-column row into one value
	scanMap                    // map[string]any keyed by column name
)

// scanPlan is the mapping from a specific result-column set to a specific
// destination type. It is computed once and cached, because a given query
// returns the same columns every time it runs.
type scanPlan struct {
	kind scanKind
	si   *structInfo

	// fieldFor[i] is the index into si.fields for result column i, or -1 when
	// the column has no destination.
	fieldFor []int

	// groupNullable[i] reports whether result column i belongs to a nilable
	// nested struct, and which group.
	colGroup []int

	// elem is the struct type behind T, with any pointer stripped.
	elem reflect.Type

	// ptr reports whether T is itself a pointer type.
	ptr bool

	err error
}

type scanKey struct {
	typ    reflect.Type
	cols   string
	mapper uintptr
	strict bool
}

var scanCache sync.Map // scanKey -> *scanPlan

func planFor[T any](cols []string, c *config) (*scanPlan, error) {
	var zero T
	t := reflect.TypeOf(&zero).Elem()

	key := scanKey{
		typ:    t,
		cols:   strings.Join(cols, "\x00"),
		mapper: reflect.ValueOf(c.nameMapper).Pointer(),
		strict: c.strict,
	}
	if v, ok := scanCache.Load(key); ok {
		p := v.(*scanPlan)
		return p, p.err
	}
	p := buildScanPlan(t, cols, c)
	actual, _ := scanCache.LoadOrStore(key, p)
	p = actual.(*scanPlan)
	return p, p.err
}

func buildScanPlan(t reflect.Type, cols []string, c *config) *scanPlan {
	p := &scanPlan{elem: t}

	// map[string]any takes the whole row as-is.
	if t.Kind() == reflect.Map && t.Key().Kind() == reflect.String &&
		t.Elem().Kind() == reflect.Interface && t.Elem().NumMethod() == 0 {
		p.kind = scanMap
		return p
	}

	base := t
	if base.Kind() == reflect.Pointer {
		p.ptr = true
		base = base.Elem()
	}

	if isLeaf(base) || base.Kind() != reflect.Struct {
		p.kind = scanLeaf
		p.elem = base
		if len(cols) != 1 {
			p.err = fmt.Errorf("cannot scan %d columns (%s) into a single %s; "+
				"select one column or scan into a struct",
				len(cols), strings.Join(cols, ", "), t)
		}
		return p
	}

	p.kind = scanStruct
	p.elem = base

	si, err := describeStruct(base, c.nameMapper)
	if err != nil {
		p.err = err
		return p
	}
	p.si = si

	p.fieldFor = make([]int, len(cols))
	p.colGroup = make([]int, len(cols))
	var missing []string
	for i, col := range cols {
		fi, ok := si.byName[col]
		if !ok {
			p.fieldFor[i] = -1
			p.colGroup[i] = -1
			missing = append(missing, col)
			continue
		}
		p.fieldFor[i] = fi
		p.colGroup[i] = si.fields[fi].group
	}

	if len(missing) > 0 && c.strict {
		p.err = fmt.Errorf(
			"no field in %s for result column%s %s\n  %s has columns: %s\n"+
				"  fix the query, add a db tag, or pass row.Lax() to ignore extra columns",
			base, plural(len(missing)), quoteList(missing),
			base, strings.Join(si.sortedColumns(), ", "))
	}
	return p
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// rowReader turns successive driver rows into values of type T.
type rowReader[T any] struct {
	plan    *scanPlan
	cols    []string
	holders []any // *any, one per column, reused across rows
	raw     []any
	nulls   []bool // per group: true until a non-NULL column is seen
}

func newRowReader[T any](cols []string, c *config) (*rowReader[T], error) {
	p, err := planFor[T](cols, c)
	if err != nil {
		return nil, err
	}
	r := &rowReader[T]{
		plan:    p,
		cols:    cols,
		raw:     make([]any, len(cols)),
		holders: make([]any, len(cols)),
	}
	for i := range r.raw {
		r.holders[i] = &r.raw[i]
	}
	if p.si != nil {
		r.nulls = make([]bool, len(p.si.groups))
	}
	return r, nil
}

// read scans the current row and materialises a T.
func (r *rowReader[T]) read(rows *sql.Rows) (T, error) {
	var out T

	if err := rows.Scan(r.holders...); err != nil {
		return out, err
	}

	switch r.plan.kind {
	case scanMap:
		m := make(map[string]any, len(r.cols))
		for i, col := range r.cols {
			m[col] = r.raw[i]
		}
		// The map kind is only chosen when T is map[string]any, so this
		// assertion cannot fail.
		out, _ = any(m).(T)
		return out, nil

	case scanLeaf:
		dst := reflect.New(reflect.TypeOf(&out).Elem()).Elem()
		if err := assign(dst, r.raw[0]); err != nil {
			return out, fmt.Errorf("column %q: %w", r.cols[0], err)
		}
		return dst.Interface().(T), nil
	}

	// Struct. First decide which nilable groups are entirely NULL, so their
	// pointers can be left nil instead of pointing at a zeroed struct.
	for i := range r.nulls {
		r.nulls[i] = true
	}
	for i := range r.cols {
		if r.plan.fieldFor[i] < 0 || r.raw[i] == nil {
			continue
		}
		for g := r.plan.colGroup[i]; g >= 0; g = r.plan.si.groups[g].parent {
			r.nulls[g] = false
		}
	}

	// elem is the struct type either way; ptr only decides whether the
	// pointer or the value is handed back at the end.
	rootVal := reflect.New(r.plan.elem)
	root := rootVal.Elem()

	for i := range r.cols {
		fi := r.plan.fieldFor[i]
		if fi < 0 {
			continue // unmapped column; strict mode already rejected it
		}
		if r.groupIsNull(r.plan.colGroup[i]) {
			continue // the whole nested struct is absent; leave the pointer nil
		}
		f := r.plan.si.fields[fi]
		dst := valueAt(root, f.index)
		if err := assign(dst, r.raw[i]); err != nil {
			return out, fmt.Errorf("column %q -> %s.%s: %w",
				r.cols[i], r.plan.elem.Name(), f.path, err)
		}
	}

	if r.plan.ptr {
		out, _ = rootVal.Interface().(T)
	} else {
		out, _ = root.Interface().(T)
	}
	return out, nil
}

// groupIsNull reports whether g, or any group enclosing it, was all NULL.
func (r *rowReader[T]) groupIsNull(g int) bool {
	for ; g >= 0; g = r.plan.si.groups[g].parent {
		if r.nulls[g] {
			return true
		}
	}
	return false
}

// collect reads every row of an already-bound query.
func collect[T any](ctx context.Context, s Session, op, q string, a []any) ([]T, error) {
	rows, err := runQuery(ctx, s, op, q, a)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rd, err := readerFor[T](s, rows)
	if err != nil {
		return nil, wrap(op, q, s.Dialect(), err)
	}

	var out []T
	for rows.Next() {
		v, err := rd.read(rows)
		if err != nil {
			return nil, wrap(op, q, s.Dialect(), err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(op, q, s.Dialect(), err)
	}
	return out, nil
}

// single reads the first row of an already-bound query. When exactly is true it
// additionally requires that no second row exists.
func single[T any](ctx context.Context, s Session, op, q string, a []any, exactly bool) (T, error) {
	var zero T
	rows, err := runQuery(ctx, s, op, q, a)
	if err != nil {
		return zero, err
	}
	defer rows.Close()

	rd, err := readerFor[T](s, rows)
	if err != nil {
		return zero, wrap(op, q, s.Dialect(), err)
	}

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return zero, wrap(op, q, s.Dialect(), err)
		}
		return zero, wrap(op, q, s.Dialect(), ErrNoRows)
	}
	v, err := rd.read(rows)
	if err != nil {
		return zero, wrap(op, q, s.Dialect(), err)
	}
	if exactly && rows.Next() {
		return zero, wrap(op, q, s.Dialect(), errTooManyRows)
	}
	if err := rows.Err(); err != nil {
		return zero, wrap(op, q, s.Dialect(), err)
	}
	return v, nil
}

// stream feeds the rows of an already-bound query to yield.
func stream[T any](ctx context.Context, s Session, op, q string, a []any, yield func(T, error) bool) {
	var zero T
	rows, err := runQuery(ctx, s, op, q, a)
	if err != nil {
		yield(zero, err)
		return
	}
	defer rows.Close()

	rd, err := readerFor[T](s, rows)
	if err != nil {
		yield(zero, wrap(op, q, s.Dialect(), err))
		return
	}
	for rows.Next() {
		v, err := rd.read(rows)
		if err != nil {
			yield(zero, wrap(op, q, s.Dialect(), err))
			return
		}
		if !yield(v, nil) {
			return // the caller broke out; the deferred Close still runs
		}
	}
	if err := rows.Err(); err != nil {
		yield(zero, wrap(op, q, s.Dialect(), err))
	}
}

func readerFor[T any](s Session, rows *sql.Rows) (*rowReader[T], error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	return newRowReader[T](cols, s.conf())
}

var errTooManyRows = errorString("expected exactly one row, got more; use row.All or add LIMIT 1")

// All runs a query and returns every row as a T.
//
// T may be a struct, a pointer to a struct, a single scannable value such as
// int or time.Time, or map[string]any. An empty result yields a nil slice and
// no error.
//
// Arguments are either a single row.Args map, a single struct, or a list of
// positional values, depending on how the statement spells its placeholders.
func All[T any](ctx context.Context, s Session, query string, args ...any) ([]T, error) {
	const op = "row.All"
	q, a, err := bindFor(op, s, query, args)
	if err != nil {
		return nil, err
	}
	return collect[T](ctx, s, op, q, a)
}

// One runs a query that must return exactly one row.
//
// It returns ErrNoRows when the query matched nothing, and an error when it
// matched more than one row — an over-broad WHERE clause is a bug, not
// something to silently take the first of. Use First when extra rows are
// expected and unwanted.
func One[T any](ctx context.Context, s Session, query string, args ...any) (T, error) {
	const op = "row.One"
	var zero T
	q, a, err := bindFor(op, s, query, args)
	if err != nil {
		return zero, err
	}
	return single[T](ctx, s, op, q, a, true)
}

// First runs a query and returns its first row, ignoring any others. It still
// returns ErrNoRows for an empty result.
func First[T any](ctx context.Context, s Session, query string, args ...any) (T, error) {
	const op = "row.First"
	var zero T
	q, a, err := bindFor(op, s, query, args)
	if err != nil {
		return zero, err
	}
	return single[T](ctx, s, op, q, a, false)
}

// Iter streams rows instead of collecting them, for result sets too large to
// hold in memory.
//
//	for u, err := range row.Iter[User](ctx, db, q) {
//	    if err != nil {
//	        return err
//	    }
//	    ...
//	}
//
// The underlying rows are closed when the loop ends, including when it breaks
// early or returns. At most one error is yielded, and it is always the last
// iteration.
func Iter[T any](ctx context.Context, s Session, query string, args ...any) iter.Seq2[T, error] {
	const op = "row.Iter"
	return func(yield func(T, error) bool) {
		var zero T
		q, a, err := bindFor(op, s, query, args)
		if err != nil {
			yield(zero, err)
			return
		}
		stream[T](ctx, s, op, q, a, yield)
	}
}

// bindFor validates the session, then compiles and binds the statement.
//
// It wraps whatever comes back so that every error leaving a row entry point
// carries the operation exactly once, whether it failed while parsing the
// statement, while binding arguments, or at the server.
func bindFor(op string, s Session, query string, args []any) (string, []any, error) {
	if err := mustSession(op, s); err != nil {
		return "", nil, err
	}
	q, a, err := prepare(s, query, args)
	if err != nil {
		return "", nil, wrap(op, query, s.Dialect(), err)
	}
	return q, a, nil
}
