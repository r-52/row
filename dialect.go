package row

import "strconv"

// Dialect describes the handful of ways SQL engines differ from one another.
//
// row's core is engine-agnostic: everything above this interface is shared by
// every supported database. Implementations live in subpackages (row/pg,
// row/sqlite) so that importing row does not drag in a driver.
type Dialect interface {
	// Name is a short stable identifier, e.g. "postgres" or "sqlite". It is
	// used as part of the statement cache key, so distinct dialects must not
	// share a name.
	Name() string

	// DriverName is the name the engine's driver registers with database/sql.
	DriverName() string

	// AppendPlaceholder appends the placeholder for the n-th bind parameter
	// (1-based) to dst and returns the extended slice. Postgres appends "$n";
	// SQLite appends "?".
	AppendPlaceholder(dst []byte, n int) []byte

	// QuoteIdent quotes a single identifier so it survives reserved words and
	// unusual characters.
	QuoteIdent(name string) string

	// Features reports what the engine can do.
	Features() Features

	// ClassifyError maps a driver error onto a portable Code. It reports false
	// when the error is not one it recognises, in which case the error is left
	// unclassified rather than guessed at.
	ClassifyError(err error) (Code, bool)
}

// Features records optional engine capabilities. Zero value means "supports
// nothing optional", which is the safe default for a new dialect.
type Features struct {
	// Returning reports whether INSERT/UPDATE/DELETE ... RETURNING works.
	Returning bool

	// Savepoints reports whether SAVEPOINT / ROLLBACK TO / RELEASE work,
	// which is what nested InTx calls are built on.
	Savepoints bool

	// Upsert reports whether INSERT ... ON CONFLICT works.
	Upsert bool

	// MaxPlaceholders is the largest number of bind parameters a single
	// statement accepts, or 0 when the engine has no meaningful limit.
	// InsertMany chunks its batches to stay under it.
	MaxPlaceholders int
}

// QuoteWith is a helper for dialects whose quoting rule is "wrap in q and
// double any occurrence of q inside". It covers "..." and `...` alike.
func QuoteWith(name string, q byte) string {
	// Count first so the common case allocates exactly once.
	extra := 0
	for i := 0; i < len(name); i++ {
		if name[i] == q {
			extra++
		}
	}
	buf := make([]byte, 0, len(name)+extra+2)
	buf = append(buf, q)
	for i := 0; i < len(name); i++ {
		if name[i] == q {
			buf = append(buf, q)
		}
		buf = append(buf, name[i])
	}
	buf = append(buf, q)
	return string(buf)
}

// AppendOrdinal appends prefix followed by the decimal form of n. Postgres
// dialects use it to emit "$1", "$2" and so on without allocating.
func AppendOrdinal(dst []byte, prefix byte, n int) []byte {
	dst = append(dst, prefix)
	return strconv.AppendInt(dst, int64(n), 10)
}
