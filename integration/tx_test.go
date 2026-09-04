package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/r-52/row"
	"github.com/r-52/row/internal/dbtest"
)

func count(t *testing.T, db row.Session, table string) int64 {
	t.Helper()
	n, err := row.One[int64](context.Background(), db, `SELECT count(*) FROM `+table)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func insertOrg(ctx context.Context, s row.Session, id int64, name string) error {
	_, err := row.Exec(ctx, s, `INSERT INTO orgs (id, name) VALUES (:id, :name)`,
		row.Args{"id": id, "name": name})
	return err
}

func TestTxCommits(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
			return insertOrg(ctx, tx, 1, "acme")
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := count(t, db, "orgs"); n != 1 {
			t.Errorf("orgs = %d, want 1", n)
		}
	})
}

func TestTxRollsBackOnError(t *testing.T) {
	sentinel := errors.New("nope")
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
			if err := insertOrg(ctx, tx, 1, "acme"); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("InTx returned %v, want the callback's error unchanged", err)
		}
		if n := count(t, db, "orgs"); n != 0 {
			t.Errorf("orgs = %d, want the insert rolled back", n)
		}
	})
}

func TestTxRollsBackOnPanic(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()

		func() {
			defer func() {
				if p := recover(); p == nil {
					t.Error("the panic should have propagated")
				}
			}()
			_ = row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
				if err := insertOrg(ctx, tx, 1, "acme"); err != nil {
					t.Fatal(err)
				}
				panic("boom")
			})
		}()

		if n := count(t, db, "orgs"); n != 0 {
			t.Errorf("orgs = %d, want the transaction rolled back after the panic", n)
		}
	})
}

func TestNestedTxUsesSavepoints(t *testing.T) {
	inner := errors.New("inner failed")
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()

		err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
			if tx.Depth() != 0 {
				t.Errorf("outer depth = %d, want 0", tx.Depth())
			}
			if err := insertOrg(ctx, tx, 1, "kept"); err != nil {
				return err
			}

			// The inner failure must undo only the inner work.
			nerr := row.InTx(ctx, tx, func(ctx context.Context, tx *row.Tx) error {
				if tx.Depth() != 1 {
					t.Errorf("inner depth = %d, want 1", tx.Depth())
				}
				if err := insertOrg(ctx, tx, 2, "discarded"); err != nil {
					return err
				}
				return inner
			})
			if !errors.Is(nerr, inner) {
				t.Errorf("nested InTx returned %v", nerr)
			}

			// The outer transaction must still be usable after the savepoint
			// rollback, which is the whole point of using one.
			return insertOrg(ctx, tx, 3, "also kept")
		})
		if err != nil {
			t.Fatal(err)
		}

		got, err := row.All[int64](ctx, db, `SELECT id FROM orgs ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0] != 1 || got[1] != 3 {
			t.Errorf("orgs = %v, want [1 3]", got)
		}
	})
}

func TestDeeplyNestedTx(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
			return row.InTx(ctx, tx, func(ctx context.Context, tx *row.Tx) error {
				return row.InTx(ctx, tx, func(ctx context.Context, tx *row.Tx) error {
					if tx.Depth() != 2 {
						t.Errorf("depth = %d, want 2", tx.Depth())
					}
					return insertOrg(ctx, tx, 1, "deep")
				})
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := count(t, db, "orgs"); n != 1 {
			t.Errorf("orgs = %d", n)
		}
	})
}

func TestNestedTxRejectsTxOptions(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
			return row.InTx(ctx, tx, func(context.Context, *row.Tx) error { return nil }, row.ReadOnly())
		})
		if err == nil {
			t.Fatal("options on a nested transaction should be rejected rather than ignored")
		}
	})
}

func TestReadOnlyTx(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 1)

		err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
			n, err := row.One[int64](ctx, tx, `SELECT count(*) FROM users`)
			if err != nil {
				return err
			}
			if n != 1 {
				t.Errorf("count = %d", n)
			}
			return nil
		}, row.ReadOnly())
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestTxSeesItsOwnWrites(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		err := row.InTx(ctx, db, func(ctx context.Context, tx *row.Tx) error {
			if err := insertOrg(ctx, tx, 1, "acme"); err != nil {
				return err
			}
			name, err := row.One[string](ctx, tx, `SELECT name FROM orgs WHERE id = :id`, row.Args{"id": 1})
			if err != nil {
				return err
			}
			if name != "acme" {
				t.Errorf("name = %q", name)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestConnSession(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 2)

		cn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cn.Close()

		n, err := row.One[int64](ctx, cn, `SELECT count(*) FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("count = %d", n)
		}

		// A transaction can be started from a reserved connection too.
		if err := row.InTx(ctx, cn, func(ctx context.Context, tx *row.Tx) error {
			return insertOrg(ctx, tx, 1, "acme")
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInTxRetryStopsOnNonRetryableError(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		attempts := 0
		err := row.InTxRetry(ctx, db, row.DefaultRetryPolicy, func(ctx context.Context, tx *row.Tx) error {
			attempts++
			// A unique violation is a bug, not contention: retrying it would
			// just fail again.
			if err := insertOrg(ctx, tx, 1, "acme"); err != nil {
				return err
			}
			return insertOrg(ctx, tx, 1, "acme")
		})
		if err == nil {
			t.Fatal("expected the unique violation to surface")
		}
		if attempts != 1 {
			t.Errorf("attempted %d times, want 1: a unique violation is not retryable", attempts)
		}
		if !row.IsCode(err, row.UniqueViolation) {
			t.Errorf("code = %v, want UniqueViolation", row.CodeOf(err))
		}
	})
}
