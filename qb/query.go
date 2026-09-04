package qb

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/r-52/row"
)

// Builders are immutable: every method returns a new value rather than
// mutating the receiver. That is what makes a partially-built query safe to
// keep as a template and branch from:
//
//	base := qb.Select("*").From("users").Where(qb.Eq{"active": true})
//	admins := base.Where(qb.Eq{"role": "admin"})   // base is unchanged

// SelectQuery builds a SELECT statement.
type SelectQuery struct {
	distinct bool
	columns  []string
	from     string
	joins    []join
	where    []Expr
	groupBy  []string
	having   []Expr
	orderBy  []string
	limit    int64
	hasLimit bool
	offset   int64
	hasOff   bool
}

type join struct {
	kind  string // "JOIN", "LEFT JOIN", ...
	table string
	on    Expr
}

// Select starts a SELECT with the given result columns. A column may be a
// plain or dotted identifier, "*", or any expression, which is written through
// unquoted.
func Select(columns ...string) SelectQuery {
	return SelectQuery{columns: append([]string(nil), columns...)}
}

// Distinct adds DISTINCT.
func (q SelectQuery) Distinct() SelectQuery {
	q.distinct = true
	return q
}

// Columns replaces the result columns, which is how a count variant of an
// existing query is derived:
//
//	total, err := row.OneOf[int64](ctx, db, base.Columns("count(*)").ClearOrder())
func (q SelectQuery) Columns(columns ...string) SelectQuery {
	q.columns = append([]string(nil), columns...)
	return q
}

// From sets the table. It may be "schema.table", and may carry an alias as
// "users u".
func (q SelectQuery) From(table string) SelectQuery {
	q.from = table
	return q
}

// Join adds an INNER JOIN.
func (q SelectQuery) Join(table string, on Expr) SelectQuery { return q.join("JOIN", table, on) }

// LeftJoin adds a LEFT JOIN.
func (q SelectQuery) LeftJoin(table string, on Expr) SelectQuery {
	return q.join("LEFT JOIN", table, on)
}

// RightJoin adds a RIGHT JOIN.
func (q SelectQuery) RightJoin(table string, on Expr) SelectQuery {
	return q.join("RIGHT JOIN", table, on)
}

// InnerJoin is a synonym for Join.
func (q SelectQuery) InnerJoin(table string, on Expr) SelectQuery {
	return q.join("INNER JOIN", table, on)
}

func (q SelectQuery) join(kind, table string, on Expr) SelectQuery {
	q.joins = append(append([]join(nil), q.joins...), join{kind: kind, table: table, on: on})
	return q
}

// Where adds conditions, which are combined with AND. Calling it repeatedly
// accumulates, so an optional filter is one if statement.
func (q SelectQuery) Where(exprs ...Expr) SelectQuery {
	q.where = append(append([]Expr(nil), q.where...), exprs...)
	return q
}

// GroupBy adds grouping columns.
func (q SelectQuery) GroupBy(columns ...string) SelectQuery {
	q.groupBy = append(append([]string(nil), q.groupBy...), columns...)
	return q
}

// Having adds conditions on the grouped result.
func (q SelectQuery) Having(exprs ...Expr) SelectQuery {
	q.having = append(append([]Expr(nil), q.having...), exprs...)
	return q
}

// OrderBy adds ordering terms, each written through as given so that
// "created_at DESC" and "lower(name)" both work.
func (q SelectQuery) OrderBy(terms ...string) SelectQuery {
	q.orderBy = append(append([]string(nil), q.orderBy...), terms...)
	return q
}

// ClearOrder removes any ordering, which a count query does not want.
func (q SelectQuery) ClearOrder() SelectQuery {
	q.orderBy = nil
	return q
}

// Limit caps the number of rows.
func (q SelectQuery) Limit(n int64) SelectQuery {
	q.limit, q.hasLimit = n, true
	return q
}

