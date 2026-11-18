package main

import (
	"context"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/support"
)

// chunkArchive reads the testers' log chunks the archive holds as the support
// package's chunks.
type chunkArchive struct{ store *analytics.Store }

func (a chunkArchive) ReadArchivedChunks(ctx context.Context, installID string, since, until time.Time, limit int) ([]support.TesterChunk, error) {
	rows, err := a.store.ReadArchivedChunks(ctx, installID, since, until, limit)
	if err != nil {
		return nil, err
	}
	out := make([]support.TesterChunk, len(rows))
	for i, r := range rows {
		out[i] = support.TesterChunk{
			ID: r.ID, Platform: r.Platform, Version: r.Version,
			Lines: int(r.Lines), Dropped: int(r.Dropped), Body: r.Body, ReceivedAt: r.ReceivedAt,
		}
	}
	return out, nil
}
