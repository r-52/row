package row

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// assignTo is a helper that runs assign into a fresh value of T.
func assignTo[T any](src any) (T, error) {
	var out T
	v := reflect.ValueOf(&out).Elem()
	err := assign(v, src)
	return out, err
}

func TestAssignBasics(t *testing.T) {
	if got, err := assignTo[string]("x"); err != nil || got != "x" {
		t.Errorf("string: %q %v", got, err)
	}
	if got, err := assignTo[string]([]byte("x")); err != nil || got != "x" {
		t.Errorf("[]byte->string: %q %v", got, err)
	}
	if got, err := assignTo[int64](int64(7)); err != nil || got != 7 {
		t.Errorf("int64: %d %v", got, err)
	}
	if got, err := assignTo[int](int64(7)); err != nil || got != 7 {
		t.Errorf("int64->int: %d %v", got, err)
	}
	if got, err := assignTo[float64](int64(7)); err != nil || got != 7 {
		t.Errorf("int64->float64: %v %v", got, err)
	}
	if got, err := assignTo[bool](true); err != nil || !got {
		t.Errorf("bool: %v %v", got, err)
	}
	if got, err := assignTo[bool](int64(1)); err != nil || !got {
		t.Errorf("int64->bool: %v %v", got, err)
	}
	if got, err := assignTo[[]byte]("abc"); err != nil || string(got) != "abc" {
		t.Errorf("string->[]byte: %q %v", got, err)
	}
	now := time.Now()
	if got, err := assignTo[time.Time](now); err != nil || !got.Equal(now) {
		t.Errorf("time: %v %v", got, err)
	}
}

func TestAssignParsesText(t *testing.T) {
	if got, err := assignTo[int]("42"); err != nil || got != 42 {
		t.Errorf("string->int: %d %v", got, err)
	}
	if got, err := assignTo[float64]([]byte("1.5")); err != nil || got != 1.5 {
		t.Errorf("bytes->float: %v %v", got, err)
	}
	if got, err := assignTo[bool]("true"); err != nil || !got {
		t.Errorf("string->bool: %v %v", got, err)
	}
}

func TestAssignPointers(t *testing.T) {
	got, err := assignTo[*int64](int64(3))
	if err != nil || got == nil || *got != 3 {
		t.Fatalf("*int64: %v %v", got, err)
	}
	// NULL leaves a pointer nil rather than allocating a zero.
	got, err = assignTo[*int64](nil)
	if err != nil || got != nil {
		t.Errorf("NULL->*int64: %v %v", got, err)
	}
}

// This is the guarantee that a NULL never turns into a silent zero value.
func TestAssignNullIntoNonNullableIsAnError(t *testing.T) {
	for _, f := range []func() (any, error){
		func() (any, error) { return assignTo[string](nil) },
		func() (any, error) { return assignTo[int](nil) },
		func() (any, error) { return assignTo[bool](nil) },
		func() (any, error) { return assignTo[time.Time](nil) },
	} {
		v, err := f()
		if err == nil {
			t.Errorf("assigning NULL to %T should fail", v)
			continue
		}
		if !strings.Contains(err.Error(), "sql.Null") {
			t.Errorf("the error should suggest a remedy: %v", err)
		}
	}
}

func TestAssignNullIntoNullableKinds(t *testing.T) {
	if got, err := assignTo[[]byte](nil); err != nil || got != nil {
		t.Errorf("NULL->[]byte: %v %v", got, err)
	}
	if got, err := assignTo[any](nil); err != nil || got != nil {
		t.Errorf("NULL->any: %v %v", got, err)
	}
}

