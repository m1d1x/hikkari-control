package admin

// entry_fleet.go answers the console's fleet screen (plan 42 К1, П9.3):
// one request for the whole of it — the pool size and health, replacements
// keeping up with deaths, rollouts in flight, the providers compared, and the
// fleet of machines the screen draws as a tree: provider → machine → relay →
// domain or dest.
//
// Computed entirely from Postgres (roadmap 42 К1 §2).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// entryFleet is the full payload for the fleet screen.
type entryFleet struct {
	Window string `json:"window"` // "24h" | "7d" | "30d"
	// BucketSeconds is the width of one point of the pool and strip series.
	BucketSeconds int `json:"bucketSeconds"`

	Pool fleetPool `json:"pool"`

	Strip fleetStrip `json:"strip"`

	Jobs []fleetJob `json:"jobs"`

	Boxes []fleetBox `json:"boxes"`

	// Providers is the provider comparison (entry_providers.go), carried here
	// so the screen is one request: the tree's top level is the provider, and
	// the economics card reads the same rows.
	Providers entryProviderCompare `json:"providers"`

	// Folded summary counts
	CleanBoxesCount   int `json:"cleanBoxesCount"`
	CleanRelaysCount  int `json:"cleanRelaysCount"`
	RetiredBoxesCount int `json:"retiredBoxesCount"`

	Missing []missingFigure `json:"missing"`
}

type fleetPool struct {
	// InDistribution is the relays the pool hands out now; PendingCount and
	// RollingCount the ones on their way in and the ones a rollout holds.
	InDistribution int `json:"inDistribution"`
	BoxesCount     int `json:"boxesCount"`
	PendingCount   int `json:"pendingCount"`
	RollingCount   int `json:"rollingCount"`
	// Series is the pool's size at the end of every bucket of the window
	// (seriesBucket), read off the rows' own timestamps.
	Series []fleetPoolPoint `json:"series"`
}

// fleetPoolPoint is the pool as it stood at the end of one bucket.
type fleetPoolPoint struct {
	Bucket string `json:"bucket"`
	// Size counts the relays that existed and had not died by then — what the
	// hub held, pending ones included, which is the only reading the rows
	// carry backwards: a relay records when it was made and when it died, not
	// every status it passed through.
	Size  int `json:"size"`
	Boxes int `json:"boxes"`
}

type fleetStrip struct {
	DeathsVsReplacements fleetDeathReplacement `json:"deathsVsReplacements"`
	TimeToReplacement    fleetTTR              `json:"timeToReplacement"`
}

// fleetDeathReplacement sets what the censor burned against what the hub
// signed into the pool. A death here is a censor death (entry.CensorDeaths):
// a machine handed back on purpose loses its relays too, but that is no block,
// and the screen calls this column «блокировки».
type fleetDeathReplacement struct {
	DeathsDay       int              `json:"deathsDay"`
	ReplacementsDay int              `json:"replacementsDay"`
	Series          []deathRepBucket `json:"series"`
}

type deathRepBucket struct {
	Bucket       string `json:"bucket"`
	Deaths       int    `json:"deaths"`
	Replacements int    `json:"replacements"`
}

type fleetTTR struct {
	MedianSec   *float64 `json:"medianSec"`
	PeakSec     *float64 `json:"peakSec"`
	SampleCount int      `json:"sampleCount"`
}

