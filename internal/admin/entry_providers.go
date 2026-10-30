package admin

// entry_providers.go answers the console's provider comparison (plan 42 К6):
// which hoster's machines actually survive, how long they live, and what a day
// of life costs there.
//
// The credentials screen above it is internal/entry's (К5) — it edits keys and
// counts nothing. This one counts and edits nothing, which is the section's
// rule (plan 42 К §0): the surface that acts is not the surface that measures.
//
// Computed entirely from Postgres. Two of the figures only became possible
// with 064: a box now records WHY it was handed back, and "9 of 19 retired"
// means nothing until it splits into banned and rotated-out.

import (
	"context"
	"encoding/json"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"log"
	"net/http"
	"time"
)

// entryProviderCompare is the whole payload for the comparison strip.
type entryProviderCompare struct {
	// Window is the cohort window in days: a machine must be at least this
	// old to be asked whether it survived it.
	WindowDays int `json:"windowDays"`

	Hosting []providerStats `json:"hosting"`

	Missing []missingFigure `json:"missing"`
}

// providerStats is one hoster, measured. Every derived figure is nil-able:
// a provider the hub has never bought a machine from has no winrate, and the
// screen says "нет данных" rather than showing a flattering 100%.
type providerStats struct {
	Provider string `json:"provider"`

	BoughtCount  int `json:"boughtCount"`
	AliveCount   int `json:"aliveCount"`
	RetiredCount int `json:"retiredCount"`

	// The split 064 made possible: handed back because the address was
	// blocked, versus handed back healthy or for a reason no censor caused.
	BannedCount  int `json:"bannedCount"`
	RotatedCount int `json:"rotatedCount"`

	// Cohort is the machines old enough to be asked about the window, and
	// Survived those of them that reached its end without an ip ban.
	CohortCount   int      `json:"cohortCount"`
	SurvivedCount int      `json:"survivedCount"`
	WinratePct    *float64 `json:"winratePct"`

	// MedianLifeDays is measured over machines the censor burned
	// (entry.CensorDeaths): a living machine has not finished its life, and a
	// machine handed back on purpose did not die young — it was let go.
	// Counting either would pull the median towards "young" and put the
	// owner's own rotations on the hoster's bill.
	MedianLifeDays  *float64 `json:"medianLifeDays"`
	LifeSampleCount int      `json:"lifeSampleCount"`

	// MonthlyCents is what the live machines cost per month together;
	// PerBoxMonthlyCents the median price of one. Nil while no machine of this
	// provider carries a price (П9.2 writes it; machines imported before it
	// do not have one).
	MonthlyCents       int  `json:"monthlyCents"`
	PerBoxMonthlyCents *int `json:"perBoxMonthlyCents"`
	// PricelessBoxes is how many live machines have no price at all, so the
	// screen can say the cost figure is partial instead of quietly averaging
	// over fewer machines than it shows.
	PricelessBoxes int `json:"pricelessBoxes"`

	// CostPerLivingDayCents is the price of one machine's month divided by how
	// long such a machine actually lives. Nil when either half is missing.
	CostPerLivingDayCents *float64 `json:"costPerLivingDayCents"`

	// BansDaily is one count per day over the window: relays of this
	// provider's machines that died of an ip ban that day. Zero is a real
	// zero here — a day with no ban is a day with no ban.
	BansDaily  []providerBanDay `json:"bansDaily"`
	BansWindow int              `json:"bansWindow"`

	// TrafficGB is what this provider's relays carried over the window, from
	// their own pulses; nil when none of them pulsed. SpentCents is the rent
	// its machines accrued over the same days. Together they are what a
	// terabyte actually cost here — the plan's allowance is a ceiling, not a
	// measurement.
	TrafficGB  *float64 `json:"trafficGb"`
	SpentCents int      `json:"spentCents"`
}

type providerBanDay struct {
	Day  string `json:"day"`
	Bans int    `json:"bans"`
}

// compareWindow is fixed at 30 days: the figure the screen is named after is
// "winrate, 30 days", and a cohort window the owner can slide is a different
// question (how long do they last) that the median already answers.
const compareWindow = 30