// Offset skips rows.
func (q SelectQuery) Offset(n int64) SelectQuery {
	q.offset, q.hasOff = n, true
	return q
}

// BuildSQL renders the query for d, implementing row.Builder.
func (q SelectQuery) BuildSQL(d row.Dialect) (string, []any, error) {
	if d == nil {
		return "", nil, fmt.Errorf("qb: nil dialect")
	}
	w := &writer{d: d}

	w.str("SELECT ")
	if q.distinct {
		w.str("DISTINCT ")
	}
	if len(q.columns) == 0 {
		w.byte('*')
	} else {
		for i, c := range q.columns {
			if i > 0 {
				w.str(", ")
			}
			w.ident(c)
		}
	}

	if q.from != "" {
		w.str(" FROM ")
		w.writeTableRef(q.from)
	}

	for _, j := range q.joins {
		w.byte(' ')
		w.str(j.kind)
		w.byte(' ')
		w.writeTableRef(j.table)
		if j.on != nil {
			w.str(" ON ")
			if err := j.on.write(w); err != nil {
				return "", nil, err
			}
		}
	}

	if err := w.clause(" WHERE ", q.where); err != nil {
		return "", nil, err
	}

	if len(q.groupBy) > 0 {
		w.str(" GROUP BY ")
		for i, c := range q.groupBy {
			if i > 0 {
				w.str(", ")
			}
			w.ident(c)
		}
	}

	if err := w.clause(" HAVING ", q.having); err != nil {
		return "", nil, err
	}

	if len(q.orderBy) > 0 {
		w.str(" ORDER BY ")
		for i, t := range q.orderBy {
			if i > 0 {
				w.str(", ")
			}
			w.writeOrderTerm(t)
		}
	}

	// LIMIT and OFFSET are counts, not data, and both engines accept them as
	// literals; writing them inline keeps a paginated query's text stable
	// across pages for the server's own statement cache.
	if q.hasLimit {
		w.str(" LIMIT ")
		w.str(strconv.FormatInt(q.limit, 10))
	}
	if q.hasOff {
		w.str(" OFFSET ")
		w.str(strconv.FormatInt(q.offset, 10))
	}

	return string(w.b), w.args, nil
}

// clause writes " WHERE "/" HAVING " and its AND-joined expressions, or
// nothing when there are none.
func (w *writer) clause(keyword string, exprs []Expr) error {
	if len(exprs) == 0 {
		return nil
	}
	w.str(keyword)
	for i, e := range exprs {
		if i > 0 {
			w.str(" AND ")
		}
		if e == nil {
			return fmt.Errorf("qb: nil expression in%s", strings.TrimRight(keyword, " "))
		}
		if err := e.write(w); err != nil {
			return err
		}
	}
	return nil
}

// writeTableRef writes a table reference, honouring a trailing alias:
// "users u" and "users AS u" both quote only the table and the alias.
func (w *writer) writeTableRef(ref string) {
	fields := strings.Fields(ref)
	switch len(fields) {
	case 2:
		w.ident(fields[0])
		w.byte(' ')
		w.ident(fields[1])
	case 3:
		if strings.EqualFold(fields[1], "AS") {
			w.ident(fields[0])
			w.str(" AS ")
			w.ident(fields[2])
			return
		}
		w.str(ref)
	default:
		w.ident(ref)
	}
}

// writeOrderTerm quotes the column of "col DESC" while passing the direction
// and anything more complicated through unchanged.
func (w *writer) writeOrderTerm(term string) {
	fields := strings.Fields(term)
	if len(fields) == 2 && isQuotableIdent(fields[0]) {
		switch strings.ToUpper(fields[1]) {
		case "ASC", "DESC":
			w.ident(fields[0])
			w.byte(' ')
			w.str(strings.ToUpper(fields[1]))
			return
		}
	}
	w.ident(term)
}

