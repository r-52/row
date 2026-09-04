package row

import (
	"database/sql/driver"
	"strings"
	"testing"
)

func TestRenderNamed(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		args     Args
		wantSQL  string
		wantArgs string
	}{
		{
			name:     "single param",
			sql:      `SELECT * FROM t WHERE id = :id`,
			args:     Args{"id": 7},
			wantSQL:  `SELECT * FROM t WHERE id = $1`,
			wantArgs: `[7]`,
		},
		{
			name:     "repeated param binds twice",
			sql:      `WHERE a = :x OR b = :x`,
			args:     Args{"x": 3},
			wantSQL:  `WHERE a = $1 OR b = $2`,
			wantArgs: `[3 3]`,
		},
		{
			name:     "order follows the statement not the map",
			sql:      `WHERE b = :b AND a = :a`,
			args:     Args{"a": 1, "b": 2},
			wantSQL:  `WHERE b = $1 AND a = $2`,
			wantArgs: `[2 1]`,
		},
		{
			name:     "cast survives binding",
			sql:      `WHERE id = :id::bigint`,
			args:     Args{"id": "9"},
			wantSQL:  `WHERE id = $1::bigint`,
			wantArgs: `[9]`,
		},
		{
			name:     "slice expands",
			sql:      `WHERE id IN (:ids)`,
			args:     Args{"ids": []int{1, 2, 3}},
			wantSQL:  `WHERE id IN ($1,$2,$3)`,
			wantArgs: `[1 2 3]`,
		},
		{
			name:     "empty slice becomes NULL",
			sql:      `WHERE id IN (:ids)`,
			args:     Args{"ids": []int{}},
			wantSQL:  `WHERE id IN (NULL)`,
			wantArgs: `[]`,
		},
		{
			name:     "slice mixed with scalars keeps numbering",
			sql:      `WHERE org = :org AND id IN (:ids) AND x = :x`,
			args:     Args{"org": 9, "ids": []string{"a", "b"}, "x": true},
			wantSQL:  `WHERE org = $1 AND id IN ($2,$3) AND x = $4`,
			wantArgs: `[9 a b true]`,
		},
		{
			name:     "byte slice is a scalar",
			sql:      `WHERE blob = :b`,
			args:     Args{"b": []byte{1, 2}},
			wantSQL:  `WHERE blob = $1`,
			wantArgs: `[[1 2]]`,
		},
		{
			name:     "string is a scalar",
			sql:      `WHERE s = :s`,
			args:     Args{"s": "abc"},
			wantSQL:  `WHERE s = $1`,
			wantArgs: `[abc]`,
		},
		{
			name:     "nil stays a single NULL argument",
			sql:      `WHERE s = :s`,
			args:     Args{"s": nil},
			wantSQL:  `WHERE s = $1`,
			wantArgs: `[<nil>]`,
		},
		{
			name:     "params inside strings and comments are untouched",
			sql:      "SELECT ':a' /* :b */ -- :c\nWHERE x = :d",
			args:     Args{"d": 1},
			wantSQL:  "SELECT ':a' /* :b */ -- :c\nWHERE x = $1",
			wantArgs: `[1]`,
		},
		{
			name:     "dollar quoted body is untouched",
			sql:      `AS $$ SELECT :nope $$ WHERE x = :x`,
			args:     Args{"x": 1},
			wantSQL:  `AS $$ SELECT :nope $$ WHERE x = $1`,
			wantArgs: `[1]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := mustRender(t, dollarDialect, tt.sql, tt.args)
			if gotSQL != tt.wantSQL {
				t.Errorf("sql\n got: %s\nwant: %s", gotSQL, tt.wantSQL)
			}
			if got := fmtArgs(gotArgs); got != tt.wantArgs {
				t.Errorf("args\n got: %s\nwant: %s", got, tt.wantArgs)
			}
		})
	}
}

func TestRenderQmarkDialect(t *testing.T) {
	gotSQL, gotArgs := mustRender(t, qmarkDialect,
		`WHERE org = :org AND id IN (:ids)`,
		Args{"org": 1, "ids": []int{4, 5}})
	if want := `WHERE org = ? AND id IN (?,?)`; gotSQL != want {
		t.Errorf("got %q want %q", gotSQL, want)
	}
	if want := `[1 4 5]`; fmtArgs(gotArgs) != want {
		t.Errorf("got %v want %v", gotArgs, want)
	}
}

func TestRenderOrdinal(t *testing.T) {
	gotSQL, gotArgs := mustRender(t, dollarDialect, `WHERE a = ? AND b IN (?)`, 1, []int{2, 3})
	if want := `WHERE a = $1 AND b IN ($2,$3)`; gotSQL != want {
		t.Errorf("got %q want %q", gotSQL, want)
	}
	if want := `[1 2 3]`; fmtArgs(gotArgs) != want {
		t.Errorf("got %v want %v", gotArgs, want)
	}
}

// A named statement must leave a bare '?' alone, because in Postgres that is
// the jsonb existence operator and not a placeholder.
func TestCompileDemotesQuestionMarkInNamedStatement(t *testing.T) {
	gotSQL, gotArgs := mustRender(t, dollarDialect,
		`SELECT * FROM t WHERE data ? 'key' AND id = :id`, Args{"id": 1})
	want := `SELECT * FROM t WHERE data ? 'key' AND id = $1`
	if gotSQL != want {
		t.Errorf("got %q\nwant %q", gotSQL, want)
	}
	if fmtArgs(gotArgs) != `[1]` {
		t.Errorf("args = %v", gotArgs)
	}
}

func TestCompileEscapedQuestionMark(t *testing.T) {
	gotSQL, _ := mustRender(t, dollarDialect, `WHERE data ?? 'k' AND id = ?`, 1)
	if want := `WHERE data ? 'k' AND id = $1`; gotSQL != want {
		t.Errorf("got %q want %q", gotSQL, want)
	}
}

func TestCompileNumberedPassesThrough(t *testing.T) {
	gotSQL, gotArgs := mustRender(t, dollarDialect, `WHERE a = $1 AND b = $2`, 10, 20)
	if want := `WHERE a = $1 AND b = $2`; gotSQL != want {
		t.Errorf("got %q want %q", gotSQL, want)
	}
	if want := `[10 20]`; fmtArgs(gotArgs) != want {
		t.Errorf("got %v want %v", gotArgs, want)
	}
}

func TestCompileRejectsMixedStyles(t *testing.T) {
	for _, sql := range []string{
		`WHERE a = :a AND b = $1`,
		`WHERE a = ? AND b = $1`,
	} {
		if _, err := compile(sql); err == nil {
			t.Errorf("compile(%q) should have failed", sql)
		} else if !strings.Contains(err.Error(), "pick one") {
			t.Errorf("compile(%q) error = %v, want a mixing complaint", sql, err)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		src  namedSource
		pos  []any
		want string
	}{
		{
			name: "missing named argument lists what is available",
			sql:  `WHERE a = :a AND b = :b`,
			src:  argsSource(Args{"a": 1}),
			want: `no argument for parameter "b"; available: "a"`,
		},
		{
			name: "named statement with no arguments",
			sql:  `WHERE a = :a`,
			want: `no named arguments were supplied`,
		},
		{
			name: "too few positional arguments",
			sql:  `WHERE a = ? AND b = ?`,
			pos:  []any{1},
			want: `has 2 placeholders but 1 arguments`,
		},
		{
			name: "too many positional arguments",
			sql:  `WHERE a = ?`,
			pos:  []any{1, 2},
			want: `has 1 placeholders but 2 arguments`,
		},
		{
			name: "named arguments for an ordinal statement",
			sql:  `WHERE a = ?`,
			src:  argsSource(Args{"a": 1}),
			want: `ordinal (?) parameters but named arguments`,
		},
		{
			name: "named arguments for a parameterless statement",
			sql:  `SELECT 1`,
			src:  argsSource(Args{"a": 1}),
			want: `no named parameters`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := compile(tt.sql)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			_, _, err = p.render(dollarDialect, tt.src, tt.pos)
			if err == nil {
				t.Fatalf("expected an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// pgArray stands in for a driver type that marshals a slice itself, such as
// pgtype's array types. row must pass it through whole rather than expanding it.
type pgArray []int

func (a pgArray) Value() (driver.Value, error) { return "{1,2,3}", nil }

func TestSliceExpansionSkipsValuers(t *testing.T) {
	gotSQL, gotArgs := mustRender(t, dollarDialect, `WHERE a = :a`, Args{"a": pgArray{1, 2, 3}})
	if want := `WHERE a = $1`; gotSQL != want {
		t.Errorf("got %q want %q", gotSQL, want)
	}
	if len(gotArgs) != 1 {
		t.Errorf("expected the Valuer to be passed whole, got %d args", len(gotArgs))
	}
}

func TestSliceElems(t *testing.T) {
	type named []byte
	cases := []struct {
		v      any
		expand bool
	}{
		{[]int{1, 2}, true},
		{[]string{"a"}, true},
		{[]any{1, "a"}, true},
		{[2]int{1, 2}, true},
		{[]byte("x"), false},
		{named("x"), false},
		{"x", false},
		{42, false},
		{nil, false},
		{pgArray{1}, false},
	}
	for _, c := range cases {
		if _, got := sliceElems(c.v); got != c.expand {
			t.Errorf("sliceElems(%#v) expand = %v, want %v", c.v, got, c.expand)
		}
	}
}

func TestPlanCacheEvicts(t *testing.T) {
	c := newPlanCache(32)
	for i := 0; i < 500; i++ {
		c.put(strings.Repeat("x", i), &plan{})
	}
	if n := c.len(); n > 32 {
		t.Errorf("cache holds %d plans, want at most 32", n)
	}
	if n := c.len(); n == 0 {
		t.Errorf("cache evicted everything")
	}
}

func TestPlanCacheRoundTrip(t *testing.T) {
	c := newPlanCache(64)
	p := &plan{nslots: 3}
	c.put("k", p)
	got, ok := c.get("k")
	if !ok || got != p {
		t.Fatalf("get = %v, %v; want the stored plan", got, ok)
	}
	if _, ok := c.get("missing"); ok {
		t.Errorf("get(missing) reported a hit")
	}
}

func BenchmarkCompile(b *testing.B) {
	sql := `SELECT id, name, created_at FROM users WHERE org_id = :org AND status IN (:statuses) AND name ILIKE :q`
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := compile(sql); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRender(b *testing.B) {
	p, err := compile(`SELECT id FROM users WHERE org_id = :org AND status IN (:statuses)`)
	if err != nil {
		b.Fatal(err)
	}
	src := argsSource(Args{"org": 1, "statuses": []string{"a", "b", "c"}})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := p.render(dollarDialect, src, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func TestParamStyleString(t *testing.T) {
	cases := map[paramStyle]string{
		styleNone:     "none",
		styleNamed:    "named (:name)",
		styleOrdinal:  "ordinal (?)",
		styleNumbered: "numbered ($1)",
	}
	for style, want := range cases {
		if got := style.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", style, got, want)
		}
	}
	// compile must infer the style from the statement.
	for sql, want := range map[string]paramStyle{
		`SELECT 1`:           styleNone,
		`WHERE a = :a`:       styleNamed,
		`WHERE a = ?`:        styleOrdinal,
		`WHERE a = $1`:       styleNumbered,
		`WHERE d ? 'k'`:      styleOrdinal,
		`WHERE d ?| ARRAY[]`: styleNone,
	} {
		p, err := compile(sql)
		if err != nil {
			t.Fatalf("compile(%q): %v", sql, err)
		}
		if p.style != want {
			t.Errorf("compile(%q).style = %v, want %v", sql, p.style, want)
		}
	}
}
