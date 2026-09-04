package integration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r-52/row"
	"github.com/r-52/row/internal/dbtest"
	"github.com/r-52/row/sqlite"
)

// row.Open is the low-level constructor the adapters build on.
func TestOpenWithDialect(t *testing.T) {
	db, err := row.Open(sqlite.Dialect, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if db.Dialect().Name() != "sqlite" {
		t.Errorf("dialect = %q", db.Dialect().Name())
	}
	n, err := row.One[int64](ctx, db, `SELECT 1`)
	if err != nil || n != 1 {
		t.Errorf("SELECT 1 = %d, %v", n, err)
	}
}

// The underlying database/sql handles are always reachable; row never hides
// them.
func TestUnderlyingHandlesAreExposed(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		if db.SQL() == nil {
			t.Fatal("DB.SQL() is nil")
		}
		var got int
		if err := db.SQL().QueryRowContext(ctx, "SELECT 1").Scan(&got); err != nil || got != 1 {
			t.Errorf("raw query: %d %v", got, err)
		}

		// The Conn is released before anything else asks the pool for one: an
		// in-memory SQLite pool holds a single connection, so holding it here
		// while starting a transaction would deadlock.
		func() {
			cn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer cn.Close()
			if cn.SQL() == nil {
				t.Error("Conn.SQL() is nil")
			}
			if cn.Dialect() != db.Dialect() {
				t.Error("Conn should inherit the dialect")
			}
		}()

		if err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
			if tx.SQL() == nil {
				t.Error("Tx.SQL() is nil")
			}
			if tx.Dialect() != db.Dialect() {
				t.Error("Tx should inherit the dialect")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// The name mapper is per-database, not a global, so two handles can disagree.
func TestWithNameMapper(t *testing.T) {
	ctx := context.Background()

	upper := func(s string) string { return strings.ToUpper(s) }
	db, err := sqlite.Open(ctx, ":memory:", row.WithNameMapper(upper))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := row.Exec(ctx, db, `CREATE TABLE t ("ID" INTEGER, "NAME" TEXT)`); err != nil {
		t.Fatal(err)
	}
	type rec struct {
		ID   int64
		Name string
	}
	if err := row.Insert(ctx, db, "t", &rec{ID: 1, Name: "x"}); err != nil {
		t.Fatal(err)
	}
	got, err := row.One[rec](ctx, db, `SELECT "ID", "NAME" FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 1 || got.Name != "x" {
		t.Errorf("got %+v", got)
	}

	// A second handle with the default mapper must be unaffected.
	other := dbtest.OpenSQLite(t)
	seedUsers(t, other, 1)
	if _, err := row.One[User](ctx, other,
		`SELECT id, name, email, age, score, active, data, bio, org_id, created_at FROM users`); err != nil {
		t.Fatalf("the default mapper was disturbed: %v", err)
	}
}

func TestWithPlanCacheSize(t *testing.T) {
	ctx := context.Background()

	// A zero-sized cache must still work; it just recompiles every time.
	db, err := sqlite.Open(ctx, ":memory:", row.WithPlanCacheSize(0))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 5; i++ {
		if n, err := row.One[int64](ctx, db, `SELECT :n`, row.Args{"n": i}); err != nil || n != int64(i) {
			t.Fatalf("iteration %d: %d %v", i, n, err)
		}
	}
}

func TestIsolationOption(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
			return insertOrg(ctx, tx, 1, "acme")
		}, row.Isolation(sql.LevelSerializable))
		if err != nil {
			t.Fatal(err)
		}
		if n := count(t, db, "orgs"); n != 1 {
			t.Errorf("orgs = %d", n)
		}
	})
}

// InTxRetry must actually retry when the failure is one a retry can fix.
func TestInTxRetryRetriesRetryableFailures(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()

		var mu sync.Mutex
		attempts := 0
		policy := row.RetryPolicy{Attempts: 5, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}

		err := row.InTxRetry(ctx, db, policy, func(ctx context.Context, tx *row.Tx) error {
			mu.Lock()
			attempts++
			n := attempts
			mu.Unlock()
			if n < 3 {
				// A synthetic retryable failure: row classifies by code, and
				// a wrapped Error carrying one is indistinguishable from a
				// real serialization failure to the retry loop.
				return &row.Error{Op: "test", Code: row.SerializationFailure, Err: errors.New("conflict")}
			}
			return insertOrg(ctx, tx, 1, "acme")
		})
		if err != nil {
			t.Fatal(err)
		}
		if attempts != 3 {
			t.Errorf("attempts = %d, want 3", attempts)
		}
		if n := count(t, db, "orgs"); n != 1 {
			t.Errorf("orgs = %d, want the successful attempt to have committed", n)
		}
	})
}

func TestInTxRetryGivesUp(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		attempts := 0
		policy := row.RetryPolicy{Attempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}

		err := row.InTxRetry(ctx, db, policy, func(ctx context.Context, tx *row.Tx) error {
			attempts++
			return &row.Error{Op: "test", Code: row.Deadlock, Err: errors.New("deadlock")}
		})
		if err == nil {
			t.Fatal("expected the failure to surface after the attempts ran out")
		}
		if attempts != 3 {
			t.Errorf("attempts = %d, want 3", attempts)
		}
		if !strings.Contains(err.Error(), "giving up after 3 attempts") {
			t.Errorf("error should say it gave up: %v", err)
		}
	})
}

func TestInTxRetryHonoursContextCancellation(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		policy := row.RetryPolicy{Attempts: 10, BaseDelay: 50 * time.Millisecond, MaxDelay: time.Second}

		err := row.InTxRetry(ctx, db, policy, func(context.Context, *row.Tx) error {
			attempts++
			if attempts == 1 {
				cancel()
			}
			return &row.Error{Op: "test", Code: row.Busy, Err: errors.New("locked")}
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if attempts != 1 {
			t.Errorf("attempts = %d; a cancelled context should stop the backoff", attempts)
		}
	})
}
