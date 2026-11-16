package downtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

// An ordinary user's minutes come as the telemetry event "tunnel"
// (hikkari-client docs/TELEMETRY.md §4a): a period of up to ten minutes folded
// into counts, under the random install of the user's telemetry. They are kept
// in product_user_tunnel_periods, apart from the testers' minutes, and drawn
// as averages over people: nobody here has a name or a line of their own.

// InsertTunnelPeriods writes periods; one already stored takes the newer
// numbers, so a batch sent twice leaves one row.
func (s *Store) InsertTunnelPeriods(ctx context.Context, periods []telemetry.TunnelPeriod) error {
	if len(periods) == 0 {
		return nil
	}
	// One period per install and minute: Postgres refuses to update a row
	// twice in one statement.
	last := map[string]int{}
	for i, p := range periods {
		last[p.Install+"\x00"+p.At.UTC().Format(time.RFC3339)] = i
	}
	n := len(last)
	installs := make([]string, 0, n)
	ats := make([]time.Time, 0, n)
	cols := make([][]int64, 12)
	for i, p := range periods {
		if last[p.Install+"\x00"+p.At.UTC().Format(time.RFC3339)] != i {
			continue
		}
		installs = append(installs, p.Install)
		ats = append(ats, p.At.UTC())
		for j, v := range []int64{
			int64(p.Flowing), int64(p.Idle), int64(p.Down), int64(p.CoreDown), int64(p.NetDown), int64(p.StallMinutes),
			int64(p.N300), p.MS300, int64(p.N1000), p.MS1000, int64(p.N3000), p.MS3000,
		} {
			cols[j] = append(cols[j], v)
		}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO product_user_tunnel_periods
			(install_id, period, o, i, d, c, n, stall_minutes, n300, ms300, n1000, ms1000, n3000, ms3000)
		SELECT * FROM unnest($1::text[], $2::timestamptz[], $3::int[], $4::int[], $5::int[], $6::int[], $7::int[],
			$8::int[], $9::int[], $10::bigint[], $11::int[], $12::bigint[], $13::int[], $14::bigint[])
		ON CONFLICT (install_id, period) DO UPDATE SET
			o = EXCLUDED.o, i = EXCLUDED.i, d = EXCLUDED.d, c = EXCLUDED.c, n = EXCLUDED.n,
			stall_minutes = EXCLUDED.stall_minutes,
			n300 = EXCLUDED.n300, ms300 = EXCLUDED.ms300, n1000 = EXCLUDED.n1000, ms1000 = EXCLUDED.ms1000,
			n3000 = EXCLUDED.n3000, ms3000 = EXCLUDED.ms3000`,
		installs, ats, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6], cols[7], cols[8], cols[9], cols[10], cols[11])
	if err != nil {
		return fmt.Errorf("insert user tunnel periods: %w", err)
	}
	return nil
}

// UserCount is one install's periods of one bucket, added up.
type UserCount struct {
	Install string
	Bucket  time.Time
	// On: minutes the tunnel was on, any state. Lost: d and c.
	On, Lost int64
	// StallMinutes: minutes the core timed stalls in.
	StallMinutes int64
	// MS300, MS1000: the length of the stalls of at least 300 ms and 1 s.
	MS300, MS1000 int64
}

// UserCounts reads the periods in [since, until) grouped into step buckets by
// their first minute. Installs that are testers are left out: their minutes
// are on the testers' screen, and here they would be counted twice over.
func (s *Store) UserCounts(ctx context.Context, since, until time.Time, step time.Duration) ([]UserCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.install_id,
		       to_timestamp(floor(extract(epoch FROM p.period) / $3) * $3) AS bucket,
		       sum(p.o + p.i + p.d + p.c + p.n), sum(p.d + p.c),
		       sum(p.stall_minutes), sum(p.ms300), sum(p.ms1000)
		FROM product_user_tunnel_periods p
		WHERE p.period >= $1 AND p.period < $2
		  AND NOT EXISTS (SELECT 1 FROM support_install_aliases a WHERE a.install_id = p.install_id)
		GROUP BY 1, 2`,
		since.UTC(), until.UTC(), int64(step/time.Second))
	if err != nil {
		return nil, fmt.Errorf("read user tunnel periods: %w", err)
	}
	defer rows.Close()
	var out []UserCount
	for rows.Next() {
		var c UserCount
		if err := rows.Scan(&c.Install, &c.Bucket, &c.On, &c.Lost, &c.StallMinutes, &c.MS300, &c.MS1000); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UserChart is the product overview's lines for everyone who is not a tester.
// Every line has a value per bucket, null where nobody's tunnel was on (or,
// for the stalls, nobody's core timed them).
type UserChart struct {
	Window        string   `json:"window"`
	BucketSeconds int64    `json:"bucketSeconds"`
	Buckets       []string `json:"buckets"`
	// People: installs whose tunnel was on in the bucket, the ones the
	// averages are over.
	People []*float64 `json:"people"`
	// Lost: minutes the tunnel was on and carried nothing, per person.
	Lost []*float64 `json:"lost"`
	// StallMS: ms of the stalls of at least StallShortMS, per person whose
	// core timed them. StallLongMS: the same for StallLongBoundMS.
	StallMS     []*float64 `json:"stallMs"`
	StallLongMS []*float64 `json:"stallLongMs"`
}

// BuildUserChart reads the window into the averages.
func BuildUserChart(ctx context.Context, s *Store, name string, now time.Time) (UserChart, error) {
	win, span, step, err := window(name)
	if err != nil {
		return UserChart{}, err
	}
	until := now.UTC().Truncate(step).Add(step)
	since := until.Add(-span)
	counts, err := s.UserCounts(ctx, since, until, step)
	if err != nil {
		return UserChart{}, err
	}
	return assembleUsers(win, since, until, step, counts), nil
}

func assembleUsers(win string, since, until time.Time, step time.Duration, counts []UserCount) UserChart {
	c := UserChart{Window: win, BucketSeconds: int64(step / time.Second), Buckets: []string{}}
	index := map[int64]int{}
	for b := since; b.Before(until); b = b.Add(step) {
		index[b.Unix()] = len(c.Buckets)
		c.Buckets = append(c.Buckets, b.Format(time.RFC3339))
	}
	n := len(c.Buckets)
	people, timed := make([]int, n), make([]int, n)
	lost, short, long := make([]float64, n), make([]float64, n), make([]float64, n)
	for _, k := range counts {
		i, ok := index[k.Bucket.Unix()]
		if !ok || k.On <= 0 {
			continue
		}
		people[i]++
		lost[i] += float64(k.Lost)
		if k.StallMinutes > 0 {
			timed[i]++
			short[i] += float64(k.MS300)
			long[i] += float64(k.MS1000)
		}
	}
	c.People, c.Lost = make([]*float64, n), make([]*float64, n)
	c.StallMS, c.StallLongMS = make([]*float64, n), make([]*float64, n)
	for i := 0; i < n; i++ {
		if people[i] > 0 {
			p, l := float64(people[i]), lost[i]/float64(people[i])
			c.People[i], c.Lost[i] = &p, &l
		}
		if timed[i] > 0 {
			s, l := short[i]/float64(timed[i]), long[i]/float64(timed[i])
			c.StallMS[i], c.StallLongMS[i] = &s, &l
		}
	}
	return c
}

// registerUserRoutes mounts GET /api/v1/admin/product/downtime-users?window=7d.
func registerUserRoutes(mux *http.ServeMux, s *Store, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/product/downtime-users", auth(func(w http.ResponseWriter, r *http.Request) {
		c, err := BuildUserChart(r.Context(), s, r.URL.Query().Get("window"), time.Now())
		if err != nil {
			if errors.Is(err, errWindow) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			applog.Error(r.Context(), "downtime", "user chart failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, c)
	}))
}