// Anything implementing sql.Scanner handles its own NULLs, including the
// generic sql.Null[T] added in Go 1.22.
func TestAssignDelegatesToScanners(t *testing.T) {
	got, err := assignTo[sql.NullString](nil)
	if err != nil || got.Valid {
		t.Errorf("NullString from NULL: %+v %v", got, err)
	}
	got, err = assignTo[sql.NullString]("x")
	if err != nil || !got.Valid || got.String != "x" {
		t.Errorf("NullString from value: %+v %v", got, err)
	}

	g, err := assignTo[sql.Null[int64]](int64(5))
	if err != nil || !g.Valid || g.V != 5 {
		t.Errorf("sql.Null[int64]: %+v %v", g, err)
	}
	g, err = assignTo[sql.Null[int64]](nil)
	if err != nil || g.Valid {
		t.Errorf("sql.Null[int64] from NULL: %+v %v", g, err)
	}
}

// Silent truncation is worse than an error, so range violations are reported.
func TestAssignRejectsOverflow(t *testing.T) {
	if _, err := assignTo[int8](int64(300)); err == nil {
		t.Error("300 should not fit in an int8")
	}
	if _, err := assignTo[uint8](int64(-1)); err == nil {
		t.Error("-1 should not fit in a uint8")
	}
	if _, err := assignTo[float32](1e300); err == nil {
		t.Error("1e300 should not fit in a float32")
	}
	if _, err := assignTo[int](1.5); err == nil {
		t.Error("1.5 should not silently become an int")
	}
	if _, err := assignTo[int](2.0); err != nil {
		t.Errorf("a whole float should convert: %v", err)
	}
}

func TestAssignByteArray(t *testing.T) {
	got, err := assignTo[[4]byte]([]byte("abcd"))
	if err != nil || string(got[:]) != "abcd" {
		t.Errorf("[4]byte: %q %v", got, err)
	}
	if _, err := assignTo[[4]byte]([]byte("abc")); err == nil {
		t.Error("a length mismatch should be reported")
	}
}

func TestAssignCopiesByteSlices(t *testing.T) {
	// Drivers are entitled to reuse their buffers between rows, so the value
	// row keeps must be a copy.
	src := []byte("abc")
	got, err := assignTo[[]byte](src)
	if err != nil {
		t.Fatal(err)
	}
	src[0] = 'z'
	if string(got) != "abc" {
		t.Errorf("the scanned slice aliases the driver's buffer: %q", got)
	}
}

func TestAssignNamedTypes(t *testing.T) {
	type Status string
	if got, err := assignTo[Status]("active"); err != nil || got != "active" {
		t.Errorf("named string: %q %v", got, err)
	}
	type ID int64
	if got, err := assignTo[ID](int64(4)); err != nil || got != 4 {
		t.Errorf("named int: %d %v", got, err)
	}
}

// reflect would happily turn an int64 into a one-rune string; that is never
// what a caller means.
func TestAssignRefusesIntToStringRuneConversion(t *testing.T) {
	got, err := assignTo[string](int64(65))
	if err != nil {
		t.Fatalf("int64->string should format, not fail: %v", err)
	}
	if got != "65" {
		t.Errorf("got %q, want the formatted number, not a rune", got)
	}
}

func TestAssignReportsUnconvertible(t *testing.T) {
	if _, err := assignTo[time.Time]("not a time"); err == nil {
		t.Error("a string should not become a time.Time")
	}
	if _, err := assignTo[map[string]string](int64(1)); err == nil {
		t.Error("an int should not become a map")
	}
}

func TestIsLeaf(t *testing.T) {
	type plain struct{ A int }
	cases := []struct {
		v    any
		leaf bool
	}{
		{int(0), true},
		{"", true},
		{[]byte(nil), true},
		{time.Time{}, true},
		{sql.NullString{}, true},
		{sql.Null[int]{}, true},
		{plain{}, false},
		{&plain{}, false},
	}
	for _, c := range cases {
		if got := isLeaf(reflect.TypeOf(c.v)); got != c.leaf {
			t.Errorf("isLeaf(%T) = %v, want %v", c.v, got, c.leaf)
		}
	}
}

