package integration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/r-52/row"
	"github.com/r-52/row/internal/dbtest"
	"github.com/r-52/row/qb"
)

// These benchmarks measure what row costs over writing the scan loop by hand.
// The hand-written variants below are what a caller would otherwise type, so
// the difference between the two is exactly row's overhead.

const benchQuery = `SELECT id, name, email, age, score, active, data, bio, org_id, created_at FROM users ORDER BY id`

func seedBench(tb testing.TB, db *row.DB, n int) {
	ctx := context.Background()
	users := make([]WUser, 0, n)
	for i := 1; i <= n; i++ {
		users = append(users, newWUser(int64(i), "user"+itoa(i)))
	}
	if err := row.InsertMany(ctx, db, "users", users); err != nil {
		tb.Fatal(err)
	}
}

// scanByHand is the code row replaces: an explicit Scan for every column.
func scanByHand(ctx context.Context, db *sql.DB) ([]WUser, error) {
	rows, err := db.QueryContext(ctx, benchQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WUser
	for rows.Next() {
		var u WUser
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Score,
			&u.Active, &u.Data, &u.Bio, &u.OrgID, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func BenchmarkScan(b *testing.B) {
	for _, n := range []int{1, 100, 1000} {
		db := dbtest.OpenSQLiteB(b)
		seedBench(b, db, n)
		ctx := context.Background()

		b.Run("row/"+itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				got, err := row.All[WUser](ctx, db, benchQuery)
				if err != nil {
					b.Fatal(err)
				}
				if len(got) != n {
					b.Fatalf("got %d rows", len(got))
				}
			}
		})

		b.Run("handwritten/"+itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				got, err := scanByHand(ctx, db.SQL())
				if err != nil {
					b.Fatal(err)
				}
				if len(got) != n {
					b.Fatalf("got %d rows", len(got))
				}
			}
		})
	}
}

func BenchmarkNamedBinding(b *testing.B) {
	db := dbtest.OpenSQLiteB(b)
	seedBench(b, db, 100)
	ctx := context.Background()

	b.Run("named", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := row.All[string](ctx, db,
				`SELECT name FROM users WHERE age > :age AND id IN (:ids)`,
				row.Args{"age": 1, "ids": []int{1, 2, 3}}); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("positional", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := row.All[string](ctx, db,
				`SELECT name FROM users WHERE age > ? AND id IN (?,?,?)`,
				1, 1, 2, 3); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("builder", func(b *testing.B) {
		q := qb.Select("name").From("users").
			Where(qb.Gt{"age": 1}, qb.In("id", 1, 2, 3))
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := row.AllOf[string](ctx, db, q); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkInsert(b *testing.B) {
	ctx := context.Background()

	b.Run("Insert", func(b *testing.B) {
		db := dbtest.OpenSQLiteB(b)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			u := newWUser(int64(i+1), "u"+itoa(i))
			if err := row.Insert(ctx, db, "users", &u); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("InsertMany/1000", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			db := dbtest.OpenSQLiteB(b)
			users := make([]WUser, 0, 1000)
			for j := 1; j <= 1000; j++ {
				users = append(users, newWUser(int64(j), "u"+itoa(j)))
			}
			b.StartTimer()
			if err := row.InsertMany(ctx, db, "users", users); err != nil {
				b.Fatal(err)
			}
		}
	})
}