// InsertQuery builds an INSERT statement.
//
// For inserting a struct, prefer row.Insert; this is for the cases that need
// explicit control, such as an upsert or an INSERT ... SELECT.
type InsertQuery struct {
	table     string
	columns   []string
	rows      [][]any
	conflict  string
	returning []string
}

// InsertInto starts an INSERT.
func InsertInto(table string) InsertQuery { return InsertQuery{table: table} }

// Columns names the target columns.
func (q InsertQuery) Columns(columns ...string) InsertQuery {
	q.columns = append([]string(nil), columns...)
	return q
}

// Values adds one row of values. Call it repeatedly for a multi-row insert.
func (q InsertQuery) Values(values ...any) InsertQuery {
	q.rows = append(append([][]any(nil), q.rows...), append([]any(nil), values...))
	return q
}

// OnConflict appends an ON CONFLICT clause verbatim, for example
// `("email") DO NOTHING` or `("email") DO UPDATE SET name = excluded.name`.
// Both Postgres and SQLite use this syntax.
func (q InsertQuery) OnConflict(clause string) InsertQuery {
	q.conflict = clause
	return q
}

// Returning adds a RETURNING clause.
func (q InsertQuery) Returning(columns ...string) InsertQuery {
	q.returning = append(append([]string(nil), q.returning...), columns...)
	return q
}

// BuildSQL renders the statement for d, implementing row.Builder.
func (q InsertQuery) BuildSQL(d row.Dialect) (string, []any, error) {
	if d == nil {
		return "", nil, fmt.Errorf("qb: nil dialect")
	}
	if q.table == "" {
		return "", nil, fmt.Errorf("qb: InsertInto needs a table")
	}
	if len(q.rows) == 0 {
		return "", nil, fmt.Errorf("qb: insert into %s has no values", q.table)
	}

	w := &writer{d: d}
	w.str("INSERT INTO ")
	w.ident(q.table)

	if len(q.columns) > 0 {
		w.str(" (")
		for i, c := range q.columns {
			if i > 0 {
				w.str(", ")
			}
			w.ident(c)
		}
		w.byte(')')
	}

	w.str(" VALUES ")
	for i, vals := range q.rows {
		if len(q.columns) > 0 && len(vals) != len(q.columns) {
			return "", nil, fmt.Errorf("qb: row %d has %d values but %d columns were named",
				i, len(vals), len(q.columns))
		}
		if i > 0 {
			w.str(", ")
		}
		w.byte('(')
		for j, v := range vals {
			if j > 0 {
				w.str(", ")
			}
			w.bind(v)
		}
		w.byte(')')
	}

	if q.conflict != "" {
		w.str(" ON CONFLICT ")
		w.str(q.conflict)
	}
	if err := w.returning(d, q.returning); err != nil {
		return "", nil, err
	}
	return string(w.b), w.args, nil
}

// UpdateQuery builds an UPDATE statement.
type UpdateQuery struct {
	table     string
	sets      []setPair
	where     []Expr
	returning []string
}

type setPair struct {
	col string
	val any
	raw Expr
}

// Update starts an UPDATE.
func Update(table string) UpdateQuery { return UpdateQuery{table: table} }

// Set assigns a bound value to a column.
func (q UpdateQuery) Set(column string, value any) UpdateQuery {
	q.sets = append(append([]setPair(nil), q.sets...), setPair{col: column, val: value})
	return q
}

// SetMap assigns several columns at once, in a stable column order.
func (q UpdateQuery) SetMap(m map[string]any) UpdateQuery {
	sets := append([]setPair(nil), q.sets...)
	for _, k := range sortedKeys(m) {
		sets = append(sets, setPair{col: k, val: m[k]})
	}
	q.sets = sets
	return q
}

// SetExpr assigns the result of an expression, for updates that read the
// current value:
//
//	qb.Update("counters").SetExpr("value", qb.Raw("value + ?", 1))
func (q UpdateQuery) SetExpr(column string, e Expr) UpdateQuery {
	q.sets = append(append([]setPair(nil), q.sets...), setPair{col: column, raw: e})
	return q
}