// EntryProviderCompare answers GET /api/v1/admin/entry/providers/compare
func (h *Handlers) EntryProviderCompare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := entryProviderCompare{
		WindowDays: compareWindow,
		Hosting:    []providerStats{},
		Missing:    []missingFigure{},
	}

	if err := h.fillProviderStats(ctx, &out); err != nil {
		log.Printf("admin entry provider compare: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handlers) fillProviderStats(ctx context.Context, out *entryProviderCompare) error {
	// One row per provider the hub has ever bought a machine from. The cohort
	// is machines old enough to have been asked the window's question, and a
	// survivor is one that neither was handed back over a ban inside the
	// window nor had a relay ip-banned inside it: the ban is what the figure
	// is about, and it can be recorded in either place.
	const stats = `
		WITH boxes AS (
			SELECT
				b.name,
				b.provider,
				b.created_at,
				b.retired_at,
				b.retired_reason,
				b.monthly_cents,
				extract(epoch FROM (coalesce(b.retired_at, now()) - b.created_at)) / 86400.0 AS life_days,
				b.created_at <= now() - make_interval(days => $1::int) AS in_cohort,
				-- coalesce, not a bare OR: a machine still alive has no
				-- retired_reason, the comparison is NULL, and NULL OR false is
				-- NULL — which would drop every living machine out of the
				-- survivors instead of counting it as one.
				coalesce(
					b.retired_reason = 'ip_banned'
					AND b.retired_at <= b.created_at + make_interval(days => $1::int),
					false
				) OR EXISTS (
					SELECT 1 FROM entry_relays r
					WHERE r.box = b.name
					  AND r.died_reason = 'ip_banned'
					  AND r.died_at <= b.created_at + make_interval(days => $1::int)
				) AS banned_in_window
			FROM entry_boxes b
		)
		SELECT
			provider,
			count(*),
			count(*) FILTER (WHERE retired_at IS NULL),
			count(*) FILTER (WHERE retired_at IS NOT NULL),
			count(*) FILTER (WHERE retired_reason = 'ip_banned'),
			count(*) FILTER (WHERE retired_reason IN ('rotated', 'provider_down')),
			count(*) FILTER (WHERE in_cohort),
			count(*) FILTER (WHERE in_cohort AND NOT banned_in_window),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY life_days)
				FILTER (WHERE retired_reason = ANY($2)),
			count(*) FILTER (WHERE retired_reason = ANY($2)),
			coalesce(sum(monthly_cents) FILTER (WHERE retired_at IS NULL), 0)::int,
			percentile_cont(0.5) WITHIN GROUP (ORDER BY monthly_cents)
				FILTER (WHERE monthly_cents IS NOT NULL),
			count(*) FILTER (WHERE retired_at IS NULL AND monthly_cents IS NULL)
		FROM boxes
		GROUP BY provider
		ORDER BY count(*) DESC, provider ASC
	`
	rows, err := h.pool.Query(ctx, stats, compareWindow, entry.CensorDeaths)
	if err != nil {
		return err
	}
	defer rows.Close()

	at := make(map[string]int, 4)
	pricelessTotal := 0
	for rows.Next() {
		var p providerStats
		var medianPrice *float64
		if err := rows.Scan(
			&p.Provider,
			&p.BoughtCount,
			&p.AliveCount,
			&p.RetiredCount,
			&p.BannedCount,
			&p.RotatedCount,
			&p.CohortCount,
			&p.SurvivedCount,
			&p.MedianLifeDays,
			&p.LifeSampleCount,
			&p.MonthlyCents,
			&medianPrice,
			&p.PricelessBoxes,
		); err != nil {
			return err
		}
		p.MedianLifeDays = round1p(p.MedianLifeDays)
		if p.CohortCount > 0 {
			rate := round1(float64(p.SurvivedCount) / float64(p.CohortCount) * 100)
			p.WinratePct = &rate
		}
		if medianPrice != nil {
			cents := int(*medianPrice + 0.5)
			p.PerBoxMonthlyCents = &cents
			if p.MedianLifeDays != nil && *p.MedianLifeDays > 0 {
				perDay := round1(float64(cents) / *p.MedianLifeDays)
				p.CostPerLivingDayCents = &perDay
			}
		}
		p.BansDaily = []providerBanDay{}
		pricelessTotal += p.PricelessBoxes
		at[p.Provider] = len(out.Hosting)
		out.Hosting = append(out.Hosting, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if pricelessTotal > 0 {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "себестоимость живого дня у части машин",
			Why: "цена машины появилась с П9.2: у машин, завезённых раньше, monthly_cents пуст, " +
				"и в медиану цены они не входят",
		})
	}

	// The ban sparkline, one query for every provider: a relay that died of an
	// ip ban is a ban of the subnet its machine sits in, which is the thing
	// being compared.
	const bans = `
		WITH days AS (
			SELECT generate_series(
				date_trunc('day', now()) - make_interval(days => $1::int - 1),
				date_trunc('day', now()),
				interval '1 day'
			)::date AS d
		),
		provs AS (
			SELECT DISTINCT provider FROM entry_boxes
		),
		counted AS (
			SELECT
				b.provider,
				date_trunc('day', r.died_at)::date AS d,
				count(*) AS c
			FROM entry_relays r
			JOIN entry_boxes b ON b.name = r.box
			WHERE r.died_reason = 'ip_banned'
			  AND r.died_at >= date_trunc('day', now()) - make_interval(days => $1::int - 1)
			GROUP BY 1, 2
		)
		SELECT provs.provider, days.d::text, coalesce(counted.c, 0)::int
		FROM provs
		CROSS JOIN days
		LEFT JOIN counted ON counted.provider = provs.provider AND counted.d = days.d
		ORDER BY provs.provider, days.d ASC
	`
	banRows, err := h.pool.Query(ctx, bans, compareWindow)
	if err != nil {
		return err
	}
	defer banRows.Close()
	for banRows.Next() {
		var provider, day string
		var count int
		if err := banRows.Scan(&provider, &day, &count); err != nil {
			return err
		}
		i, ok := at[provider]
		if !ok {
			continue
		}
		out.Hosting[i].BansDaily = append(out.Hosting[i].BansDaily, providerBanDay{Day: day, Bans: count})
		out.Hosting[i].BansWindow += count
	}
	if err := banRows.Err(); err != nil {
		return err
	}
	return h.fillProviderCost(ctx, out, at)
}

// fillProviderCost is what each provider's machines carried and cost over the
// window. The rent is accrued the way the overview accrues it (fillMachineSpend):
// each machine's monthly price spread over the days it was in service inside
// the window.
func (h *Handlers) fillProviderCost(ctx context.Context, out *entryProviderCompare, at map[string]int) error {
	rows, err := h.pool.Query(ctx, `
		SELECT b.provider,
		       coalesce(sum(
		           b.monthly_cents / 30.0 * EXTRACT(EPOCH FROM (
		               upper(tstzrange(b.created_at, coalesce(b.retired_at, now())) * tstzrange(now() - make_interval(days => $1::int), now()))
		             - lower(tstzrange(b.created_at, coalesce(b.retired_at, now())) * tstzrange(now() - make_interval(days => $1::int), now()))
		           )) / 86400.0
		       ) FILTER (WHERE b.monthly_cents IS NOT NULL
		                   AND NOT isempty(tstzrange(b.created_at, coalesce(b.retired_at, now())) * tstzrange(now() - make_interval(days => $1::int), now()))),
		       0)::int
		FROM entry_boxes b
		GROUP BY b.provider
	`, compareWindow)
	if err != nil {
		return err
	}
	for rows.Next() {
		var provider string
		var cents int
		if err := rows.Scan(&provider, &cents); err != nil {
			rows.Close()
			return err
		}
		if i, ok := at[provider]; ok {
			out.Hosting[i].SpentCents = cents
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if h.analytics == nil {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "трафик провайдера",
			Why:    "хаб запущен без ClickHouse: пульсов релеев читать неоткуда",
		})
		return nil
	}
	providerOf := map[string]string{}
	prow, err := h.pool.Query(ctx, `
		SELECT r.name, b.provider FROM entry_relays r JOIN entry_boxes b ON b.name = r.box
	`)
	if err != nil {
		return err
	}
	for prow.Next() {
		var relay, provider string
		if err := prow.Scan(&relay, &provider); err != nil {
			prow.Close()
			return err
		}
		providerOf[relay] = provider
	}
	prow.Close()
	if err := prow.Err(); err != nil {
		return err
	}

	trows, err := h.analytics.Query(ctx, relayDeltasByRelayQ, time.Now().UTC().AddDate(0, 0, -compareWindow))
	if err != nil {
		return err
	}
	defer trows.Close()
	carried := map[string]uint64{}
	for trows.Next() {
		var relay string
		var bytes uint64
		if err := trows.Scan(&relay, &bytes); err != nil {
			return err
		}
		if provider, ok := providerOf[relay]; ok {
			carried[provider] += bytes
		}
	}
	if err := trows.Err(); err != nil {
		return err
	}
	for provider, bytes := range carried {
		if i, ok := at[provider]; ok {
			gb := round2(float64(bytes) / (1024 * 1024 * 1024))
			out.Hosting[i].TrafficGB = &gb
		}
	}
	return nil
}
