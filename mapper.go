package row

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// SnakeCase converts a Go field name to the column name row will look for when
// a field carries no db tag.
//
// It handles acronyms the way Go code actually spells them:
//
//	ID        -> id
//	UserID    -> user_id
//	HTTPCode  -> http_code
//	OAuth2Key -> oauth2_key
//	CreatedAt -> created_at
func SnakeCase(name string) string {
	rs := []rune(name)
	var b strings.Builder
	b.Grow(len(rs) + 4)

	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if !unicode.IsUpper(r) {
			b.WriteRune(r)
			continue
		}
		// Insert a separator before an uppercase run only when it starts a new
		// word: either the previous rune was lower/digit, or this is the last
		// letter of an acronym that is followed by a lowercase letter.
		if i > 0 {
			prev := rs[i-1]
			startsWord := !unicode.IsUpper(prev)
			endsAcronym := unicode.IsUpper(prev) &&
				i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if startsWord || endsAcronym {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// field is one scannable column of a struct type.
type field struct {
	// column is the name matched against result columns.
	column string

	// index is the chain of field indexes from the root struct to this field.
	index []int

	// group identifies the nilable nested struct this field belongs to, or -1
	// when the field hangs off the root. See structInfo.groups.
	group int

	pk       bool
	readonly bool

	// path is the dotted Go path, e.g. "Address.City", for error messages.
	path string
}

// group describes a nested struct reached through a pointer. Such a struct is
// left nil when every one of its columns came back NULL, which is what a LEFT
// JOIN that matched nothing should produce.
type group struct {
	index  []int // path to the pointer field itself
	parent int   // enclosing group, or -1
	path   string
}

// structInfo is the cached mapping between a struct type and its columns.
type structInfo struct {
	typ    reflect.Type
	fields []field
	byName map[string]int // column -> index into fields
	groups []group
	pks    []int // indexes into fields
	err    error // set when the type cannot be mapped at all
}

// mapperKey distinguishes cache entries that differ only by naming policy, so
// two DBs with different name mappers do not share a mapping.
type mapperKey struct {
	typ    reflect.Type
	mapper uintptr
}

var structCache sync.Map // mapperKey -> *structInfo

// describeStruct returns the cached mapping for t, building it on first use.
func describeStruct(t reflect.Type, nameMapper func(string) string) (*structInfo, error) {
	key := mapperKey{typ: t, mapper: reflect.ValueOf(nameMapper).Pointer()}
	if v, ok := structCache.Load(key); ok {
		si := v.(*structInfo)
		return si, si.err
	}
	si := buildStruct(t, nameMapper)
	actual, _ := structCache.LoadOrStore(key, si)
	si = actual.(*structInfo)
	return si, si.err
}

// tagInfo is a parsed `db:"..."` tag.
type tagInfo struct {
	name     string
	skip     bool
	pk       bool
	readonly bool
	inline   bool
	prefix   string
	hasPfx   bool
}

func parseTag(raw string) tagInfo {
	var ti tagInfo
	if raw == "-" {
		ti.skip = true
		return ti
	}
	parts := strings.Split(raw, ",")
	ti.name = parts[0]
	for _, opt := range parts[1:] {
		switch {
		case opt == "pk":
			ti.pk = true
		case opt == "readonly":
			ti.readonly = true
		case opt == "inline":
			ti.inline = true
			ti.prefix = ""
			ti.hasPfx = true
		case strings.HasPrefix(opt, "prefix="):
			ti.prefix = strings.TrimPrefix(opt, "prefix=")
			ti.hasPfx = true
		}
	}
	return ti
}

func buildStruct(t reflect.Type, nameMapper func(string) string) *structInfo {
	si := &structInfo{typ: t, byName: map[string]int{}}
	if t.Kind() != reflect.Struct {
		si.err = fmt.Errorf("%s is not a struct", t)
		return si
	}

	// dup tracks the Go path that first claimed each column, so a collision can
	// name both sides.
	dup := map[string]string{}

	var walk func(t reflect.Type, idx []int, prefix, path string, grp int, seen map[reflect.Type]bool)
	walk = func(t reflect.Type, idx []int, prefix, path string, grp int, seen map[reflect.Type]bool) {
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() && !sf.Anonymous {
				continue
			}

			raw, tagged := sf.Tag.Lookup("db")
			ti := parseTag(raw)
			if ti.skip {
				continue
			}

			name := ti.name
			if name == "" {
				name = nameMapper(sf.Name)
			}

			// index must be copied: append would otherwise alias the backing
			// array across sibling fields.
			myIdx := make([]int, len(idx)+1)
			copy(myIdx, idx)
			myIdx[len(idx)] = i

			myPath := sf.Name
			if path != "" {
				myPath = path + "." + sf.Name
			}

			ft := sf.Type
			if isLeaf(ft) {
				if !sf.IsExported() {
					continue
				}
				col := prefix + name
				if prev, clash := dup[col]; clash {
					si.err = fmt.Errorf("%s maps %s and %s to the same column %q; "+
						"disambiguate with a db tag", si.typ, prev, myPath, col)
					return
				}
				dup[col] = myPath
				si.byName[col] = len(si.fields)
				si.fields = append(si.fields, field{
					column: col, index: myIdx, group: grp,
					pk: ti.pk, readonly: ti.readonly, path: myPath,
				})
				if ti.pk {
					si.pks = append(si.pks, len(si.fields)-1)
				}
				continue
			}

			// A nested struct. Recursion guard: a type that contains itself
			// (directly or through a pointer) has no finite column list.
			base := ft
			nilable := false
			if base.Kind() == reflect.Pointer {
				base = base.Elem()
				nilable = true
			}
			if seen[base] {
				continue
			}

			// Naming policy: embedded structs merge into the parent's namespace;
			// named struct fields get a prefix derived from their column name,
			// which is what "SELECT a.city AS address_city" produces. Either can
			// be overridden with `inline` or `prefix=`.
			sub := prefix
			switch {
			case ti.hasPfx:
				sub = prefix + ti.prefix
			case sf.Anonymous && !tagged:
				// merge, no prefix
			default:
				sub = prefix + name + "_"
			}

			childGroup := grp
			if nilable {
				si.groups = append(si.groups, group{index: myIdx, parent: grp, path: myPath})
				childGroup = len(si.groups) - 1
			}

			seen[base] = true
			walk(base, myIdx, sub, myPath, childGroup, seen)
			delete(seen, base)

			if si.err != nil {
				return
			}
		}
	}

	walk(t, nil, "", "", -1, map[reflect.Type]bool{t: true})
	if si.err != nil {
		si.fields, si.byName, si.groups, si.pks = nil, nil, nil, nil
	}
	return si
}

// columns lists the mapped column names in declaration order.
func (si *structInfo) columns() []string {
	out := make([]string, len(si.fields))
	for i, f := range si.fields {
		out[i] = f.column
	}
	return out
}

// sortedColumns lists the mapped column names alphabetically, for error text.
func (si *structInfo) sortedColumns() []string {
	out := si.columns()
	sort.Strings(out)
	return out
}

// valueAt walks index from root, allocating nil pointers along the way, and
// returns the settable destination field.
func valueAt(root reflect.Value, index []int) reflect.Value {
	v := root
	for _, i := range index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v
}

// structSource adapts a struct to namedSource so that named parameters can be
// filled straight from a value:
//
//	row.Exec(ctx, db, `INSERT INTO t (a, b) VALUES (:a, :b)`, myStruct)
//
// It reports false when v is not a struct, leaving the caller to treat the
// argument as positional.
func structSource(v any, nameMapper func(string) string) (namedSource, bool, error) {
	if v == nil {
		return nil, false, nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false, nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct || isLeaf(rv.Type()) {
		return nil, false, nil
	}
	si, err := describeStruct(rv.Type(), nameMapper)
	if err != nil {
		return nil, false, err
	}
	return &structArgs{si: si, v: rv}, true, nil
}

type structArgs struct {
	si *structInfo
	v  reflect.Value
}

func (s *structArgs) lookup(name string) (any, bool) {
	i, ok := s.si.byName[name]
	if !ok {
		return nil, false
	}
	f := s.si.fields[i]
	v := s.v
	for _, idx := range f.index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil, true // a nil branch contributes NULL
			}
			v = v.Elem()
		}
		v = v.Field(idx)
	}
	return v.Interface(), true
}

func (s *structArgs) describe() []string { return s.si.sortedColumns() }