func TestCodeString(t *testing.T) {
	if got := UniqueViolation.String(); got != "unique_violation" {
		t.Errorf("got %q", got)
	}
	if got := Code(999).String(); !strings.HasPrefix(got, "Code(") {
		t.Errorf("an unknown code should be printable: %q", got)
	}
}

func TestCodeRetryable(t *testing.T) {
	for _, c := range []Code{Deadlock, SerializationFailure, Busy} {
		if !c.Retryable() {
			t.Errorf("%v should be retryable", c)
		}
	}
	for _, c := range []Code{Unknown, UniqueViolation, ForeignKeyViolation, NotNullViolation, CheckViolation, Timeout} {
		if c.Retryable() {
			t.Errorf("%v should not be retryable", c)
		}
	}
}

func TestErrorFormatting(t *testing.T) {
	e := &Error{
		Op:   "row.All",
		SQL:  "SELECT *\n  FROM t\n  WHERE x = 1",
		Code: UniqueViolation,
		Err:  &errCoded{code: UniqueViolation, msg: "duplicate key"},
	}
	msg := e.Error()
	for _, want := range []string{"row.All", "unique_violation", "duplicate key", "SELECT * FROM t WHERE x = 1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q should contain %q", msg, want)
		}
	}
}

func TestWrapDoesNotDoubleWrap(t *testing.T) {
	inner := wrap("row.One", "SELECT 1", dollarDialect, &errCoded{code: Busy, msg: "locked"})
	outer := wrap("row.All", "SELECT 2", dollarDialect, inner)
	if outer != inner {
		t.Errorf("wrap should pass an already-wrapped error through unchanged")
	}
	if CodeOf(outer) != Busy {
		t.Errorf("code = %v", CodeOf(outer))
	}
}

func TestWrapNilIsNil(t *testing.T) {
	if err := wrap("row.All", "", dollarDialect, nil); err != nil {
		t.Errorf("wrap(nil) = %v", err)
	}
}

func TestCodeOfUnwrappedError(t *testing.T) {
	if got := CodeOf(errorString("plain")); got != Unknown {
		t.Errorf("got %v", got)
	}
	if got := CodeOf(nil); got != Unknown {
		t.Errorf("got %v", got)
	}
}

func TestQuoteWith(t *testing.T) {
	cases := map[string]string{
		"users":      `"users"`,
		"we\"ird":    `"we""ird"`,
		"":           `""`,
		"UPPER":      `"UPPER"`,
		`a"b"c`:      `"a""b""c"`,
		"with space": `"with space"`,
	}
	for in, want := range cases {
		if got := QuoteWith(in, '"'); got != want {
			t.Errorf("QuoteWith(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestAppendOrdinal(t *testing.T) {
	got := string(AppendOrdinal([]byte("x="), '$', 42))
	if got != "x=$42" {
		t.Errorf("got %q", got)
	}
}

func TestCollapseSpace(t *testing.T) {
	if got := collapseSpace("  a\n\tb   c  "); got != "a b c" {
		t.Errorf("got %q", got)
	}
	if got := collapseSpace(""); got != "" {
		t.Errorf("got %q", got)
	}
}

// errors.Is can match on a code alone, which is occasionally more convenient
// than IsCode inside an errors.Is chain.
func TestErrorIsMatchesOnCode(t *testing.T) {
	err := wrap("row.Exec", "INSERT", dollarDialect, &errCoded{code: UniqueViolation, msg: "dup"})
	if !errors.Is(err, &Error{Code: UniqueViolation}) {
		t.Error("errors.Is should match an Error carrying only a Code")
	}
	if errors.Is(err, &Error{Code: Deadlock}) {
		t.Error("a different code should not match")
	}
	// A fully-populated target is not a wildcard.
	if errors.Is(err, &Error{Code: UniqueViolation, Op: "row.Exec", Err: errorString("dup")}) {
		t.Error("only a code-only target is treated as a pattern")
	}
}
