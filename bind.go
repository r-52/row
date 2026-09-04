package row

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Args carries named parameters.
//
//	row.All[User](ctx, db, `SELECT * FROM users WHERE org = :org`, row.Args{"org": 7})
type Args map[string]any

// paramStyle is the placeholder convention a statement was written in. row
// infers it rather than making the caller declare it, and refuses statements
// that mix conventions.
type paramStyle uint8

const (
	styleNone     paramStyle = iota // no parameters at all
	styleNamed                      // :name
	styleOrdinal                    // ?
	styleNumbered                   // $1, passed through untouched
)

func (s paramStyle) String() string {
	switch s {
	case styleNamed:
		return "named (:name)"
	case styleOrdinal:
		return "ordinal (?)"
	case styleNumbered:
		return "numbered ($1)"
	default:
		return "none"
	}
}

// part is one piece of a compiled statement: either a run of literal SQL or a
// single parameter slot.
type part struct {
	text  string // literal SQL, when name is empty and !isParam
	name  string // parameter name, for the named style
	pos   int    // 0-based argument index, for the ordinal style
	param bool
}

// plan is a statement compiled once and rendered many times. Rendering is what
// happens per query: it walks the parts, emits dialect placeholders and expands
// slice arguments.
type plan struct {
	style  paramStyle
	parts  []part
	names  []string // distinct names, first-appearance order
	nslots int      // number of parameter slots
	size   int      // total literal bytes, for buffer pre-sizing
}

// compile turns a statement into a plan. It reports an error only for genuine
// ambiguity — mixed placeholder conventions — and otherwise leaves invalid SQL
// for the database to diagnose.
func compile(sql string) (*plan, error) {
	toks := lex(sql)

	var hasNamed, hasOrdinal, hasNumbered bool
	for _, t := range toks {
		switch t.kind {
		case tokNamed:
			hasNamed = true
		case tokOrdinal:
			hasOrdinal = true
		case tokNumbered:
			hasNumbered = true
		}
	}

	p := &plan{}
	switch {
	case hasNamed && hasNumbered:
		return nil, fmt.Errorf("statement mixes named (:name) and numbered ($1) parameters; pick one")
	case hasOrdinal && hasNumbered:
		return nil, fmt.Errorf("statement mixes ordinal (?) and numbered ($1) parameters; pick one")
	case hasNamed:
		p.style = styleNamed
	case hasOrdinal:
		p.style = styleOrdinal
	case hasNumbered:
		// The caller wrote dialect-native placeholders. row passes the
		// statement through verbatim and binds arguments positionally.
		p.style = styleNumbered
	default:
		p.style = styleNone
	}

	seen := map[string]bool{}
	var lit strings.Builder

	flushLit := func() {
		if lit.Len() > 0 {
			p.parts = append(p.parts, part{text: lit.String()})
			p.size += lit.Len()
			lit.Reset()
		}
	}

	for _, t := range toks {
		switch t.kind {
		case tokText:
			lit.WriteString(sql[t.lo:t.hi])

		case tokEscapedQ:
			// "??" always stands for one literal question mark.
			lit.WriteByte('?')

		case tokNumbered:
			// Verbatim pass-through, including the original spelling.
			lit.WriteString(sql[t.lo:t.hi])

		case tokNamed:
			flushLit()
			p.parts = append(p.parts, part{name: t.name, param: true})
			if !seen[t.name] {
				seen[t.name] = true
				p.names = append(p.names, t.name)
			}
			p.nslots++

		case tokOrdinal:
			if p.style == styleNamed {
				// In a named statement a lone '?' cannot be a placeholder, so
				// it is Postgres's jsonb existence operator. Emit it as text.
				lit.WriteByte('?')
				break
			}
			flushLit()
			p.parts = append(p.parts, part{pos: p.nslots, param: true})
			p.nslots++
		}
	}
	flushLit()
	return p, nil
}