type fleetJob struct {
	ID         string     `json:"id"`
	Box        string     `json:"box"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"` // "running", "done", "failed"
	Step       string     `json:"step"`
	Error      *string    `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	CanRetry   bool       `json:"canRetry"`
}

// fleetBox is one machine as the list shows it. No address: the list is what
// anyone glancing at the screen sees, and the address is opened on the
// machine's card (entry_box_card.go), where the owner asks for it.
type fleetBox struct {
	Name           string       `json:"name"`
	Provider       string       `json:"provider"`
	Location       string       `json:"location"`
	Proto          string       `json:"proto"`
	Region         string       `json:"region,omitempty"`
	RentedUntil    *time.Time   `json:"rentedUntil,omitempty"`
	CreatedAt      time.Time    `json:"createdAt"`
	RetiredAt      *time.Time   `json:"retiredAt,omitempty"`
	Domain         string       `json:"domain,omitempty"`
	Arch           string       `json:"arch,omitempty"`
	EdgeSHA        string       `json:"edgeSha,omitempty"`
	OpenIncident   *string      `json:"openIncident,omitempty"`
	Verdict        string       `json:"verdict"`      // "ok", "warn", "bad", "mute"
	VerdictLabel   string       `json:"verdictLabel"` // "чисто", "ip забанен целиком", etc.
	Relays         []fleetRelay `json:"relays"`
	MonthlyCents   *int         `json:"monthlyCents,omitempty"`
	Plan           string       `json:"plan,omitempty"`
	ProviderRegion string       `json:"providerRegion,omitempty"`
}

// fleetRelay is one relay as the park shows it. Port, protocol and cover are
// opened from the relay's sealed identity; a relay whose seal the server key
// cannot open says so in Unreadable and shows none of the three rather than a
// plausible default.
type fleetRelay struct {
	Name       string     `json:"name"`
	Proto      string     `json:"proto"`
	Port       int        `json:"port"`
	Cover      string     `json:"cover"`
	Unreadable bool       `json:"unreadable,omitempty"`
	Grants     int        `json:"grants"`
	SignedAt   *time.Time `json:"signedAt,omitempty"`
	Status     string     `json:"status"`
	Verdict    string     `json:"verdict"`
	Kind       string     `json:"kind"`
	DiedAt     *time.Time `json:"diedAt,omitempty"`
}

// EntryFleet answers GET /api/v1/admin/entry/machines
func (h *Handlers) EntryFleet(w http.ResponseWriter, r *http.Request) {
	window, label, ok := overviewRange(r.URL.Query().Get("window"))
	if !ok {
		http.Error(w, "window is 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	out := entryFleet{
		Window:  label,
		Jobs:    []fleetJob{},
		Boxes:   []fleetBox{},
		Missing: []missingFigure{},
		Providers: entryProviderCompare{
			WindowDays: compareWindow,
			Hosting:    []providerStats{},
			Missing:    []missingFigure{},
		},
	}

	if err := h.fillFleetPool(ctx, &out, window); err != nil {
		log.Printf("admin entry fleet: pool: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	if err := h.fillFleetStrip(ctx, &out, window); err != nil {
		log.Printf("admin entry fleet: strip: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	if err := h.fillFleetJobs(ctx, &out); err != nil {
		log.Printf("admin entry fleet: jobs: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	if err := h.fillFleetBoxes(ctx, &out); err != nil {
		log.Printf("admin entry fleet: boxes: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	if err := h.fillProviderStats(ctx, &out.Providers); err != nil {
		log.Printf("admin entry fleet: providers: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handlers) fillFleetPool(ctx context.Context, out *entryFleet, window time.Duration) error {
	err := h.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'alive'),
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status IN ('testing', 'draining')),
			count(DISTINCT box) FILTER (WHERE status = 'alive')
		FROM entry_relays
	`).Scan(&out.Pool.InDistribution, &out.Pool.PendingCount, &out.Pool.RollingCount, &out.Pool.BoxesCount)
	if err != nil {
		return fmt.Errorf("pool counts: %w", err)
	}

	// One query for the whole series: the end of every bucket, and what
	// existed then. The bucket ends are generated in SQL from the same width
	// the overview uses, so the two screens agree on an axis.
	now := time.Now().UTC()
	bucket := seriesBucket(window)
	out.BucketSeconds = int(bucket.Seconds())
	starts := bucketStarts(now.Add(-window), now, bucket)
	ends := make([]time.Time, len(starts))
	for i, at := range starts {
		ends[i] = minTime(at.Add(bucket), now)
	}
	rows, err := h.pool.Query(ctx, `
		SELECT e,
			(SELECT count(*) FROM entry_relays
			 WHERE created_at <= e AND (died_at IS NULL OR died_at > e)),
			(SELECT count(*) FROM entry_boxes
			 WHERE created_at <= e AND (retired_at IS NULL OR retired_at > e))
		FROM unnest($1::timestamptz[]) AS e
		ORDER BY e
	`, ends)
	if err != nil {
		return fmt.Errorf("pool series: %w", err)
	}
	defer rows.Close()
	out.Pool.Series = make([]fleetPoolPoint, 0, len(starts))
	i := 0
	for rows.Next() {
		var e time.Time
		var p fleetPoolPoint
		if err := rows.Scan(&e, &p.Size, &p.Boxes); err != nil {
			return fmt.Errorf("scan pool series: %w", err)
		}
		p.Bucket = starts[i].Format(time.RFC3339)
		i++
		out.Pool.Series = append(out.Pool.Series, p)
	}
	return rows.Err()
}

