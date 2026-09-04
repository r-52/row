// Package sqlite adapts row to SQLite through modernc.org/sqlite, a pure-Go
// translation of the SQLite C sources. It needs no cgo, so programs using it
// still cross-compile with the standard toolchain.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"

	msqlite "modernc.org/sqlite"

	"github.com/r-52/row"
)

// Dialect is the SQLite dialect. It is stateless and safe to share.
var Dialect row.Dialect = dialect{}

type dialect struct{}

func (dialect) Name() string { return "sqlite" }

// DriverName is modernc.org/sqlite's database/sql registration.
func (dialect) DriverName() string { return "sqlite" }

// AppendPlaceholder emits "?"; SQLite binds them positionally.
func (dialect) AppendPlaceholder(dst []byte, _ int) []byte { return append(dst, '?') }

func (dialect) QuoteIdent(name string) string { return row.QuoteWith(name, '"') }

func (dialect) Features() row.Features {
	return row.Features{
		Returning:  true, // SQLite 3.35+, and modernc tracks upstream closely
		Savepoints: true,
		Upsert:     true,
		// SQLITE_MAX_VARIABLE_NUMBER defaults to 32766 since 3.32.
		MaxPlaceholders: 32766,
	}
}

// SQLite reports errors as a primary result code in the low byte plus an
// optional extended code in the upper bits: extended = primary | (n << 8).
const (
	codeBusy               = 5
	codeLocked             = 6
	codeConstraint         = 19
	codeConstraintCheck    = 275  // 19 | (1 << 8)
	codeConstraintFK       = 787  // 19 | (3 << 8)
	codeConstraintNotNull  = 1299 // 19 | (5 << 8)
	codeConstraintPK       = 1555 // 19 | (6 << 8)
	codeConstraintUnique   = 2067 // 19 | (8 << 8)
	codeConstraintRowID    = 2579 // 19 | (10 << 8)
	codeBusyRecovery       = 261
	codeBusySnapshot       = 517
	codeBusyTimeout        = 773
	codeInterrupt          = 9
	primaryResultCodeMask  = 0xff
	constraintPrimaryIsSet = codeConstraint
)

// ClassifyError maps SQLite result codes onto row's portable codes.
func (dialect) ClassifyError(err error) (row.Code, bool) {
	var serr *msqlite.Error
	if !errors.As(err, &serr) {
		return row.Unknown, false
	}
	switch c := serr.Code(); c {
	case codeConstraintUnique, codeConstraintPK, codeConstraintRowID:
		return row.UniqueViolation, true
	case codeConstraintFK:
		return row.ForeignKeyViolation, true
	case codeConstraintNotNull:
		return row.NotNullViolation, true
	case codeConstraintCheck:
		return row.CheckViolation, true
	case codeBusy, codeBusyRecovery, codeBusySnapshot, codeLocked:
		return row.Busy, true
	case codeBusyTimeout:
		return row.Timeout, true
	case codeInterrupt:
		return row.Timeout, true
	default:
		// An extended constraint code row does not name specifically still
		// tells us a constraint failed.
		if c&primaryResultCodeMask == constraintPrimaryIsSet {
			return row.CheckViolation, true
		}
		return row.Unknown, false
	}
}

// Open connects to a SQLite database.
//
// dsn is a file path, ":memory:", or a "file:" URI. Unless the DSN already
// sets them, Open applies the defaults a server-side application wants and
// SQLite does not supply on its own:
//
//	journal_mode=WAL     concurrent readers alongside one writer
//	busy_timeout=5000    wait for a lock instead of failing immediately
//	foreign_keys=on      enforce foreign keys, which SQLite disables by default
//
// Pass Raw to skip all of that.
func Open(ctx context.Context, dsn string, opts ...row.Option) (*row.DB, error) {
	return open(ctx, applyDefaults(dsn), opts...)
}

// Raw connects without adding any pragmas, for callers who want full control
// of the DSN.
func Raw(ctx context.Context, dsn string, opts ...row.Option) (*row.DB, error) {
	return open(ctx, dsn, opts...)
}

func open(ctx context.Context, dsn string, opts ...row.Option) (*row.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if isMemory(dsn) {
		// Every connection to ":memory:" gets its own private database, so a
		// pool would hand out unrelated empty databases at random. One
		// connection makes an in-memory database behave the way callers expect.
		//
		// The consequence is that an in-memory database serialises everything.
		// Holding a *row.Conn and then asking the same handle for a
		// transaction will deadlock, because the transaction waits for the
		// only connection. Release the Conn first, or use a file-backed
		// database. Set SetMaxOpenConns yourself via DB.SQL() to override.
		db.SetMaxOpenConns(1)
	}
	rdb := row.New(db, Dialect, opts...)
	if err := rdb.Ping(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return rdb, nil
}

func isMemory(dsn string) bool {
	return strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory")
}

// applyDefaults appends row's recommended pragmas to dsn, leaving alone any the
// caller already specified.
func applyDefaults(dsn string) string {
	defaults := [][2]string{
		{"_pragma", "journal_mode(WAL)"},
		{"_pragma", "busy_timeout(5000)"},
		{"_pragma", "foreign_keys(1)"},
	}
	// An in-memory database has no journal to put in WAL mode.
	if isMemory(dsn) {
		defaults = defaults[1:]
	}

	base, query, hasQuery := strings.Cut(dsn, "?")
	vals := url.Values{}
	if hasQuery {
		if parsed, err := url.ParseQuery(query); err == nil {
			vals = parsed
		} else {
			// Leave an unparseable DSN completely alone.
			return dsn
		}
	}
	existing := strings.Join(vals["_pragma"], " ")
	for _, d := range defaults {
		name, _, _ := strings.Cut(d[1], "(")
		if strings.Contains(existing, name) {
			continue
		}
		vals.Add(d[0], d[1])
	}
	return base + "?" + vals.Encode()
}
