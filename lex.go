package row

import "strings"

// This file implements the SQL scanner that makes row's parameter binding
// reliable. Libraries that rewrite placeholders with a naive search for ':'
// or '?' corrupt statements that contain casts, dollar-quoted bodies, comments
// or JSON operators. The scanner below walks the statement once and knows the
// difference.
//
// It is deliberately a lexer and not a parser: row never needs to understand
// what a statement *means*, only which byte ranges are literal text and which
// are bind parameters.

type tokenKind uint8

const (
	tokText     tokenKind = iota // verbatim SQL
	tokNamed                     // :name
	tokOrdinal                   // ? placeholder
	tokNumbered                  // $1 placeholder, already native to Postgres
	tokEscapedQ                  // ?? which stands for a literal ?
)

type token struct {
	kind tokenKind
	lo   int // inclusive byte offset into the source
	hi   int // exclusive
	name string
	num  int
}

// isIdentStart reports whether c may begin an unquoted identifier. Bytes at or
// above 0x80 are accepted so that UTF-8 identifiers work without decoding.
func isIdentStart(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		c >= 0x80
}

// isIdentByte reports whether c may continue an unquoted identifier.
//
// '$' is excluded even though Postgres permits it, because admitting it here
// would make "$tag$" indistinguishable from an identifier followed by a
// dollar-quote, and dollar-quoting is by far the more important of the two to
// get right.
func isIdentByte(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// lex splits sql into literal text runs and parameter references.
//
// Adjacent text is merged, so the result alternates between at most one text
// token and each parameter. Unterminated strings and comments are not errors:
// the scanner consumes to end of input and lets the database report the syntax
// error, which produces a far better message than anything row could invent.
func lex(sql string) []token {
	var toks []token
	n := len(sql)
	textStart := 0

	flush := func(end int) {
		if end > textStart {
			toks = append(toks, token{kind: tokText, lo: textStart, hi: end})
		}
	}

	for i := 0; i < n; {
		c := sql[i]
		switch {
		// -- line comment
		case c == '-' && i+1 < n && sql[i+1] == '-':
			i += 2
			for i < n && sql[i] != '\n' {
				i++
			}

		// /* block comment */, nested as Postgres allows
		case c == '/' && i+1 < n && sql[i+1] == '*':
			i += 2
			for depth := 1; i < n && depth > 0; {
				switch {
				case sql[i] == '/' && i+1 < n && sql[i+1] == '*':
					depth++
					i += 2
				case sql[i] == '*' && i+1 < n && sql[i+1] == '/':
					depth--
					i += 2
				default:
					i++
				}
			}

		// E'...' escape string: backslashes escape, including \'
		case (c == 'E' || c == 'e') && i+1 < n && sql[i+1] == '\'':
			i = skipEscapeString(sql, i+1)

		// '...' string, "..." and `...` quoted identifiers; doubling escapes
		case c == '\'' || c == '"' || c == '`':
			i = skipDoubled(sql, i, c)

		case c == '$':
			i = lexDollar(sql, i, &toks, &textStart, flush)

		case c == ':':
			// "::" is a cast, never a parameter.
			if i+1 < n && sql[i+1] == ':' {
				i += 2
				break
			}
			if i+1 < n && isIdentStart(sql[i+1]) {
				j := i + 1
				for j < n && isIdentByte(sql[j]) {
					j++
				}
				flush(i)
				toks = append(toks, token{kind: tokNamed, lo: i, hi: j, name: sql[i+1 : j]})
				textStart = j
				i = j
				break
			}
			i++

		case c == '?':
			// Postgres spells its JSON existence operators ?, ?| and ?&.
			// The two-character forms are unambiguous, so handle them first.
			if i+1 < n && (sql[i+1] == '|' || sql[i+1] == '&') {
				i += 2
				break
			}
			// "??" is the escape for a literal question mark.
			if i+1 < n && sql[i+1] == '?' {
				flush(i)
				toks = append(toks, token{kind: tokEscapedQ, lo: i, hi: i + 2})
				textStart = i + 2
				i += 2
				break
			}
			flush(i)
			toks = append(toks, token{kind: tokOrdinal, lo: i, hi: i + 1})
			textStart = i + 1
			i++

		// Consume identifiers whole so that a keyword ending in E, or an
		// identifier containing a digit, cannot be misread by the cases above.
		case isIdentStart(c):
			i++
			for i < n && isIdentByte(sql[i]) {
				i++
			}

		default:
			i++
		}
	}
	flush(n)
	return toks
}

// lexDollar handles the two very different things a '$' can start: a numbered
// placeholder ($1) or a dollar-quoted string ($tag$ ... $tag$). It returns the
// offset to resume scanning at.
func lexDollar(sql string, i int, toks *[]token, textStart *int, flush func(int)) int {
	n := len(sql)

	// $1, $2, ... — a placeholder Postgres already understands.
	if i+1 < n && isDigit(sql[i+1]) {
		j := i + 1
		num := 0
		for j < n && isDigit(sql[j]) {
			num = num*10 + int(sql[j]-'0')
			j++
		}
		flush(i)
		*toks = append(*toks, token{kind: tokNumbered, lo: i, hi: j, num: num})
		*textStart = j
		return j
	}

	// $tag$ ... $tag$, where tag may be empty ($$ ... $$).
	j := i + 1
	for j < n && isIdentByte(sql[j]) {
		j++
	}
	if j < n && sql[j] == '$' {
		tag := sql[i : j+1]
		rest := sql[j+1:]
		if k := strings.Index(rest, tag); k >= 0 {
			return j + 1 + k + len(tag)
		}
		return n // unterminated; let the server complain
	}

	// A lone '$'. Ordinary text.
	return i + 1
}

// skipDoubled consumes a quoted run delimited by q, where the delimiter is
// escaped by doubling it. It returns the offset just past the closing quote.
func skipDoubled(sql string, i int, q byte) int {
	n := len(sql)
	i++ // opening quote
	for i < n {
		if sql[i] == q {
			if i+1 < n && sql[i+1] == q {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return n
}

// skipEscapeString consumes a Postgres E'...' body starting at the opening
// quote. Both backslash escapes and doubled quotes terminate correctly.
func skipEscapeString(sql string, i int) int {
	n := len(sql)
	i++ // opening quote
	for i < n {
		switch sql[i] {
		case '\\':
			i += 2
		case '\'':
			if i+1 < n && sql[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		default:
			i++
		}
	}
	return n
}
