package integration

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/r-52/row"
	"github.com/r-52/row/internal/dbtest"
)

type capture struct {
	mu     sync.Mutex
	order  []string
	infos  []row.QueryInfo
	label  string
	shared *[]string
}

type key string

func (c *capture) BeforeQuery(ctx context.Context, info *row.QueryInfo) context.Context {
	c.mu.Lock()
	*c.shared = append(*c.shared, "before:"+c.label)
	c.mu.Unlock()
	return context.WithValue(ctx, key(c.label), true)
}

func (c *capture) AfterQuery(ctx context.Context, info *row.QueryInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.shared = append(*c.shared, "after:"+c.label)
	c.infos = append(c.infos, *info)
	if ctx.Value(key(c.label)) != true {
		c.order = append(c.order, "LOST CONTEXT")
	}
}

func TestHooksObserveRealQueries(t *testing.T) {
	var shared []string
	outer := &capture{label: "outer", shared: &shared}
	inner := &capture{label: "inner", shared: &shared}

	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		seedUsers(t, db, 2)

		// Discard everything the seeding produced; the assertions below are
		// about the single query that follows.
		shared = nil
		outer.infos, inner.infos = nil, nil

		n, err := row.One[int64](ctx, db, `SELECT count(*) FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("count = %d", n)
		}

		// Hooks run outward-in before and inward-out after, so a hook that
		// opens a span in Before can close it in After.
		want := []string{"before:outer", "before:inner", "after:inner", "after:outer"}
		if len(shared) != len(want) {
			t.Fatalf("hook calls = %v, want %v", shared, want)
		}
		for i := range want {
			if shared[i] != want[i] {
				t.Fatalf("hook calls = %v, want %v", shared, want)
			}
		}

		if len(outer.infos) != 1 {
			t.Fatalf("outer saw %d queries", len(outer.infos))
		}
		info := outer.infos[0]
		if info.Op != "row.One" {
			t.Errorf("op = %q", info.Op)
		}
		if !strings.Contains(info.SQL, "count(*)") {
			t.Errorf("sql = %q", info.SQL)
		}
		if info.Duration <= 0 {
			t.Errorf("duration = %v, want a positive measurement", info.Duration)
		}
		if info.Err != nil {
			t.Errorf("err = %v", info.Err)
		}
		if info.Started.IsZero() {
			t.Errorf("start time was not recorded")
		}
	}, row.WithHook(outer), row.WithHook(inner))
}

func TestHooksSeeFailuresAndRowCounts(t *testing.T) {
	var shared []string
	c := &capture{label: "c", shared: &shared}

	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		c.infos = nil

		seedUsers(t, db, 3)
		if _, err := row.Exec(ctx, db, `DELETE FROM users WHERE id > :id`, row.Args{"id": 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := row.Exec(ctx, db, `SELECT * FROM nope`); err == nil {
			t.Fatal("expected a failure")
		}

		var sawDelete, sawFailure bool
		for _, info := range c.infos {
			if strings.Contains(info.SQL, "DELETE") {
				sawDelete = true
				if info.RowsAffected != 2 {
					t.Errorf("DELETE affected %d, want 2", info.RowsAffected)
				}
			}
			if info.Err != nil {
				sawFailure = true
			}
		}
		if !sawDelete {
			t.Error("the DELETE was not observed")
		}
		if !sawFailure {
			t.Error("the failing statement was not observed with its error")
		}
	}, row.WithHook(c))
}
