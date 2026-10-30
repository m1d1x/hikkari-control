package admin

// entry_domains.go answers the console's domain screen with the figures the
// ledger itself cannot show (plan 42 К4): how long a name survives on a box
// before it is burned, how fast the pool drains, and how much buffer is left.
//
// The ledger handle (internal/entry/admin.go) hands out rows and the buttons
// that act on them; this one only counts, and the two stay apart on purpose
// (plan 42 К §0: observation and tools are different surfaces).
//
// Computed entirely from Postgres: entry_domains carries status, the
// assigned/burned timestamps and the price, which is everything the screen
// asks for except one thing — a domain has no cause of death, so "lived until
// blocked" is really "lived until burned". That gap is named in missing
// instead of being papered over.

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
)

// entryDomainMetrics is the whole payload for the domain screen's top half.
type entryDomainMetrics struct {
	Window string `json:"window"` // "7d" | "30d" | "90d"

	Life domainLife `json:"life"`

	// Shapes is the same lifetime cut by what the name looks like, which is
	// the one question this screen answers that the overview does not: the
	// overview says how long names live, this says which kind to buy next.
	Shapes []domainShape `json:"shapes"`

	Pool domainPoolMetrics `json:"pool"`

	// Spend is what the window's names cost, day by day. Counted here and not
	// on the client: the ledger handle caps the rows it hands out, so a sum
	// taken from that list silently undercounts the moment the pool outgrows
	// the cap.
	Spend domainSpendMetrics `json:"spend"`

	Missing []missingFigure `json:"missing"`
}

// domainLife is "how long does a name live", the screen's headline. Every
// figure is nil-able: with nothing burned in the window there is no median to
// report, and the screen draws "нет данных" rather than a plausible number.
type domainLife struct {
	MedianHours *float64 `json:"medianHours"`
	// TrendHours is the window's recent half minus its older half. Negative
	// means names are burning faster than they used to.
	TrendHours  *float64 `json:"trendHours"`
	SampleCount int      `json:"sampleCount"`

	// Days are the window's day buckets as ISO dates, shared by every zone so
	// the screen can draw them on one axis.
	Days []string `json:"days"`

	// Zones are the TLDs the burned names belong to, heaviest first.
	Zones []domainZone `json:"zones"`
}

// domainZone is one TLD: its median over the window and the per-day medians
// behind the line. A day in which no name of this zone burned is nil, not
// zero — a zone with no sample that day did not suddenly live zero hours.
type domainZone struct {
	TLD         string     `json:"tld"`
	MedianHours *float64   `json:"medianHours"`
	SampleCount int        `json:"sampleCount"`
	Daily       []*float64 `json:"daily"`
}

// domainShape is a family of names — how they are built, not where they live.
//
// The generator can make a name any way it likes, and the console has nothing
// to say about which way until the ledger is asked. A median over three names
// is noise dressed as advice, so a family under shapeMinSamples reports its
// count and no median, and the screen says "мало данных" instead of ranking
// it.
type domainShape struct {
	// Shape is the family's id; the console holds the wording.
	Shape       string   `json:"shape"`
	MedianHours *float64 `json:"medianHours"`
	SampleCount int      `json:"sampleCount"`
	// Example is one name of the family, so the operator can see what the
	// classifier means rather than trust the label.
	Example string `json:"example"`
}

// shapeMinSamples is how many burned names a family needs before its median
// is a figure rather than an anecdote.
const shapeMinSamples = 5

// classifyDomainShape names the family of a domain by its label — the part
// before the first dot. The classes are the ways the hub's own generator and
// the registrar's suggestions actually build names.
func classifyDomainShape(domain string) string {
	label := domain
	if i := strings.IndexByte(label, '.'); i > 0 {
		label = label[:i]
	}
	label = strings.ToLower(label)
	if label == "" {
		return "other"
	}

	hasDigit, hasAlpha := false, false
	for _, r := range label {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z':
			hasAlpha = true
		}
	}

	switch {
	case strings.Contains(label, "-"):
		return "hyphenated"
	case hasDigit && !hasAlpha:
		return "digits"
	case hasDigit && hasAlpha:
		return "word_digits"
	case len(label) >= 10:
		return "long_word"
	case hasAlpha:
		return "short_word"
	default:
		return "other"
	}
}

// domainPoolMetrics is the buffer: what is left, how fast it drains, and how
// long that leaves before the hub must buy.
type domainPoolMetrics struct {
	Owned    int `json:"owned"`
	Assigned int `json:"assigned"`
	Buying   int `json:"buying"`

	// BurnedWindow is the raw count the rate is derived from, so the screen
	// can say "4 за 7 дней" instead of implying a measured daily cadence.
	BurnedWindow     int      `json:"burnedWindow"`
	BurnedPerDay     *float64 `json:"burnedPerDay"`
	PrevBurnedPerDay *float64 `json:"prevBurnedPerDay"`

	// HoursLeft is the pool divided by the burn rate. Nil while nothing burns:
	// a pool that drains at zero does not last "forever", the question simply
	// has no answer yet.
	HoursLeft *float64 `json:"hoursLeft"`
}

