package main

import (
	"context"
	"log"
	"time"
)

// pointsEvery is how often the brain reads the pipe's word on the points:
// the pipe says it once a second, after its tick.
const pointsEvery = time.Second

// pointsStale is how old the pipe's word may be to count: an older one is a
// pipe (or a brain) not run, and says nothing of the points.
const pointsStale = 2500 * time.Millisecond

// sayPoints says how the border points stand (points.go): a change at once,
// the minute once a minute; nothing while no points are given.
func (b *brain) sayPoints(ctx context.Context) {
	book := newPointBook()
	t := time.NewTicker(pointsEvery)
	defer t.Stop()
	from := time.Now().Truncate(time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if m := now.Truncate(time.Minute); m.After(from) {
				if line := book.minute(from); line != "" {
					log.Printf("nekko-trunk-entry: %s", line)
				}
				from = m
			}
			ps, at := b.pipe.Points()
			if at.IsZero() || now.Sub(at) > pointsStale {
				// No word: no points given (the pipe says none then), or a
				// pipe not run. Either way the book knows nothing now.
				ps = nil
			}
			for _, l := range book.step(now, ps) {
				log.Printf("nekko-trunk-entry: %s", l)
			}
		}
	}
}