func (h *Handlers) fillFleetStrip(ctx context.Context, out *entryFleet, window time.Duration) error {
	// Censor deaths against relays signed into the pool, over the last day
	// and bucket by bucket over the window.
	err := h.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE died_at >= now() - interval '24 hours' AND died_reason = ANY($1)),
			count(*) FILTER (WHERE signed_at >= now() - interval '24 hours')
		FROM entry_relays
	`, entry.CensorDeaths).Scan(&out.Strip.DeathsVsReplacements.DeathsDay, &out.Strip.DeathsVsReplacements.ReplacementsDay)
	if err != nil {
		return fmt.Errorf("deaths and replacements: %w", err)
	}

	now := time.Now().UTC()
	bucket := seriesBucket(window)
	starts := bucketStarts(now.Add(-window), now, bucket)
	deaths := map[time.Time]int{}
	reps := map[time.Time]int{}
	rows, err := h.pool.Query(ctx, `
		SELECT died_at, NULL::timestamptz FROM entry_relays
		WHERE died_at >= $1 AND died_reason = ANY($2)
		UNION ALL
		SELECT NULL::timestamptz, signed_at FROM entry_relays
		WHERE signed_at >= $1
	`, starts[0], entry.CensorDeaths)
	if err != nil {
		return fmt.Errorf("deaths and replacements series: %w", err)
	}
	for rows.Next() {
		var died, signed *time.Time
		if err := rows.Scan(&died, &signed); err != nil {
			rows.Close()
			return fmt.Errorf("scan deaths and replacements: %w", err)
		}
		if died != nil {
			deaths[died.UTC().Truncate(bucket)]++
		}
		if signed != nil {
			reps[signed.UTC().Truncate(bucket)]++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	out.Strip.DeathsVsReplacements.Series = make([]deathRepBucket, 0, len(starts))
	for _, at := range starts {
		out.Strip.DeathsVsReplacements.Series = append(out.Strip.DeathsVsReplacements.Series, deathRepBucket{
			Bucket:       at.Format(time.RFC3339),
			Deaths:       deaths[at],
			Replacements: reps[at],
		})
	}

	// Time to replacement: how long a replacing repair took, from the
	// incident opening to its closing. Nothing else stands in for it when no
	// replacement closed — a grant's age measures something else.
	trows, err := h.pool.Query(ctx, `
		SELECT EXTRACT(EPOCH FROM (closed_at - opened_at))::float8 AS sec
		FROM entry_incidents
		WHERE closed_at IS NOT NULL
		  AND playbook IN ('evict_box', 'evict_domain')
		  AND opened_at >= now() - $1::interval
		ORDER BY sec ASC
	`, window)
	if err != nil {
		return fmt.Errorf("time to replacement: %w", err)
	}
	var durations []float64
	for trows.Next() {
		var sec float64
		if err := trows.Scan(&sec); err != nil {
			trows.Close()
			return fmt.Errorf("scan time to replacement: %w", err)
		}
		durations = append(durations, sec)
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return err
	}
	if len(durations) == 0 {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "strip.timeToReplacement",
			Why:    "в выбранном окне не закрывалось замен машин",
		})
		return nil
	}
	out.Strip.TimeToReplacement.SampleCount = len(durations)
	out.Strip.TimeToReplacement.MedianSec = median(durations)
	peak := durations[len(durations)-1]
	out.Strip.TimeToReplacement.PeakSec = &peak
	return nil
}

func (h *Handlers) fillFleetJobs(ctx context.Context, out *entryFleet) error {
	rows, err := h.pool.Query(ctx, `
		SELECT id, box, kind, status, step, error, created_at, finished_at
		FROM entry_jobs
		ORDER BY created_at DESC
		LIMIT 10
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var j fleetJob
		var errStr *string
		if err := rows.Scan(&j.ID, &j.Box, &j.Kind, &j.Status, &j.Step, &errStr, &j.CreatedAt, &j.FinishedAt); err != nil {
			return err
		}
		j.Error = errStr
		j.CanRetry = (j.Status == "failed")
		out.Jobs = append(out.Jobs, j)
	}

	return rows.Err()
}

