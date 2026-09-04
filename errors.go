package row

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNoRows is returned by One when a query produces no rows.
//
// It is sql.ErrNoRows, not a copy of it, so code that already tests for
// sql.ErrNoRows keeps working and errors.Is matches either spelling.
var ErrNoRows = sql.ErrNoRows

// Code is a portable classification of a database error. Engines report
// failures with their own vocabulary — SQLSTATE strings on Postgres, integer
// result codes on SQLite — and a Code is what those map onto so that calling
// code can branch without importing a driver.
type Code int

const (
	// Unknown means the error was not recognised by the dialect. It carries no
	// claim about what went wrong; inspect the wrapped error.
	Unknown Code = iota

	// UniqueViolation: a UNIQUE or PRIMARY KEY constraint was violated.
	UniqueViolation

	// ForeignKeyViolation: a FOREIGN KEY constraint was violated.
	ForeignKeyViolation

	// NotNullViolation: a NOT NULL column was given a NULL.
	NotNullViolation

	// CheckViolation: a CHECK constraint failed.
	CheckViolation

	// Deadlock: the engine aborted this transaction to break a deadlock.
	Deadlock

	// SerializationFailure: the transaction could not be serialised against a
	// concurrent one and must be retried.
	SerializationFailure

	// Busy: the database is locked by another connection (SQLite).
	Busy

	// Timeout: a statement or lock wait exceeded its timeout.
	Timeout
)

var codeNames = [...]string{
	Unknown:              "unknown",
	UniqueViolation:      "unique_violation",
	ForeignKeyViolation:  "foreign_key_violation",
	NotNullViolation:     "not_null_violation",
	CheckViolation:       "check_violation",
	Deadlock:             "deadlock",
	SerializationFailure: "serialization_failure",
	Busy:                 "busy",
	Timeout:              "timeout",
}

func (c Code) String() string {
	if c < 0 || int(c) >= len(codeNames) {
		return "Code(" + fmt.Sprint(int(c)) + ")"
	}
	return codeNames[c]
}

// Retryable reports whether a failure with this code is worth retrying with
// the same input. InTxRetry uses it to decide.
func (c Code) Retryable() bool {
	switch c {
	case Deadlock, SerializationFailure, Busy:
		return true
	default:
		return false
	}
}

// Error is the error type row returns for every database failure. It records
// what row was doing and the statement it was doing it with, which is the
// context that raw driver errors leave out.
type Error struct {
	// Op is the row operation that failed, e.g. "row.All" or "row.Insert".
	Op string

	// SQL is the statement as sent to the driver, after binding. It is empty
	// for failures that happen before a statement exists.
	SQL string

	// Code classifies Err when the dialect recognised it, else Unknown.
	Code Code

	// Err is the underlying error.
	Err error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Op)
	if e.Code != Unknown {
		b.WriteString(" [")
		b.WriteString(e.Code.String())
		b.WriteByte(']')
	}
	b.WriteString(": ")
	b.WriteString(e.Err.Error())
	if e.SQL != "" {
		b.WriteString("\n  sql: ")
		b.WriteString(collapseSpace(e.SQL))
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// Is lets errors.Is(err, row.ErrNoRows) and friends see through the wrapper
// even though Unwrap already handles the common case; it additionally allows
// matching on a bare Code via a sentinel-free comparison.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	// A zero-valued target with only Code set matches any error of that code.
	if t.Op == "" && t.SQL == "" && t.Err == nil {
		return t.Code == e.Code
	}
	return false
}

// wrap attaches operation context to err. It returns nil for a nil err, and
// avoids double-wrapping an error row already annotated.
func wrap(op, query string, d Dialect, err error) error {
	if err == nil {
		return nil
	}
	var already *Error
	if errors.As(err, &already) {
		return err
	}
	e := &Error{Op: op, SQL: query, Err: err}
	if d != nil {
		if code, ok := d.ClassifyError(err); ok {
			e.Code = code
		}
	}
	return e
}

// CodeOf reports the portable classification of err, or Unknown when err did
// not come from row or was not recognised by its dialect.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Unknown
}

// IsCode reports whether err was classified as the given code. It is the
// idiomatic way to branch on a constraint violation:
//
//	if row.IsCode(err, row.UniqueViolation) { ... }
func IsCode(err error, c Code) bool { return CodeOf(err) == c }

// collapseSpace squeezes runs of whitespace into single spaces so a multi-line
// statement stays readable on one line of an error message.
func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\n', '\r':
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteByte(c)
		}
	}
	return b.String()
}