// namedSource is anything that can resolve a parameter name. Args implements
// it directly; structs are adapted by the mapper.
type namedSource interface {
	lookup(name string) (any, bool)
	// describe lists the names it can supply, for error messages.
	describe() []string
}

type argsSource Args

func (a argsSource) lookup(name string) (any, bool) { v, ok := a[name]; return v, ok }

func (a argsSource) describe() []string {
	names := make([]string, 0, len(a))
	for k := range a {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// render produces the final statement text and positional argument slice.
//
// src supplies values for the named style and must be nil otherwise; pos
// supplies values for the ordinal style and must be empty otherwise.
func (p *plan) render(d Dialect, src namedSource, pos []any) (string, []any, error) {
	switch p.style {
	case styleNone, styleNumbered:
		if src != nil {
			return "", nil, fmt.Errorf("statement has no named parameters but named arguments were supplied")
		}
		var b strings.Builder
		b.Grow(p.size)
		for _, pt := range p.parts {
			b.WriteString(pt.text)
		}
		return b.String(), pos, nil

	case styleNamed:
		if src == nil {
			return "", nil, fmt.Errorf("statement uses named parameters %s but no named arguments were supplied",
				quoteList(p.names))
		}
		if len(pos) > 0 {
			return "", nil, fmt.Errorf("statement uses named parameters but %d positional arguments were supplied", len(pos))
		}

	case styleOrdinal:
		if src != nil {
			return "", nil, fmt.Errorf("statement uses %s parameters but named arguments were supplied", p.style)
		}
		if len(pos) != p.nslots {
			return "", nil, fmt.Errorf("statement has %d placeholders but %d arguments were supplied", p.nslots, len(pos))
		}
	}

	// Pre-size generously: literals plus room for placeholders.
	buf := make([]byte, 0, p.size+p.nslots*4)
	out := make([]any, 0, p.nslots)

	for _, pt := range p.parts {
		if !pt.param {
			buf = append(buf, pt.text...)
			continue
		}

		var val any
		if p.style == styleNamed {
			v, ok := src.lookup(pt.name)
			if !ok {
				return "", nil, fmt.Errorf("no argument for parameter %q; available: %s",
					pt.name, quoteList(src.describe()))
			}
			val = v
		} else {
			val = pos[pt.pos]
		}

		elems, isSlice := sliceElems(val)
		if !isSlice {
			out = append(out, val)
			buf = d.AppendPlaceholder(buf, len(out))
			continue
		}
		if len(elems) == 0 {
			// "x IN (NULL)" is never true, which is the correct meaning of
			// membership in an empty set and keeps the statement valid.
			buf = append(buf, "NULL"...)
			continue
		}
		for i, e := range elems {
			if i > 0 {
				buf = append(buf, ',')
			}
			out = append(out, e)
			buf = d.AppendPlaceholder(buf, len(out))
		}
	}
	return string(buf), out, nil
}

// ExpandSlice reports whether v is a slice that a query should expand into a
// list of bind parameters, and if so returns its elements.
//
// It is exported so that query builders apply exactly the same rule row does:
// []byte and string are scalars, and a type implementing driver.Valuer knows
// how to marshal itself and is passed through whole.
func ExpandSlice(v any) ([]any, bool) { return sliceElems(v) }

// sliceElems reports whether v should be expanded into a placeholder list, and
// if so returns its elements.
//
// A slice is expanded unless it is a []byte (a scalar BLOB), a string, or a
// type that knows how to marshal itself through driver.Valuer — Postgres array
// types being the important case there.
func sliceElems(v any) ([]any, bool) {
	if v == nil {
		return nil, false
	}
	switch v.(type) {
	case []byte, string:
		return nil, false
	case driver.Valuer:
		return nil, false
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		// Named []byte types are still scalars.
		return nil, false
	}

	n := rv.Len()
	out := make([]any, n)
	for i := 0; i < n; i++ {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

func quoteList(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('"')
		b.WriteString(n)
		b.WriteByte('"')
	}
	return b.String()
}
