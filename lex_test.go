package row

import (
	"fmt"
	"strings"
	"testing"
)

// summarize renders the token stream compactly so table tests can state the
// expectation as a readable string: text runs appear as-is, parameters as
// :name, ?, $n or ?? markers.
func summarize(sql string, toks []token) string {
	var b strings.Builder
	for _, t := range toks {
		switch t.kind {
		case tokText:
			b.WriteString(sql[t.lo:t.hi])
		case tokNamed:
			b.WriteString("<:" + t.name + ">")
		case tokOrdinal:
			b.WriteString("<?>")
		case tokNumbered:
			fmt.Fprintf(&b, "<$%d>", t.num)
		case tokEscapedQ:
			b.WriteString("<??>")
		}
	}
	return b.String()
}

func TestLex(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "plain named param",
			sql:  `SELECT * FROM t WHERE id = :id`,
			want: `SELECT * FROM t WHERE id = <:id>`,
		},
		{
			name: "several params",
			sql:  `WHERE a = :a AND b = :b AND a = :a`,
			want: `WHERE a = <:a> AND b = <:b> AND a = <:a>`,
		},
		{
			name: "cast is not a param",
			sql:  `SELECT id::text FROM t`,
			want: `SELECT id::text FROM t`,
		},
		{
			name: "cast next to a param",
			sql:  `WHERE id = :id::bigint`,
			want: `WHERE id = <:id>::bigint`,
		},
		{
			name: "chained casts",
			sql:  `SELECT a::text::bytea, :v::int FROM t`,
			want: `SELECT a::text::bytea, <:v>::int FROM t`,
		},
		{
			name: "colon inside single quotes",
			sql:  `SELECT ':notaparam' , :real`,
			want: `SELECT ':notaparam' , <:real>`,
		},
		{
			name: "doubled quote inside string",
			sql:  `SELECT 'it''s :not a param', :yes`,
			want: `SELECT 'it''s :not a param', <:yes>`,
		},
		{
			name: "colon inside quoted identifier",
			sql:  `SELECT "weird:col" FROM t WHERE x = :x`,
			want: `SELECT "weird:col" FROM t WHERE x = <:x>`,
		},
		{
			name: "doubled quote inside identifier",
			sql:  `SELECT "a""b:c" FROM t`,
			want: `SELECT "a""b:c" FROM t`,
		},
		{
			name: "backtick identifier",
			sql:  "SELECT `a:b` FROM t WHERE x = :x",
			want: "SELECT `a:b` FROM t WHERE x = <:x>",
		},
		{
			name: "line comment hides a param",
			sql:  "SELECT 1 -- :nope\nWHERE x = :yes",
			want: "SELECT 1 -- :nope\nWHERE x = <:yes>",
		},
		{
			name: "block comment hides a param",
			sql:  `SELECT /* :nope */ :yes`,
			want: `SELECT /* :nope */ <:yes>`,
		},
		{
			name: "nested block comment",
			sql:  `SELECT /* a /* :deep */ still */ :yes`,
			want: `SELECT /* a /* :deep */ still */ <:yes>`,
		},
		{
			name: "dollar quoted body",
			sql:  `CREATE FUNCTION f() AS $$ SELECT :nope; $$ LANGUAGE sql`,
			want: `CREATE FUNCTION f() AS $$ SELECT :nope; $$ LANGUAGE sql`,
		},
		{
			name: "tagged dollar quote",
			sql:  `AS $body$ x := :nope $body$ WHERE a = :yes`,
			want: `AS $body$ x := :nope $body$ WHERE a = <:yes>`,
		},
		{
			name: "dollar quote containing a lookalike tag",
			sql:  `$a$ inner $b$ :nope $b$ still $a$ :yes`,
			want: `$a$ inner $b$ :nope $b$ still $a$ <:yes>`,
		},
		{
			name: "numbered placeholders",
			sql:  `WHERE a = $1 AND b = $12`,
			want: `WHERE a = <$1> AND b = <$12>`,
		},
		{
			name: "escape string with backslash quote",
			sql:  `SELECT E'no \' escape :nope', :yes`,
			want: `SELECT E'no \' escape :nope', <:yes>`,
		},
		{
			name: "lowercase escape string",
			sql:  `SELECT e'\\:nope', :yes`,
			want: `SELECT e'\\:nope', <:yes>`,
		},
		{
			name: "identifier ending in E is not an escape string",
			sql:  `SELECT VALUE'x' FROM t`,
			want: `SELECT VALUE'x' FROM t`,
		},
		{
			// A bare '?' is only a candidate here. Whether it is a placeholder
			// or a jsonb existence operator depends on whether the statement
			// also uses named parameters, which compile decides. See
			// TestCompileDemotesQuestionMarkInNamedStatement.
			name: "bare question mark is a candidate placeholder",
			sql:  `WHERE data ? 'key'`,
			want: `WHERE data <?> 'key'`,
		},
		{
			name: "jsonb any operator",
			sql:  `WHERE data ?| array['a','b']`,
			want: `WHERE data ?| array['a','b']`,
		},
		{
			name: "jsonb all operator",
			sql:  `WHERE data ?& array['a']`,
			want: `WHERE data ?& array['a']`,
		},
		{
			name: "ordinal placeholders",
			sql:  `WHERE a = ? AND b = ?`,
			want: `WHERE a = <?> AND b = <?>`,
		},
		{
			name: "escaped question mark",
			sql:  `WHERE data ?? 'key' AND id = ?`,
			want: `WHERE data <??> 'key' AND id = <?>`,
		},
		{
			name: "question mark inside a string",
			sql:  `SELECT 'why?' , ?`,
			want: `SELECT 'why?' , <?>`,
		},
		{
			name: "unicode identifier",
			sql:  `SELECT übung FROM t WHERE x = :wert`,
			want: `SELECT übung FROM t WHERE x = <:wert>`,
		},
		{
			name: "unicode parameter name",
			sql:  `WHERE x = :grösse`,
			want: `WHERE x = <:grösse>`,
		},
		{
			name: "unterminated string consumes rest",
			sql:  `SELECT 'oops :nope`,
			want: `SELECT 'oops :nope`,
		},
		{
			name: "unterminated block comment consumes rest",
			sql:  `SELECT /* :nope`,
			want: `SELECT /* :nope`,
		},
		{
			name: "unterminated dollar quote consumes rest",
			sql:  `SELECT $t$ :nope`,
			want: `SELECT $t$ :nope`,
		},
		{
			name: "lone colon is text",
			sql:  `SELECT a : b`,
			want: `SELECT a : b`,
		},
		{
			name: "lone dollar is text",
			sql:  `SELECT a $ b`,
			want: `SELECT a $ b`,
		},
		{
			name: "trailing colon",
			sql:  `SELECT a:`,
			want: `SELECT a:`,
		},
		{
			name: "param at very start",
			sql:  `:only`,
			want: `<:only>`,
		},
		{
			name: "empty statement",
			sql:  ``,
			want: ``,
		},
		{
			name: "param name with digits and underscores",
			sql:  `WHERE x = :user_id2`,
			want: `WHERE x = <:user_id2>`,
		},
		{
			name: "array slice lower bound is not a param",
			sql:  `SELECT arr[1:3] FROM t`,
			want: `SELECT arr[1:3] FROM t`,
		},
		{
			name: "comment markers inside a string",
			sql:  `SELECT '-- :nope /* :nope */', :yes`,
			want: `SELECT '-- :nope /* :nope */', <:yes>`,
		},
		{
			name: "string inside a comment",
			sql:  `SELECT 1 /* 'unclosed */ , :yes`,
			want: `SELECT 1 /* 'unclosed */ , <:yes>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarize(tt.sql, lex(tt.sql))
			if got != tt.want {
				t.Errorf("lex(%q)\n got: %s\nwant: %s", tt.sql, got, tt.want)
			}
		})
	}
}

// TestLexCoversInput is the invariant that matters most: the token ranges must
// tile the input exactly, in order, with no gaps and no overlaps. If that holds
// then rendering the tokens back out can never corrupt a statement.
func TestLexCoversInput(t *testing.T) {
	inputs := []string{
		`SELECT * FROM t WHERE a = :a AND b::text = ? AND c = $3`,
		`$$ body $$ :x '?' "id:col" -- trail`,
		`/* /* nested */ */ E'\\' :p ?| x`,
		``,
		`:`,
		`??`,
		`$`,
		`$$`,
	}
	for _, sql := range inputs {
		t.Run(sql, func(t *testing.T) {
			toks := lex(sql)
			pos := 0
			for i, tk := range toks {
				if tk.lo != pos {
					t.Fatalf("token %d starts at %d, want %d", i, tk.lo, pos)
				}
				if tk.hi < tk.lo || tk.hi > len(sql) {
					t.Fatalf("token %d has bad range [%d,%d) for len %d", i, tk.lo, tk.hi, len(sql))
				}
				pos = tk.hi
			}
			if pos != len(sql) {
				t.Fatalf("tokens cover %d bytes, input is %d", pos, len(sql))
			}
		})
	}
}

func FuzzLex(f *testing.F) {
	for _, s := range []string{
		`SELECT :a, ?, $1 FROM t`,
		`$tag$ :x $tag$`,
		`E'\\'`, `/* /* */ */`, `'`, `"`, "`", `??`, `?|`, `::`, `$`, `--`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		toks := lex(sql)
		// The tiling invariant must survive arbitrary bytes.
		pos := 0
		for _, tk := range toks {
			if tk.lo != pos || tk.hi < tk.lo || tk.hi > len(sql) {
				t.Fatalf("bad tiling %v on %q", toks, sql)
			}
			pos = tk.hi
		}
		if pos != len(sql) {
			t.Fatalf("incomplete tiling: %d of %d on %q", pos, len(sql), sql)
		}
	})
}