func (h *Handlers) fillFleetBoxes(ctx context.Context, out *entryFleet) error {
	// 1. The standing incident of every box, by what it is about.
	incRows, err := h.pool.Query(ctx, `
		SELECT box, cause, advice
		FROM entry_incidents
		WHERE status IN ('open', 'acting') AND box IS NOT NULL
		ORDER BY opened_at
	`)
	if err != nil {
		return fmt.Errorf("open incidents: %w", err)
	}
	type standing struct{ cause, label string }
	openIncidents := make(map[string]standing)
	for incRows.Next() {
		var b, c, adv string
		if err := incRows.Scan(&b, &c, &adv); err != nil {
			incRows.Close()
			return fmt.Errorf("scan open incident: %w", err)
		}
		label := adv
		switch {
		case c == entry.CauseIPBanned:
			label = "ip забанен целиком"
		case c == entry.CauseDomainBlocked:
			label = "домен заблокирован · машина рабочая"
		case label == "":
			label = c
		}
		openIncidents[b] = standing{cause: c, label: label}
	}
	incRows.Close()
	if err := incRows.Err(); err != nil {
		return err
	}

	// 2. Fetch relays with active grants
	relayRows, err := h.pool.Query(ctx, `
		SELECT
			r.name,
			r.box,
			r.kind,
			r.status,
			r.signed_at,
			r.died_at,
			r.identity_ct,
			coalesce(count(g.id), 0) AS active_grants
		FROM entry_relays r
		LEFT JOIN entry_grants g ON g.relay_id = r.id AND g.revoked_at IS NULL
		GROUP BY r.id, r.name, r.box, r.kind, r.status, r.signed_at, r.died_at, r.identity_ct
		ORDER BY r.name ASC
	`)
	if err != nil {
		return fmt.Errorf("relays query: %w", err)
	}
	defer relayRows.Close()

	relaysByBox := make(map[string][]fleetRelay)
	for relayRows.Next() {
		var r fleetRelay
		var box string
		var identityCT []byte
		if err := relayRows.Scan(&r.Name, &box, &r.Kind, &r.Status, &r.SignedAt, &r.DiedAt, &identityCT, &r.Grants); err != nil {
			return err
		}
		openRelayFace(&r, identityCT)

		relaysByBox[box] = append(relaysByBox[box], r)
	}

	// 3. Fetch boxes
	boxRows, err := h.pool.Query(ctx, `
		SELECT
			name,
			provider,
			location,
			proto,
			coalesce(region, ''),
			rented_until,
			created_at,
			retired_at,
			domain_ct,
			coalesce(arch, ''),
			coalesce(edge_sha, ''),
			monthly_cents,
			coalesce(plan, ''),
			coalesce(provider_region, '')
		FROM entry_boxes
		ORDER BY name ASC
	`)
	if err != nil {
		return fmt.Errorf("boxes query: %w", err)
	}
	defer boxRows.Close()

	for boxRows.Next() {
		var b fleetBox
		var domainCT []byte
		var region, arch, sha, plan, provRegion string
		var monthlyCents *int
		if err := boxRows.Scan(
			&b.Name,
			&b.Provider,
			&b.Location,
			&b.Proto,
			&region,
			&b.RentedUntil,
			&b.CreatedAt,
			&b.RetiredAt,
			&domainCT,
			&arch,
			&sha,
			&monthlyCents,
			&plan,
			&provRegion,
		); err != nil {
			return err
		}

		b.Region = region
		b.Arch = arch
		b.EdgeSHA = sha
		b.MonthlyCents = monthlyCents
		b.Plan = plan
		b.ProviderRegion = provRegion

		if len(domainCT) > 0 {
			if plain, err := cryptobox.Decrypt(domainCT); err == nil {
				b.Domain = string(plain)
			}
		}

		b.Relays = relaysByBox[b.Name]
		if b.Relays == nil {
			b.Relays = []fleetRelay{}
		}

		// Determine incident and verdict
		if b.RetiredAt != nil {
			out.RetiredBoxesCount++
			b.Verdict = "mute"
			b.VerdictLabel = "сдана"
		} else if inc, has := openIncidents[b.Name]; has {
			label := inc.label
			b.OpenIncident = &label
			b.VerdictLabel = label
			if inc.cause == entry.CauseIPBanned {
				b.Verdict = "bad"
			} else {
				b.Verdict = "warn"
			}
		} else {
			// Check if any relay is bad
			hasDead := false
			for _, r := range b.Relays {
				if r.Status == "dead" {
					hasDead = true
					break
				}
			}
			if hasDead {
				b.Verdict = "warn"
				b.VerdictLabel = "релей требует замены"
			} else {
				b.Verdict = "ok"
				b.VerdictLabel = "чисто"
				out.CleanBoxesCount++
				for _, r := range b.Relays {
					if r.Status == "alive" {
						out.CleanRelaysCount++
					}
				}
			}
		}

		out.Boxes = append(out.Boxes, b)
	}

	return boxRows.Err()
}
