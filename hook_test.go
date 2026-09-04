package row

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder captures what the hook interface is handed.
type recorder struct {
	mu     sync.Mutex
	before []string
	after  []QueryInfo
	tag    string
}

type ctxKey string

func (r *recorder) BeforeQuery(ctx context.Context, info *QueryInfo) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.before = append(r.before, r.tag)
	return context.WithValue(ctx, ctxKey(r.tag), true)
}

func (r *recorder) AfterQuery(ctx context.Context, info *QueryInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Copy: the caller reuses the struct.
	r.after = append(r.after, *info)
	if ctx.Value(ctxKey(r.tag)) != true {
		panic("AfterQuery lost the context BeforeQuery returned")
	}
}

func TestSlogHookLevels(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := SlogHook(log, slog.LevelInfo, 50*time.Millisecond)

	ctx := context.Background()

	// A fast, successful statement logs at the configured level.
	h.AfterQuery(ctx, &QueryInfo{Op: "row.All", SQL: "SELECT 1", Duration: time.Millisecond, RowsAffected: -1})
	if got := buf.String(); !strings.Contains(got, "level=INFO") || !strings.Contains(got, "SELECT 1") {
		t.Errorf("fast query log = %q", got)
	}

	// A slow one is promoted to warn.
	buf.Reset()
	h.AfterQuery(ctx, &QueryInfo{Op: "row.All", SQL: "SELECT 1", Duration: time.Second, RowsAffected: -1})
	if got := buf.String(); !strings.Contains(got, "level=WARN") {
		t.Errorf("slow query should warn: %q", got)
	}

	// A failure is an error regardless of duration.
	buf.Reset()
	h.AfterQuery(ctx, &QueryInfo{Op: "row.Exec", SQL: "BAD", Err: errorString("boom"), RowsAffected: -1})
	if got := buf.String(); !strings.Contains(got, "level=ERROR") || !strings.Contains(got, "boom") {
		t.Errorf("failed query log = %q", got)
	}
}

func TestSlogHookCollapsesStatements(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	SlogHook(log, slog.LevelInfo, 0).AfterQuery(context.Background(),
		&QueryInfo{Op: "row.All", SQL: "SELECT *\n  FROM t", Duration: time.Millisecond, RowsAffected: -1})
	if got := buf.String(); !strings.Contains(got, "SELECT * FROM t") {
		t.Errorf("log = %q", got)
	}
}

func TestSlogHookOmitsUnknownRowCount(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	SlogHook(log, slog.LevelInfo, 0).AfterQuery(context.Background(),
		&QueryInfo{Op: "row.All", SQL: "SELECT 1", RowsAffected: -1})
	if strings.Contains(buf.String(), "rows=") {
		t.Errorf("an unknown row count should be omitted: %q", buf.String())
	}
	buf.Reset()
	SlogHook(log, slog.LevelInfo, 0).AfterQuery(context.Background(),
		&QueryInfo{Op: "row.Exec", SQL: "DELETE FROM t", RowsAffected: 3})
	if !strings.Contains(buf.String(), "rows=3") {
		t.Errorf("a known row count should be logged: %q", buf.String())
	}
}
