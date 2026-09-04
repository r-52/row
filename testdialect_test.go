package row

import (
	"errors"
	"fmt"
)

// testDialect stands in for a real engine in unit tests. Two variants let the
// same table cover both placeholder conventions without importing a driver.
type testDialect struct {
	name     string
	numbered bool
	quote    byte
}

var (
	dollarDialect = testDialect{name: "test-dollar", numbered: true, quote: '"'}
	qmarkDialect  = testDialect{name: "test-qmark", numbered: false, quote: '"'}
)

func (d testDialect) Name() string       { return d.name }
func (d testDialect) DriverName() string { return "testdriver" }

func (d testDialect) AppendPlaceholder(dst []byte, n int) []byte {
	if d.numbered {
		return AppendOrdinal(dst, '$', n)
	}
	return append(dst, '?')
}

func (d testDialect) QuoteIdent(s string) string { return QuoteWith(s, d.quote) }

func (d testDialect) Features() Features {
	return Features{Returning: true, Savepoints: true, Upsert: true, MaxPlaceholders: 100}
}

// errCoded lets tests inject an error that classifies to a known Code.
type errCoded struct {
	code Code
	msg  string
}

func (e *errCoded) Error() string { return e.msg }

func (d testDialect) ClassifyError(err error) (Code, bool) {
	var ec *errCoded
	if errors.As(err, &ec) {
		return ec.code, true
	}
	return Unknown, false
}

func mustRender(t interface{ Fatalf(string, ...any) }, d Dialect, sql string, args ...any) (string, []any) {
	p, err := compile(sql)
	if err != nil {
		t.Fatalf("compile(%q): %v", sql, err)
	}
	var src namedSource
	var pos []any
	if len(args) == 1 {
		if a, ok := args[0].(Args); ok {
			src = argsSource(a)
		} else {
			pos = args
		}
	} else {
		pos = args
	}
	out, outArgs, err := p.render(d, src, pos)
	if err != nil {
		t.Fatalf("render(%q): %v", sql, err)
	}
	return out, outArgs
}

func fmtArgs(a []any) string { return fmt.Sprint(a) }
