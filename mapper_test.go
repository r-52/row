package row

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSnakeCase(t *testing.T) {
	cases := map[string]string{
		"ID":          "id",
		"UserID":      "user_id",
		"Name":        "name",
		"CreatedAt":   "created_at",
		"HTTPCode":    "http_code",
		"OAuth2Key":   "o_auth2_key",
		"URL":         "url",
		"APIKey":      "api_key",
		"A":           "a",
		"":            "",
		"already_ok":  "already_ok",
		"HTML":        "html",
		"ParseHTML":   "parse_html",
		"XMLHTTPPost": "xmlhttp_post",
		"Field2":      "field2",
	}
	for in, want := range cases {
		if got := SnakeCase(in); got != want {
			t.Errorf("SnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

type simple struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

type tagged struct {
	ID       int64  `db:"user_id,pk"`
	Name     string `db:"full_name"`
	Secret   string `db:"-"`
	Computed int    `db:"computed,readonly"`
	private  int
}

type base struct {
	ID   int64 `db:"id,pk"`
	Kind string
}

type embedded struct {
	base
	Name string
}

type address struct {
	City string
	Zip  string
}

type withNested struct {
	ID      int64
	Address address
}

type withInline struct {
	ID      int64
	Address address `db:",inline"`
}

type withPrefix struct {
	ID      int64
	Address address `db:",prefix=home_"`
}

type withPointer struct {
	ID      int64
	Address *address
}

type clashing struct {
	A struct{ X int } `db:",inline"`
	B struct{ X int } `db:",inline"`
}

type selfRef struct {
	ID     int64
	Parent *selfRef
}

type withScanner struct {
	ID   int64
	Name sql.NullString
	When sql.Null[time.Time]
}

func columnsOf(t *testing.T, v any) []string {
	t.Helper()
	si, err := describeStruct(reflect.TypeOf(v), SnakeCase)
	if err != nil {
		t.Fatalf("describeStruct(%T): %v", v, err)
	}
	return si.columns()
}

func TestDescribeStructColumns(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want []string
	}{
		{"plain fields", simple{}, []string{"id", "name", "created_at"}},
		{"tags override, dash skips, unexported ignored", tagged{}, []string{"user_id", "full_name", "computed"}},
		{"embedded structs merge without a prefix", embedded{}, []string{"id", "kind", "name"}},
		{"named struct gets a derived prefix", withNested{}, []string{"id", "address_city", "address_zip"}},
		{"inline drops the prefix", withInline{}, []string{"id", "city", "zip"}},
		{"prefix option wins", withPrefix{}, []string{"id", "home_city", "home_zip"}},
		{"pointer struct maps like a value", withPointer{}, []string{"id", "address_city", "address_zip"}},
		{"scanners are leaves", withScanner{}, []string{"id", "name", "when"}},
		{"self reference terminates", selfRef{}, []string{"id"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := columnsOf(t, c.val)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("columns = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDescribeStructReportsCollisions(t *testing.T) {
	_, err := describeStruct(reflect.TypeOf(clashing{}), SnakeCase)
	if err == nil {
		t.Fatal("expected an error for two fields claiming the same column")
	}
	for _, want := range []string{"same column", `"x"`, "A.X", "B.X"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestPrimaryKeys(t *testing.T) {
	si, err := describeStruct(reflect.TypeOf(tagged{}), SnakeCase)
	if err != nil {
		t.Fatal(err)
	}
	if len(si.pks) != 1 || si.fields[si.pks[0]].column != "user_id" {
		t.Errorf("pks = %v, want the user_id field", si.pks)
	}
	if !si.fields[2].readonly {
		t.Errorf("computed should be readonly")
	}
}

func TestFieldIndexPathsAreIndependent(t *testing.T) {
	// A bug where append aliases the shared backing array shows up as two
	// fields pointing at the same index path.
	si, err := describeStruct(reflect.TypeOf(withNested{}), SnakeCase)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, f := range si.fields {
		key := ""
		for _, i := range f.index {
			key += string(rune('0' + i))
		}
		if prev, dup := seen[key]; dup {
			t.Errorf("fields %s and %s share index path %v", prev, f.path, f.index)
		}
		seen[key] = f.path
	}
}

func TestNameMapperIsPerConfig(t *testing.T) {
	upper := func(s string) string { return strings.ToUpper(s) }
	si, err := describeStruct(reflect.TypeOf(simple{}), upper)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ID", "NAME", "CREATEDAT"}
	if got := si.columns(); !reflect.DeepEqual(got, want) {
		t.Errorf("columns = %v, want %v", got, want)
	}
	// The default mapper must be unaffected by the call above.
	if got := columnsOf(t, simple{}); !reflect.DeepEqual(got, []string{"id", "name", "created_at"}) {
		t.Errorf("default mapper was disturbed: %v", got)
	}
}

func TestStructSourceBindsNamedParams(t *testing.T) {
	u := tagged{ID: 5, Name: "ada", Computed: 9}
	src, ok, err := structSource(u, SnakeCase)
	if err != nil || !ok {
		t.Fatalf("structSource: ok=%v err=%v", ok, err)
	}
	if v, ok := src.lookup("full_name"); !ok || v != "ada" {
		t.Errorf("lookup(full_name) = %v, %v", v, ok)
	}
	if _, ok := src.lookup("secret"); ok {
		t.Errorf("a db:\"-\" field should not be bindable")
	}
}

func TestStructSourceThroughPointer(t *testing.T) {
	u := &tagged{ID: 5}
	src, ok, _ := structSource(u, SnakeCase)
	if !ok {
		t.Fatal("a pointer to a struct should be usable as a named source")
	}
	if v, _ := src.lookup("user_id"); v != int64(5) {
		t.Errorf("lookup(user_id) = %v", v)
	}
}

func TestStructSourceRejectsNonStructs(t *testing.T) {
	for _, v := range []any{42, "s", nil, (*tagged)(nil), time.Now()} {
		if _, ok, _ := structSource(v, SnakeCase); ok {
			t.Errorf("structSource(%T) should not have claimed the value", v)
		}
	}
}

func TestStructSourceNilBranchIsNull(t *testing.T) {
	src, ok, _ := structSource(withPointer{ID: 1}, SnakeCase)
	if !ok {
		t.Fatal("expected a named source")
	}
	v, found := src.lookup("address_city")
	if !found {
		t.Fatal("the column should still be known")
	}
	if v != nil {
		t.Errorf("a nil nested struct should bind NULL, got %v", v)
	}
}

func TestUseStructAsNamedArgs(t *testing.T) {
	gotSQL, gotArgs := mustRender(t, dollarDialect,
		`INSERT INTO users (user_id, full_name) VALUES (:user_id, :full_name)`,
		Args{"user_id": 1, "full_name": "ada"})
	if !strings.Contains(gotSQL, "VALUES ($1, $2)") {
		t.Errorf("sql = %q", gotSQL)
	}
	if fmtArgs(gotArgs) != "[1 ada]" {
		t.Errorf("args = %v", gotArgs)
	}
}

func BenchmarkDescribeStructCached(b *testing.B) {
	tp := reflect.TypeOf(withNested{})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := describeStruct(tp, SnakeCase); err != nil {
			b.Fatal(err)
		}
	}
}
