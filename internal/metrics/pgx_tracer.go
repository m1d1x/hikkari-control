package metrics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// pgxStartKey scopes the per-query start time inside a context. Using a
// dedicated key (rather than the Timings accumulator) keeps the tracer
// usable on connections that bypass the HTTP middleware.
type pgxStartKey struct{}

// PGXTracer is a pgx v5 QueryTracer / BatchTracer that adds elapsed time
// for every executed query to the request-bound Timings accumulator.
//
// It implements pgx.QueryTracer and pgx.BatchTracer. Connect plugs it in
// via pgxpool.Config.ConnConfig.Tracer.
type PGXTracer struct{}

// Compile-time interface checks.
var (
	_ pgx.QueryTracer = (*PGXTracer)(nil)
	_ pgx.BatchTracer = (*PGXTracer)(nil)
)

// TraceQueryStart stamps the start time onto the context. pgx threads the
// returned context through to TraceQueryEnd.
func (PGXTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, pgxStartKey{}, time.Now())
}

// TraceQueryEnd folds the elapsed time into the request Timings accumulator
// under the "db" label. If no accumulator is attached (e.g. background
// jobs, migrations), this is a cheap no-op.
func (PGXTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	start, ok := ctx.Value(pgxStartKey{}).(time.Time)
	if !ok {
		return
	}
	Add(ctx, "db", time.Since(start))
}

// TraceBatchStart stamps the batch start time.
func (PGXTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return context.WithValue(ctx, pgxStartKey{}, time.Now())
}

// TraceBatchQuery is invoked per query inside a batch. Per-query timing
// inside a batch is not exposed by pgx, so we leave this as a no-op and
// rely on TraceBatchEnd for the aggregate.
func (PGXTracer) TraceBatchQuery(_ context.Context, _ *pgx.Conn, _ pgx.TraceBatchQueryData) {
}

// TraceBatchEnd records the total batch duration under "db".
func (PGXTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchEndData) {
	start, ok := ctx.Value(pgxStartKey{}).(time.Time)
	if !ok {
		return
	}
	Add(ctx, "db", time.Since(start))
}
