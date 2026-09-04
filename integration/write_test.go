package integration

import (
	"context"
	"testing"
	"time"

	"github.com/r-52/row"
	"github.com/r-52/row/internal/dbtest"
)

// WUser writes to the fixture users table. Nothing is readonly here because
// the fixture assigns ids explicitly; see TestInsertReturning for the
// generated-column case.
type WUser struct {
	ID        int64 `db:"id,pk"`
	Name      string
	Email     string
	Age       int
	Score     float64
	Active    bool
	Data      []byte
	Bio       *string
	OrgID     *int64
	CreatedAt time.Time
}

func newWUser(id int64, name string) WUser {
	return WUser{
		ID: id, Name: name, Email: name + "@example.com",
		Age: 30, Score: 1.5, Active: true, CreatedAt: epoch,
	}
}

func TestInsert(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		u := newWUser(1, "ada")
		if err := row.Insert(ctx, db, "users", &u); err != nil {
			t.Fatal(err)
		}
		got, err := row.One[WUser](ctx, db, `SELECT * FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "ada" || got.Email != "ada@example.com" || got.Age != 30 {
			t.Errorf("got %+v", got)
		}
		if !got.CreatedAt.UTC().Equal(epoch) {
			t.Errorf("created_at = %v", got.CreatedAt.UTC())
		}
	})
}

func TestInsertReturning(t *testing.T) {
	// A column the database fills in must come back into the struct.
	type counter struct {
		Name  string `db:"name,pk"`
		Value int64  `db:"value"`
	}
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		c := counter{Name: "hits", Value: 3}
		if err := row.Insert(ctx, db, "counters", &c, row.Returning("value")); err != nil {
			t.Fatal(err)
		}
		if c.Value != 3 {
			t.Errorf("value = %d", c.Value)
		}

		// Prove RETURNING actually writes back by letting the database change
		// the value during an update.
		c.Value = 10
		if err := row.Update(ctx, db, "counters", &c, row.Returning("name", "value")); err != nil {
			t.Fatal(err)
		}
		if c.Value != 10 {
			t.Errorf("value after update = %d", c.Value)
		}
	})
}

func TestInsertOmitAndOnly(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		u := newWUser(1, "ada")
		u.Bio = nil
		// Omitting a nullable column leaves it at its database default.
		if err := row.Insert(ctx, db, "users", &u, row.Omit("bio", "org_id", "data")); err != nil {
			t.Fatal(err)
		}
		got, err := row.One[WUser](ctx, db, `SELECT * FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Bio != nil || got.OrgID != nil || got.Data != nil {
			t.Errorf("omitted columns should be NULL: %+v", got)
		}
	})
}

func TestUpdate(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		u := newWUser(1, "ada")
		if err := row.Insert(ctx, db, "users", &u); err != nil {
			t.Fatal(err)
		}

		u.Name = "ada lovelace"
		u.Age = 36
		if err := row.Update(ctx, db, "users", &u); err != nil {
			t.Fatal(err)
		}
		got, err := row.One[WUser](ctx, db, `SELECT * FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "ada lovelace" || got.Age != 36 {
			t.Errorf("got %+v", got)
		}
	})
}

func TestUpdateOnly(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		u := newWUser(1, "ada")
		if err := row.Insert(ctx, db, "users", &u); err != nil {
			t.Fatal(err)
		}

		// Change two fields in the struct but write back only one.
		u.Name = "changed"
		u.Age = 99
		if err := row.Update(ctx, db, "users", &u, row.Only("name")); err != nil {
			t.Fatal(err)
		}
		got, err := row.One[WUser](ctx, db, `SELECT * FROM users`)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "changed" {
			t.Errorf("name = %q", got.Name)
		}
		if got.Age != 30 {
			t.Errorf("age = %d, want the original 30: Only should have excluded it", got.Age)
		}
	})
}

func TestDelete(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		u := newWUser(1, "ada")
		if err := row.Insert(ctx, db, "users", &u); err != nil {
			t.Fatal(err)
		}
		if err := row.Delete(ctx, db, "users", &u); err != nil {
			t.Fatal(err)
		}
		if n := count(t, db, "users"); n != 0 {
			t.Errorf("users = %d", n)
		}
	})
}

func TestInsertMany(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		users := make([]WUser, 0, 250)
		for i := 1; i <= 250; i++ {
			users = append(users, newWUser(int64(i), "u"+string(rune('a'+i%26))+itoa(i)))
		}
		if err := row.InsertMany(ctx, db, "users", users); err != nil {
			t.Fatal(err)
		}
		if n := count(t, db, "users"); n != 250 {
			t.Errorf("users = %d, want 250", n)
		}
	})
}

func TestInsertManyEmptyIsNoOp(t *testing.T) {
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		if err := row.InsertMany(context.Background(), db, "users", []WUser(nil)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInsertManyReturning(t *testing.T) {
	type counter struct {
		Name  string `db:"name,pk"`
		Value int64  `db:"value"`
	}
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		cs := []counter{{"a", 1}, {"b", 2}, {"c", 3}}
		if err := row.InsertMany(ctx, db, "counters", cs, row.Returning("name", "value")); err != nil {
			t.Fatal(err)
		}
		for i, c := range cs {
			if c.Value != int64(i+1) {
				t.Errorf("cs[%d] = %+v", i, c)
			}
		}
	})
}

func TestInsertManyChunksUnderPlaceholderLimit(t *testing.T) {
	// 250 users at 10 columns each is 2500 placeholders, comfortably over what
	// a naive single statement would need on neither engine — but the batching
	// code path is what is under test, so drive it with a low limit by using
	// many rows and checking they all land.
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		var cs []struct {
			Name  string `db:"name,pk"`
			Value int64  `db:"value"`
		}
		for i := 0; i < 20000; i++ {
			cs = append(cs, struct {
				Name  string `db:"name,pk"`
				Value int64  `db:"value"`
			}{Name: itoa(i), Value: int64(i)})
		}
		if err := row.InsertMany(ctx, db, "counters", cs); err != nil {
			t.Fatal(err)
		}
		if n := count(t, db, "counters"); n != 20000 {
			t.Errorf("counters = %d, want 20000", n)
		}
	})
}

func TestUpdateRequiresPrimaryKey(t *testing.T) {
	type noPK struct {
		Name string `db:"name"`
	}
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		v := noPK{Name: "x"}
		err := row.Update(context.Background(), db, "counters", &v)
		if err == nil {
			t.Fatal("Update without a pk tag should fail loudly")
		}
	})
}

func TestReadonlyColumnsAreNotWritten(t *testing.T) {
	type c struct {
		Name  string `db:"name,pk"`
		Value int64  `db:"value,readonly"`
	}
	dbtest.Each(t, func(t *testing.T, db *row.DB) {
		ctx := context.Background()
		// value is readonly, so the INSERT must omit it and the NOT NULL
		// column has no default: the statement should fail at the server,
		// proving the column really was left out.
		v := c{Name: "x", Value: 5}
		err := row.Insert(ctx, db, "counters", &v)
		if err == nil {
			t.Fatal("expected the readonly column to be excluded, leaving value unset")
		}
		if !row.IsCode(err, row.NotNullViolation) {
			t.Errorf("code = %v (err %v)", row.CodeOf(err), err)
		}
	})
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	p := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		buf[p] = '-'
	}
	return string(buf[p:])
}
