package row

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// This file generates INSERT, UPDATE and DELETE statements from struct values.
//
// It stops well short of an ORM: there are no relations, no dirty tracking and
// no identity map. It exists because writing out a column list, a matching
// placeholder list and a RETURNING clause by hand is pure transcription, and
// transcription is where typos live.

// writeConfig holds the options for one write.
type writeConfig struct {
	returning []string
	only      []string
	omit      []string
}

// WriteOption configures Insert, InsertMany, Update and Delete.
type WriteOption func(*writeConfig)

// Returning adds a RETURNING clause and scans the result back into the value
// being written. It is how a database-generated id or timestamp gets back into
// the struct:
//
//	u := User{Name: "ada"}
//	err := row.Insert(ctx, db, "users", &u, row.Returning("id", "created_at"))
//
// Both Postgres and SQLite support RETURNING.
func Returning(columns ...string) WriteOption {
	return func(c *writeConfig) { c.returning = append(c.returning, columns...) }
}

// Only restricts the write to the named columns. Everything else is left alone,
// which is how a partial update is expressed.
func Only(columns ...string) WriteOption {
	return func(c *writeConfig) { c.only = append(c.only, columns...) }
}

// Omit excludes the named columns from the write.
func Omit(columns ...string) WriteOption {
	return func(c *writeConfig) { c.omit = append(c.omit, columns...) }
}

func newWriteConfig(opts []WriteOption) *writeConfig {
	c := &writeConfig{}
	for _, o := range opts {
		o(c)
	}
	return c
}

// selects reports whether column should be written, honouring Only and Omit.
func (c *writeConfig) selects(column string) bool {
	if len(c.only) > 0 {
		for _, o := range c.only {
			if o == column {
				return true
			}
		}
		return false
	}
	for _, o := range c.omit {
		if o == column {
			return false
		}
	}
	return true
}

// quoteTable quotes a possibly schema-qualified table name, so that
// "public.users" becomes "public"."users" rather than one odd identifier.
func quoteTable(d Dialect, table string) string {
	parts := strings.Split(table, ".")
	for i, p := range parts {
		parts[i] = d.QuoteIdent(p)
	}
	return strings.Join(parts, ".")
}

// structOf resolves v to an addressable struct value and its mapping.
func structOf[T any](op string, s Session, v *T) (reflect.Value, *structInfo, error) {
	if v == nil {
		return reflect.Value{}, nil, fmt.Errorf("%s: nil value", op)
	}
	rv := reflect.ValueOf(v).Elem()
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, nil, fmt.Errorf("%s: nil value", op)
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, nil, fmt.Errorf("%s: %s is not a struct", op, rv.Type())
	}
	si, err := describeStruct(rv.Type(), s.conf().nameMapper)
	if err != nil {
		return reflect.Value{}, nil, fmt.Errorf("%s: %w", op, err)
	}
	return rv, si, nil
}

// fieldValue reads the value at f's index path, treating a nil pointer
// anywhere along the way as NULL.
func fieldValue(root reflect.Value, f field) any {
	v := root
	for _, i := range f.index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v.Interface()
}

// writableFields lists the fields eligible for an INSERT or the SET clause of
// an UPDATE: everything mapped, minus readonly columns, minus whatever Only and
// Omit exclude. skipPK drops primary keys, which belong in the WHERE clause.
func writableFields(si *structInfo, c *writeConfig, skipPK bool) []field {
	out := make([]field, 0, len(si.fields))
	for _, f := range si.fields {
		if f.readonly || (skipPK && f.pk) {
			continue
		}
		if !c.selects(f.column) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// appendReturning adds a RETURNING clause, refusing on engines without one
// rather than emitting SQL that will fail at the server.
func appendReturning(b *strings.Builder, d Dialect, op string, cols []string) error {
	if len(cols) == 0 {
		return nil
	}
	if !d.Features().Returning {
		return fmt.Errorf("%s: %s does not support RETURNING", op, d.Name())
	}
	b.WriteString(" RETURNING ")
	for i, c := range cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.QuoteIdent(c))
	}
	return nil
}

