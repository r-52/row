package row

import (
	"context"
	"log/slog"
	"time"
)

// QueryInfo describes one statement execution. It is passed to hooks before
// and after the statement runs; the fields filled in on the way out are
// Duration, RowsAffected and Err.
type QueryInfo struct {
	// Op is the row operation, e.g. "row.All".
	Op string

	// SQL is the statement as sent to the driver, after binding.
	SQL string

	// Args are the positional arguments as sent to the driver.
	Args []any

	// Started is when the statement was handed to the driver.
	Started time.Time

	// Duration is how long it took. Zero in BeforeQuery.
	Duration time.Duration

	// RowsAffected is set for Exec, and is -1 when the driver does not report
	// it or the operation was a query.
	RowsAffected int64

	// Err is the resulting error, nil on success. Always nil in BeforeQuery.
	Err error
}

// Hook observes statement execution. Implementations must be safe for
// concurrent use and should not block.
type Hook interface {
	// BeforeQuery runs immediately before the statement is executed. The
	// returned context replaces the one used for the call, which lets a hook
	// attach a tracing span.
	BeforeQuery(ctx context.Context, info *QueryInfo) context.Context

	// AfterQuery runs once the statement has completed.
	AfterQuery(ctx context.Context, info *QueryInfo)
}

// slogHook logs every statement. It is the built-in Hook, and depends on
// nothing outside the standard library.
type slogHook struct {
	log   *slog.Logger
	level slog.Level
	slow  time.Duration
}

// SlogHook returns a Hook that logs each statement to log at the given level.
//
// Statements taking longer than slow are logged at warn level instead; pass a
// zero slow to disable that promotion.
func SlogHook(log *slog.Logger, level slog.Level, slow time.Duration) Hook {
	return &slogHook{log: log, level: level, slow: slow}
}

func (h *slogHook) BeforeQuery(ctx context.Context, _ *QueryInfo) context.Context {
	return ctx
}

func (h *slogHook) AfterQuery(ctx context.Context, info *QueryInfo) {
	lvl := h.level
	switch {
	case info.Err != nil:
		lvl = slog.LevelError
	case h.slow > 0 && info.Duration >= h.slow:
		lvl = slog.LevelWarn
	}
	if !h.log.Enabled(ctx, lvl) {
		return
	}
	attrs := []any{
		slog.String("op", info.Op),
		slog.String("sql", collapseSpace(info.SQL)),
		slog.Duration("dur", info.Duration),
	}
	if info.RowsAffected >= 0 {
		attrs = append(attrs, slog.Int64("rows", info.RowsAffected))
	}
	if info.Err != nil {
		attrs = append(attrs, slog.String("err", info.Err.Error()))
	}
	h.log.Log(ctx, lvl, "sql", attrs...)
}
