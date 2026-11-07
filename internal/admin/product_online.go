package admin

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/m1d1x/hikkari-control/internal/support"
)

// How many installs are on the line is a number of this minute, not of a
// window, so it is sampled: once a minute the hub counts the installs whose
// last signal is inside support.TesterOnlineWindow and writes the count down.
// The chart is read back from those rows, a gap where the hub itself was down.
const (
	onlineSampleEvery = time.Minute
	onlineRetention   = 35 * 24 * time.Hour
)

// metricsOnline is the "online now" figure and its history by bucket.
type metricsOnline struct {
	// Now is the count at the latest sample; null before the first one.
	Now *int `json:"now"`
	// Series is the busiest minute of each bucket; null where nothing was sampled.
	Series []*int `json:"series"`
}

// RunOnlineSampler records the online count every minute until ctx ends.
func (h *Handlers) RunOnlineSampler(ctx context.Context) {
	tick := time.NewTicker(onlineSampleEvery)
	defer tick.Stop()
	purged := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-tick.C:
			if err := h.sampleOnline(ctx, t); err != nil {
				log.Printf("admin: online sample: %v", err)
			}
			if t.Sub(purged) >= time.Hour {
				if _, err := h.pool.Exec(ctx, `DELETE FROM product_online_samples WHERE ts < $1`, t.Add(-onlineRetention)); err != nil {
					log.Printf("admin: purge online samples: %v", err)
				}
				purged = t
			}
		}
	}
}

func (h *Handlers) sampleOnline(ctx context.Context, t time.Time) error {
	ts := t.UTC().Truncate(onlineSampleEvery)
	_, err := h.pool.Exec(ctx, `
		INSERT INTO product_online_samples (ts, online)
		SELECT $1, count(*) FROM support_install_aliases WHERE last_seen_at > $2
		ON CONFLICT (ts) DO NOTHING`, ts, ts.Add(-support.TesterOnlineWindow))
	if err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}

// onlineHistory reads the samples of a window into its buckets.
func (h *Handlers) onlineHistory(ctx context.Context, win productWindow, since, end time.Time) (metricsOnline, error) {
	out := metricsOnline{Series: make([]*int, win.N)}
	rows, err := h.pool.Query(ctx, `
		SELECT to_timestamp(floor(extract(epoch FROM ts) / $3) * $3), max(online)
		FROM product_online_samples
		WHERE ts >= $1 AND ts < $2
		GROUP BY 1`, since, end, win.Step.Seconds())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var b time.Time
		var n int
		if err := rows.Scan(&b, &n); err != nil {
			return out, err
		}
		if i := win.index(since, b.UTC()); i >= 0 {
			v := n
			out.Series[i] = &v
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	var now int
	if err := h.pool.QueryRow(ctx, `SELECT online FROM product_online_samples ORDER BY ts DESC LIMIT 1`).Scan(&now); err == nil {
		out.Now = &now
	}
	return out, nil
}