// scanBack reads a RETURNING result into dst, matching result columns to
// struct fields by name.
func scanBack(ctx context.Context, s Session, op, query string, args []any, dsts []reflect.Value) error {
	rows, err := runQuery(ctx, s, op, query, args)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return wrap(op, query, s.Dialect(), err)
	}

	raw := make([]any, len(cols))
	holders := make([]any, len(cols))
	for i := range raw {
		holders[i] = &raw[i]
	}

	n := 0
	for rows.Next() {
		if n >= len(dsts) {
			return wrap(op, query, s.Dialect(),
				fmt.Errorf("RETURNING produced more rows than values written"))
		}
		if err := rows.Scan(holders...); err != nil {
			return wrap(op, query, s.Dialect(), err)
		}
		dst := dsts[n]
		si, err := describeStruct(dst.Type(), s.conf().nameMapper)
		if err != nil {
			return wrap(op, query, s.Dialect(), err)
		}
		for i, col := range cols {
			fi, ok := si.byName[col]
			if !ok {
				if s.conf().strict {
					return wrap(op, query, s.Dialect(), fmt.Errorf(
						"no field in %s for returned column %q; %s has columns: %s",
						dst.Type(), col, dst.Type(), strings.Join(si.sortedColumns(), ", ")))
				}
				continue
			}
			f := si.fields[fi]
			if err := assign(valueAt(dst, f.index), raw[i]); err != nil {
				return wrap(op, query, s.Dialect(),
					fmt.Errorf("returned column %q -> %s: %w", col, f.path, err))
			}
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return wrap(op, query, s.Dialect(), err)
	}
	if n < len(dsts) {
		return wrap(op, query, s.Dialect(),
			fmt.Errorf("RETURNING produced %d rows for %d written values", n, len(dsts)))
	}
	return nil
}

// Insert writes one struct as a row.
//
// Every mapped column is written except those tagged readonly, which is how a
// database-generated column is declared:
//
//	type User struct {
//	    ID        int64     `db:"id,pk,readonly"`
//	    Name      string
//	    CreatedAt time.Time `db:"created_at,readonly"`
//	}
//	u := User{Name: "ada"}
//	err := row.Insert(ctx, db, "users", &u, row.Returning("id", "created_at"))
func Insert[T any](ctx context.Context, s Session, table string, v *T, opts ...WriteOption) error {
	const op = "row.Insert"
	if err := mustSession(op, s); err != nil {
		return err
	}
	rv, si, err := structOf(op, s, v)
	if err != nil {
		return err
	}
	c := newWriteConfig(opts)
	d := s.Dialect()

	fields := writableFields(si, c, false)
	if len(fields) == 0 {
		return fmt.Errorf("%s: %s has no writable columns", op, rv.Type())
	}

	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(quoteTable(d, table))
	b.WriteString(" (")
	args := make([]any, 0, len(fields))
	for i, f := range fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.QuoteIdent(f.column))
		args = append(args, fieldValue(rv, f))
	}
	b.WriteString(") VALUES (")
	ph := make([]byte, 0, len(fields)*4)
	for i := range fields {
		if i > 0 {
			ph = append(ph, ',', ' ')
		}
		ph = d.AppendPlaceholder(ph, i+1)
	}
	b.Write(ph)
	b.WriteString(")")

	if err := appendReturning(&b, d, op, c.returning); err != nil {
		return err
	}
	query := b.String()

	if len(c.returning) == 0 {
		_, err := runExec(ctx, s, op, query, args)
		return err
	}
	return scanBack(ctx, s, op, query, args, []reflect.Value{rv})
}

// InsertMany writes a slice of structs in as few statements as possible.
//
// Rows are batched into multi-row VALUES clauses, split so that no single
// statement exceeds the engine's parameter limit. An empty slice is a no-op.
//
// The batching is not atomic on its own: wrap the call in InTx when all rows
// must land together.
func InsertMany[T any](ctx context.Context, s Session, table string, vs []T, opts ...WriteOption) error {
	const op = "row.InsertMany"
	if err := mustSession(op, s); err != nil {
		return err
	}
	if len(vs) == 0 {
		return nil
	}
	c := newWriteConfig(opts)
	d := s.Dialect()

	// Every element shares a type, so describe it once from the first.
	_, si, err := structOf(op, s, &vs[0])
	if err != nil {
		return err
	}

	fields := writableFields(si, c, false)
	if len(fields) == 0 {
		return fmt.Errorf("%s: %s has no writable columns", op, si.typ)
	}

	// Rows per statement, bounded by the engine's placeholder limit.
	perRow := len(fields)
	batch := len(vs)
	if max := d.Features().MaxPlaceholders; max > 0 && perRow > 0 {
		if n := max / perRow; n < batch {
			batch = n
		}
	}
	if batch < 1 {
		return fmt.Errorf("%s: %s has %d columns, more than %s allows in one statement",
			op, si.typ, perRow, d.Name())
	}

	colList := make([]string, len(fields))
	for i, f := range fields {
		colList[i] = d.QuoteIdent(f.column)
	}
	prefix := "INSERT INTO " + quoteTable(d, table) + " (" + strings.Join(colList, ", ") + ") VALUES "

	for start := 0; start < len(vs); start += batch {
		end := min(start+batch, len(vs))

		var b strings.Builder
		b.Grow(len(prefix) + (end-start)*perRow*5)
		b.WriteString(prefix)

		args := make([]any, 0, (end-start)*perRow)
		dsts := make([]reflect.Value, 0, end-start)

		for i := start; i < end; i++ {
			rv, _, err := structOf(op, s, &vs[i])
			if err != nil {
				return err
			}
			dsts = append(dsts, rv)
			if i > start {
				b.WriteString(", ")
			}
			b.WriteByte('(')
			ph := make([]byte, 0, perRow*4)
			for j, f := range fields {
				if j > 0 {
					ph = append(ph, ',', ' ')
				}
				args = append(args, fieldValue(rv, f))
				ph = d.AppendPlaceholder(ph, len(args))
			}
			b.Write(ph)
			b.WriteByte(')')
		}

		if err := appendReturning(&b, d, op, c.returning); err != nil {
			return err
		}
		query := b.String()

		if len(c.returning) == 0 {
			if _, err := runExec(ctx, s, op, query, args); err != nil {
				return err
			}
			continue
		}
		if err := scanBack(ctx, s, op, query, args, dsts); err != nil {
			return err
		}
	}
	return nil
}