// Where adds conditions, combined with AND.
func (q UpdateQuery) Where(exprs ...Expr) UpdateQuery {
	q.where = append(append([]Expr(nil), q.where...), exprs...)
	return q
}

// Returning adds a RETURNING clause.
func (q UpdateQuery) Returning(columns ...string) UpdateQuery {
	q.returning = append(append([]string(nil), q.returning...), columns...)
	return q
}

// BuildSQL renders the statement for d, implementing row.Builder.
func (q UpdateQuery) BuildSQL(d row.Dialect) (string, []any, error) {
	if d == nil {
		return "", nil, fmt.Errorf("qb: nil dialect")
	}
	if q.table == "" {
		return "", nil, fmt.Errorf("qb: Update needs a table")
	}
	if len(q.sets) == 0 {
		return "", nil, fmt.Errorf("qb: update %s sets nothing", q.table)
	}
	// An UPDATE with no WHERE rewrites the whole table. That is occasionally
	// intended and almost always a bug, so it has to be said out loud.
	if len(q.where) == 0 {
		return "", nil, fmt.Errorf("qb: update %s has no WHERE clause; "+
			"add one, or say qb.Raw(\"1 = 1\") to update every row", q.table)
	}

	w := &writer{d: d}
	w.str("UPDATE ")
	w.ident(q.table)
	w.str(" SET ")
	for i, s := range q.sets {
		if i > 0 {
			w.str(", ")
		}
		w.ident(s.col)
		w.str(" = ")
		if s.raw != nil {
			if err := s.raw.write(w); err != nil {
				return "", nil, err
			}
			continue
		}
		w.bind(s.val)
	}
	if err := w.clause(" WHERE ", q.where); err != nil {
		return "", nil, err
	}
	if err := w.returning(d, q.returning); err != nil {
		return "", nil, err
	}
	return string(w.b), w.args, nil
}

// DeleteQuery builds a DELETE statement.
type DeleteQuery struct {
	table     string
	where     []Expr
	returning []string
}

// DeleteFrom starts a DELETE.
func DeleteFrom(table string) DeleteQuery { return DeleteQuery{table: table} }

// Where adds conditions, combined with AND.
func (q DeleteQuery) Where(exprs ...Expr) DeleteQuery {
	q.where = append(append([]Expr(nil), q.where...), exprs...)
	return q
}

// Returning adds a RETURNING clause.
func (q DeleteQuery) Returning(columns ...string) DeleteQuery {
	q.returning = append(append([]string(nil), q.returning...), columns...)
	return q
}

// BuildSQL renders the statement for d, implementing row.Builder.
func (q DeleteQuery) BuildSQL(d row.Dialect) (string, []any, error) {
	if d == nil {
		return "", nil, fmt.Errorf("qb: nil dialect")
	}
	if q.table == "" {
		return "", nil, fmt.Errorf("qb: DeleteFrom needs a table")
	}
	if len(q.where) == 0 {
		return "", nil, fmt.Errorf("qb: delete from %s has no WHERE clause; "+
			"add one, or say qb.Raw(\"1 = 1\") to delete every row", q.table)
	}

	w := &writer{d: d}
	w.str("DELETE FROM ")
	w.ident(q.table)
	if err := w.clause(" WHERE ", q.where); err != nil {
		return "", nil, err
	}
	if err := w.returning(d, q.returning); err != nil {
		return "", nil, err
	}
	return string(w.b), w.args, nil
}

func (w *writer) returning(d row.Dialect, cols []string) error {
	if len(cols) == 0 {
		return nil
	}
	if !d.Features().Returning {
		return fmt.Errorf("qb: %s does not support RETURNING", d.Name())
	}
	w.str(" RETURNING ")
	for i, c := range cols {
		if i > 0 {
			w.str(", ")
		}
		w.ident(c)
	}
	return nil
}