// domainSpendMetrics is the money the pool cost over the window, with the
// series behind each figure — the screen draws all three as lines, so a month
// that doubled is visible as a slope and not only as a bigger number.
type domainSpendMetrics struct {
	BoughtCount int `json:"boughtCount"`
	BoughtCents int `json:"boughtCents"`
	// AvgCents is nil when nothing was bought: an average over no purchases
	// is not zero dollars a name.
	AvgCents *int `json:"avgCents"`
	// Days are ISO dates, shared with Life.Days so the axes line up.
	Days []string `json:"days"`
	// One per day: what was spent, how many names, and the average of that
	// day (nil on a day with no purchase).
	DailyCents []int  `json:"dailyCents"`
	DailyCount []int  `json:"dailyCount"`
	DailyAvg   []*int `json:"dailyAvg"`
	// MonthSpentCents is the calendar month's committed spend, the figure the
	// registrar caps are checked against (entry: domainSpend).
	MonthSpentCents int `json:"monthSpentCents"`
}

// domainWindow parses the screen's range. The domain screen's windows are
// longer than the rest of the console's: a name lives for days, so a 24h
// window would hold too few burns to have a median at all.
func domainWindow(q string) (int, string, bool) {
	switch q {
	case "", "30d":
		return 30, "30d", true
	case "7d":
		return 7, "7d", true
	case "90d":
		return 90, "90d", true
	}
	return 0, "", false
}