// Update writes a struct back to its row, matching on the fields tagged pk.
//
// Primary keys and readonly columns are never in the SET clause. Use Only for a
// partial update:
//
//	err := row.Update(ctx, db, "users", &u, row.Only("name", "email"))
func Update[T any](ctx context.Context, s Session, table string, v *T, opts ...WriteOption) error {
	const op = "row.Update"
	if err := mustSession(op, s); err != nil {
		return err
	}
	rv, si, err := structOf(op, s, v)
	if err != nil {
		return err
	}
	if len(si.pks) == 0 {
		return fmt.Errorf("%s: %s has no primary key; tag one field `db:\"...,pk\"`", op, rv.Type())
	}
	c := newWriteConfig(opts)
	d := s.Dialect()

	fields := writableFields(si, c, true)
	if len(fields) == 0 {
		return fmt.Errorf("%s: %s has no columns to update", op, rv.Type())
	}

	var b strings.Builder
	b.WriteString("UPDATE ")
	b.WriteString(quoteTable(d, table))
	b.WriteString(" SET ")
	args := make([]any, 0, len(fields)+len(si.pks))
	for i, f := range fields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(d.QuoteIdent(f.column))
		b.WriteString(" = ")
		args = append(args, fieldValue(rv, f))
		buf := d.AppendPlaceholder(nil, len(args))
		b.Write(buf)
	}

	b.WriteString(" WHERE ")
	for i, pki := range si.pks {
		if i > 0 {
			b.WriteString(" AND ")
		}
		f := si.fields[pki]
		b.WriteString(d.QuoteIdent(f.column))
		b.WriteString(" = ")
		args = append(args, fieldValue(rv, f))
		b.Write(d.AppendPlaceholder(nil, len(args)))
	}

	if err := appendReturning(&b, d, op, c.returning); err != nil {
		return err
	}
	query := b.String()

	if len(c.returning) == 0 {
		_, err := runExec(ctx, s, op, query, args)
		return err
	}
	return scanBack(ctx, s, op, query, args, []reflect.Value{rv})
}

// Delete removes the row matching the struct's primary key.
func Delete[T any](ctx context.Context, s Session, table string, v *T, opts ...WriteOption) error {
	const op = "row.Delete"
	if err := mustSession(op, s); err != nil {
		return err
	}
	rv, si, err := structOf(op, s, v)
	if err != nil {
		return err
	}
	if len(si.pks) == 0 {
		return fmt.Errorf("%s: %s has no primary key; tag one field `db:\"...,pk\"`", op, rv.Type())
	}
	c := newWriteConfig(opts)
	d := s.Dialect()

	var b strings.Builder
	b.WriteString("DELETE FROM ")
	b.WriteString(quoteTable(d, table))
	b.WriteString(" WHERE ")
	args := make([]any, 0, len(si.pks))
	for i, pki := range si.pks {
		if i > 0 {
			b.WriteString(" AND ")
		}
		f := si.fields[pki]
		b.WriteString(d.QuoteIdent(f.column))
		b.WriteString(" = ")
		args = append(args, fieldValue(rv, f))
		b.Write(d.AppendPlaceholder(nil, len(args)))
	}

	if err := appendReturning(&b, d, op, c.returning); err != nil {
		return err
	}
	query := b.String()

	if len(c.returning) == 0 {
		_, err := runExec(ctx, s, op, query, args)
		return err
	}
	return scanBack(ctx, s, op, query, args, []reflect.Value{rv})
}