// EntryDomainMetrics answers GET /api/v1/admin/entry/domains/metrics
func (h *Handlers) EntryDomainMetrics(w http.ResponseWriter, r *http.Request) {
	days, label, ok := domainWindow(r.URL.Query().Get("window"))
	if !ok {
		http.Error(w, "window is 7d, 30d or 90d", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	out := entryDomainMetrics{
		Window: label,
		Life: domainLife{
			Days:  []string{},
			Zones: []domainZone{},
		},
		Shapes: []domainShape{},
		Missing: []missingFigure{{
			Figure: "почему имя сгорело",
			Why:    "у домена нет своей причины смерти: причина живёт на релее (died_reason), домен знает только burned_at. медиана считает любое сгорание, не только блокировку по имени",
		}},
	}

	if err := h.fillDomainLife(ctx, &out, days); err != nil {
		log.Printf("admin entry domain metrics: life: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	if err := h.fillDomainShapes(ctx, &out, days); err != nil {
		log.Printf("admin entry domain metrics: shapes: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	if err := h.fillDomainPool(ctx, &out, days); err != nil {
		log.Printf("admin entry domain metrics: pool: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	if err := h.fillDomainSpend(ctx, &out, days); err != nil {
		log.Printf("admin entry domain metrics: spend: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}

// fillDomainLife computes the median lifetime, its trend and the per-zone
// lines. A name's life is assigned_at → burned_at: the span it actually spent
// in front of users, not the span since it was bought.
func (h *Handlers) fillDomainLife(ctx context.Context, out *entryDomainMetrics, days int) error {
	// The headline median plus the two halves the trend is the difference of.
	// FILTER on an ordered-set aggregate keeps it one pass over one window.
	const headline = `
		WITH lives AS (
			SELECT
				extract(epoch FROM (burned_at - assigned_at)) / 3600.0 AS hours,
				burned_at >= now() - make_interval(days => $1::int) / 2    AS recent
			FROM entry_domains
			WHERE status IN ('burned', 'dead')
			  AND assigned_at IS NOT NULL
			  AND burned_at IS NOT NULL
			  AND burned_at > assigned_at
			  AND burned_at >= now() - make_interval(days => $1::int)
		)
		SELECT
			percentile_cont(0.5) WITHIN GROUP (ORDER BY hours),
			count(*),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY hours) FILTER (WHERE recent),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY hours) FILTER (WHERE NOT recent)
		FROM lives
	`
	var median, recent, older *float64
	var samples int
	if err := h.pool.QueryRow(ctx, headline, days).Scan(&median, &samples, &recent, &older); err != nil {
		return err
	}
	out.Life.MedianHours = round1p(median)
	out.Life.SampleCount = samples
	if recent != nil && older != nil {
		trend := round1(*recent - *older)
		out.Life.TrendHours = &trend
	}

	// The day axis, built in Postgres so the buckets match the ones the
	// per-day medians are grouped into.
	const axis = `
		SELECT generate_series(
			date_trunc('day', now()) - make_interval(days => $1::int - 1),
			date_trunc('day', now()),
			interval '1 day'
		)::date::text
	`
	rows, err := h.pool.Query(ctx, axis, days)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := make(map[string]int, days)
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return err
		}
		index[day] = len(out.Life.Days)
		out.Life.Days = append(out.Life.Days, day)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// One row per zone: its median over the whole window, heaviest first.
	const zones = `
		SELECT
			tld,
			percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM (burned_at - assigned_at)) / 3600.0),
			count(*)
		FROM entry_domains
		WHERE status IN ('burned', 'dead')
		  AND assigned_at IS NOT NULL
		  AND burned_at IS NOT NULL
		  AND burned_at > assigned_at
		  AND burned_at >= now() - make_interval(days => $1::int)
		GROUP BY tld
		ORDER BY count(*) DESC, tld ASC
	`
	zoneRows, err := h.pool.Query(ctx, zones, days)
	if err != nil {
		return err
	}
	defer zoneRows.Close()
	at := make(map[string]int, 8)
	for zoneRows.Next() {
		var zone domainZone
		if err := zoneRows.Scan(&zone.TLD, &zone.MedianHours, &zone.SampleCount); err != nil {
			return err
		}
		zone.MedianHours = round1p(zone.MedianHours)
		zone.Daily = make([]*float64, len(out.Life.Days))
		at[zone.TLD] = len(out.Life.Zones)
		out.Life.Zones = append(out.Life.Zones, zone)
	}
	if err := zoneRows.Err(); err != nil {
		return err
	}

	// The lines themselves: the median life of the names that burned on that
	// day, per zone. Days without a sample stay nil.
	const daily = `
		SELECT
			tld,
			date_trunc('day', burned_at)::date::text,
			percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM (burned_at - assigned_at)) / 3600.0)
		FROM entry_domains
		WHERE status IN ('burned', 'dead')
		  AND assigned_at IS NOT NULL
		  AND burned_at IS NOT NULL
		  AND burned_at > assigned_at
		  AND burned_at >= date_trunc('day', now()) - make_interval(days => $1::int - 1)
		GROUP BY 1, 2
	`
	dayRows, err := h.pool.Query(ctx, daily, days)
	if err != nil {
		return err
	}
	defer dayRows.Close()
	for dayRows.Next() {
		var tld, day string
		var hours *float64
		if err := dayRows.Scan(&tld, &day, &hours); err != nil {
			return err
		}
		zi, okZone := at[tld]
		di, okDay := index[day]
		if !okZone || !okDay {
			continue
		}
		out.Life.Zones[zi].Daily[di] = round1p(hours)
	}
	return dayRows.Err()
}

// fillDomainSpend totals what the window's purchases cost, day by day.
//
// A purchase is dated by bought_at and not created_at: the row can be written
// before the registrar confirms, and the screen is about money leaving the
// account. The month's own figure next to it is the one the cap is checked
// against, which is a calendar month and deliberately a different span — the
// screen says which is which rather than quietly mixing them.
func (h *Handlers) fillDomainSpend(ctx context.Context, out *entryDomainMetrics, days int) error {
	out.Spend.Days = append([]string{}, out.Life.Days...)
	out.Spend.DailyCents = make([]int, len(out.Spend.Days))
	out.Spend.DailyCount = make([]int, len(out.Spend.Days))
	out.Spend.DailyAvg = make([]*int, len(out.Spend.Days))

	index := make(map[string]int, len(out.Spend.Days))
	for i, day := range out.Spend.Days {
		index[day] = i
	}

	const daily = `
		SELECT
			date_trunc('day', bought_at)::date::text,
			count(*),
			coalesce(sum(cost_cents), 0)
		FROM entry_domains
		WHERE source = 'purchased'
		  AND status <> 'failed'
		  AND bought_at IS NOT NULL
		  AND bought_at >= date_trunc('day', now()) - make_interval(days => $1::int - 1)
		GROUP BY 1
	`
	rows, err := h.pool.Query(ctx, daily, days)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var count, cents int
		if err := rows.Scan(&day, &count, &cents); err != nil {
			return err
		}
		i, ok := index[day]
		if !ok {
			continue
		}
		out.Spend.DailyCount[i] = count
		out.Spend.DailyCents[i] = cents
		if count > 0 {
			avg := cents / count
			out.Spend.DailyAvg[i] = &avg
		}
		out.Spend.BoughtCount += count
		out.Spend.BoughtCents += cents
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if out.Spend.BoughtCount > 0 {
		avg := out.Spend.BoughtCents / out.Spend.BoughtCount
		out.Spend.AvgCents = &avg
	}

	// The month against the cap, by the same rule the buyer enforces it with
	// (internal/entry/spend.go, domainSpend): committed from the moment
	// the intent is written, so two purchases cannot both slip under the cap.
	const month = `
		SELECT coalesce(sum(cost_cents), 0)
		FROM entry_domains
		WHERE source = 'purchased' AND status <> 'failed'
		  AND created_at >= date_trunc('month', now())
	`
	return h.pool.QueryRow(ctx, month).Scan(&out.Spend.MonthSpentCents)
}

// fillDomainShapes cuts the same lifetimes by the shape of the name.
//
// The classification is Go's and not SQL's on purpose: it is a product rule
// about how the hub builds names, it will change as the generator changes,
// and a regex grown inside a query is the kind of thing nobody dares touch
// later. The ledger holds hundreds of rows, not millions, so reading them and
// grouping here costs nothing worth the trade.
func (h *Handlers) fillDomainShapes(ctx context.Context, out *entryDomainMetrics, days int) error {
	const q = `
		SELECT
			domain,
			extract(epoch FROM (burned_at - assigned_at)) / 3600.0 AS hours
		FROM entry_domains
		WHERE status IN ('burned', 'dead')
		  AND assigned_at IS NOT NULL
		  AND burned_at IS NOT NULL
		  AND burned_at > assigned_at
		  AND burned_at >= now() - make_interval(days => $1::int)
	`
	rows, err := h.pool.Query(ctx, q, days)
	if err != nil {
		return err
	}
	defer rows.Close()

	lives := map[string][]float64{}
	example := map[string]string{}
	for rows.Next() {
		var domain string
		var hours float64
		if err := rows.Scan(&domain, &hours); err != nil {
			return err
		}
		shape := classifyDomainShape(domain)
		lives[shape] = append(lives[shape], hours)
		if _, seen := example[shape]; !seen {
			example[shape] = domain
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for shape, hours := range lives {
		s := domainShape{Shape: shape, SampleCount: len(hours), Example: example[shape]}
		if len(hours) >= shapeMinSamples {
			sort.Float64s(hours)
			mid := len(hours) / 2
			median := hours[mid]
			if len(hours)%2 == 0 {
				median = (hours[mid-1] + hours[mid]) / 2
			}
			s.MedianHours = round1p(&median)
		}
		out.Shapes = append(out.Shapes, s)
	}
	// Longest-lived first, and the families with no median yet after them:
	// the order is the recommendation, so an unranked family must not sit
	// above a measured one.
	sort.Slice(out.Shapes, func(i, j int) bool {
		a, b := out.Shapes[i], out.Shapes[j]
		switch {
		case a.MedianHours != nil && b.MedianHours != nil:
			return *a.MedianHours > *b.MedianHours
		case a.MedianHours != nil:
			return true
		case b.MedianHours != nil:
			return false
		default:
			return a.SampleCount > b.SampleCount
		}
	})
	return nil
}

// fillDomainPool counts the buffer and how fast it drains.
func (h *Handlers) fillDomainPool(ctx context.Context, out *entryDomainMetrics, days int) error {
	const counts = `
		SELECT
			count(*) FILTER (WHERE status = 'owned'),
			count(*) FILTER (WHERE status = 'assigned'),
			count(*) FILTER (WHERE status = 'buying'),
			count(*) FILTER (WHERE status IN ('burned', 'dead') AND burned_at >= now() - make_interval(days => $1::int)),
			count(*) FILTER (WHERE status IN ('burned', 'dead')
				AND burned_at >= now() - make_interval(days => $1::int) * 2
				AND burned_at <  now() - make_interval(days => $1::int))
		FROM entry_domains
	`
	var owned, assigned, buying, burned, burnedPrev int
	if err := h.pool.QueryRow(ctx, counts, days).Scan(&owned, &assigned, &buying, &burned, &burnedPrev); err != nil {
		return err
	}

	out.Pool.Owned = owned
	out.Pool.Assigned = assigned
	out.Pool.Buying = buying
	out.Pool.BurnedWindow = burned

	window := float64(days)
	if burned > 0 {
		rate := round1(float64(burned) / window)
		out.Pool.BurnedPerDay = &rate
		if rate > 0 {
			left := round1(float64(owned) / rate * 24)
			out.Pool.HoursLeft = &left
		}
	}
	if burnedPrev > 0 {
		prev := round1(float64(burnedPrev) / window)
		out.Pool.PrevBurnedPerDay = &prev
	}
	return nil
}

// round1 keeps one decimal: these are hours and names per day, and the
// screen has no use for the float's tail.
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func round1p(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := round1(*v)
	return &r
}
