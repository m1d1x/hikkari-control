package admin

// entry_overview.go answers the entry-points console's first screen (plan 42
// П9.9 §6а): one request for the whole dashboard, instead of the three
// parallel fetches each of its old pages made.
//
// # What this screen is, and is not
//
// It only reads. Observation and tools are separate surfaces here: nothing on
// this screen acts, and the pages that act compute no analytics. That split is
// the point of the phase — the old console diagnosed a box in Svelte, which
// meant the diagnosis existed for the operator's eyes and for nothing else.
//
// # Counters of inventory are deliberately absent
//
// "14 relays alive of 17", "9 domains in the pool": with the autopilot running
// those numbers lead to no decision, because the thing they would prompt is
// already automatic. What is here instead is what a person acts on — whether
// people get through, what it costs, and how fast the censor is burning what
// we buy.
//
// # Nothing here is invented
//
// A figure the hub cannot compute yet is absent from the JSON and named in
// `missing`, never defaulted to a plausible number. The console shows "no
// data" for it. This is the rule the old system page broke by rendering 99.9%
// uptime whenever the backend returned none.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// overviewWindow is how far back the screen's headline figures look. A week:
// long enough that one bad evening does not move a median, short enough to
// still be "now" rather than history.
const overviewWindow = 7 * 24 * time.Hour

// entryOverview is the whole screen.
type entryOverview struct {
	// Window is the headline window, as the request asked for it.
	Window string `json:"window"`
	// BucketSeconds is the width of one point of every series on the screen
	// (entry_overview_series.go): all the charts share one axis.
	BucketSeconds int `json:"bucketSeconds"`
	// Reach is the doorway metric: did people get through.
	Reach reachBlock `json:"reach"`
	// Money is what the doorway costs, by what it is spent on.
	Money moneyBlock `json:"money"`
	// Survival is how long what we buy lives before the censor burns it.
	Survival survivalBlock `json:"survival"`
	// Protocols is each protocol's share and the quality behind it.
	Protocols []protocolBlock `json:"protocols"`
	// Traffic is how much the doorway carried, and how much that is per
	// person. Absent until relays have pulsed.
	Traffic *trafficBlock `json:"traffic"`
	// TrafficPerUser is the per-person reading of the same doorway, from
	// what each account's socket carried (entry_user_traffic) — the figure
	// `missing` calls "traffic.perUser". Absent until a socket has flushed.
	TrafficPerUser *perUserTraffic `json:"trafficPerUser"`
	// NeedsYou is what the autopilot cannot do by itself, and Automation what
	// it did. The two are deliberately separate lists: one is a queue, the
	// other a log, and mixing them makes the queue unreadable.
	NeedsYou   []incidentRow `json:"needsYou"`
	Automation []incidentRow `json:"automation"`
	// Missing names every figure this build cannot compute yet, and why. The
	// console renders "no data" for each — it never fills a gap with a
	// plausible number.
	Missing []missingFigure `json:"missing"`
}

// missingFigure is one absent number with the reason it is absent, so the
// console can say which part of the plan fills it rather than showing a blank.
type missingFigure struct {
	Figure string `json:"figure"`
	Why    string `json:"why"`
}

// reachBlock answers "did people get through", split so the three shares add
// up to every attempt made.
type reachBlock struct {
	// Attempts is how many attempts the window holds. Zero means no client of
	// this build has reported yet, and every share below is zero with it.
	Attempts int64 `json:"attempts"`
	// People is how many distinct accounts those attempts came from, counted
	// over pseudonyms — the hub holds no list of who they are.
	People int64 `json:"people"`
	// FirstTryPct got through without the carrier race working relays;
	// RetriedPct needed it; FailedPct never got through at all. The last one
	// is reported by the session that finally came up, which is the only way
	// it can be known.
	FirstTryPct float64 `json:"firstTryPct"`
	RetriedPct  float64 `json:"retriedPct"`
	FailedPct   float64 `json:"failedPct"`
	// Series is one point per bucket of the window, oldest first. The
	// distance between `anyPct` and `firstPct` is the cost of retrying, and
	// its widening is the earliest sign of a new filter.
	Series []reachPoint `json:"series"`
}

// moneyBlock is the spend by what it is spent on, plus where the month lands
// if the current rate holds.
//
// Two articles, and they are spent differently: a domain is bought once and
// its receipt is in the ledger, a machine is rented and accrues by the day. So
// the domain line is summed from purchases and the machine line is computed
// from the rent of the boxes that were in service — which is why one figure
// could not be derived from the other's query.
type moneyBlock struct {
	Domains  spendLine `json:"domains"`
	Machines spendLine `json:"machines"`
	// Total is the row the console's third line shows: the whole doorway bill.
	Total      spendLine `json:"total"`
	MonthlyCap int64     `json:"monthlyCapCents"`
	// MachinesUnpriced is how many machines in service carry no price — the
	// ones rented by hand before the hoster was under key. The machine line
	// understates by their rent, and that is said rather than hidden (the same
	// honesty as `relaysReporting` in the traffic block).
	MachinesUnpriced int64 `json:"machinesUnpriced"`
	// MachinesPriced is how many machines the hub knows the rent of, retired
	// ones included — the rows the machine line is computed from. Zero with a
	// hand-rented machine in service is the one case where the article is not
	// a figure at all; zero with no machines on record is a true zero, since
	// entry_boxes is the whole fleet and there is no rent to be unknown.
	MachinesPriced int64  `json:"machinesPriced"`
	PerUserMonth   *int64 `json:"perUserMonthCents"`
	ActivePeople   int64  `json:"activePeople"`
	// Series is the spend bucket by bucket.
	Series []moneyPoint `json:"series"`
}

// spendLine is one spending article in three readings. Projection is the
// month's spend at the rate seen so far — the number that says "this is where
// it lands", which neither of the other two does.
type spendLine struct {
	PerDayCents     int64 `json:"perDayCents"`
	MonthCents      int64 `json:"monthCents"`
	ProjectionCents int64 `json:"projectionCents"`
}

// survivalBlock is how long what we buy lives. The two medians apart are the
// diagnosis: a domain's falling while a relay's holds means the censor is
// burning names, not addresses — so the answer is in how domains are chosen,
// and the machines are innocent.
//
// Only what the censor killed counts (entry.CensorDeaths). A machine handed
// back on purpose, or lost to the hoster, did not live a short life: it was
// let go, and counting it would put every rotation into the censor's column.
type survivalBlock struct {
	// BoxDays is the median life of a machine retired for a censor reason, in
	// days; nil when none was.
	BoxDays *float64 `json:"boxMedianDays"`
	// BoxDaysBefore is the same median one window earlier.
	BoxDaysBefore *float64 `json:"boxMedianDaysBefore"`
	// CensorDeaths is how many relays the censor killed in the window.
	CensorDeaths int64 `json:"censorDeaths"`
	// DomainHours is the median life of a domain that has been burnt, in
	// hours; nil when none has burnt yet.
	DomainHours *float64 `json:"domainMedianHours"`
	// DomainHoursBefore is the same median one window earlier, for the delta.
	DomainHoursBefore *float64 `json:"domainMedianHoursBefore"`
	// RelayDays is the median life of a relay that has died, in days.
	RelayDays *float64 `json:"relayMedianDays"`
	// RelayDaysBefore is the same, one window earlier.
	RelayDaysBefore *float64 `json:"relayMedianDaysBefore"`
	// RelaysEver counts every relay the pool has ever held, alive and dead:
	// the turnover, which is what there is to count when the autopilot
	// replaces a dead relay before anyone looks.
	RelaysEver int64 `json:"relaysEver"`
	// Series is what the censor burned bucket by bucket.
	Series []survivalPoint `json:"series"`
}

// protocolBlock is one protocol's share of attempts and the quality people got
// through it. `direct` is here too: its share is the measure of how tightly
// the direct path is closed, and its fall is the censor's work showing up in
// our bill.
type protocolBlock struct {
	Proto string `json:"proto"`
	// Attempts and SharePct are over successful attempts only: a failed
	// attempt belongs to no protocol, since nothing carried it.
	Attempts int64   `json:"attempts"`
	SharePct float64 `json:"sharePct"`
	// P50Ms and P95Ms are the round trip people actually saw, from their own
	// clients — not from one vantage of ours.
	P50Ms float64 `json:"p50Ms"`
	P95Ms float64 `json:"p95Ms"`
	// ConnectP50Ms is how long the attempt took end to end: the wait a person
	// sat through, which a round trip does not show.
	ConnectP50Ms float64 `json:"connectP50Ms"`
	// Latency is the measured shape of P50Ms over the window, one point per
	// bucket. A bucket nobody reported through carries a null median rather
	// than a zero, so the chart breaks the line instead of asserting a
	// measurement that was never taken.
	Latency []latencyPoint `json:"latency"`
}

// latencyPoint is one bucket of the protocol's latency series.
type latencyPoint struct {
	// Bucket is the bucket's start, hourly for the 24h window and daily
	// otherwise.
	Bucket string `json:"bucket"`
	// P50Ms is null when no attempt in the bucket went through this protocol.
	P50Ms *float64 `json:"p50Ms"`
	// Attempts is the bucket's sample size, so a lone point is readable as
	// one report rather than as a trend.
	Attempts int64 `json:"attempts"`
}

// trafficBlock is what the relays carried over the window.
//
// Per person is a division, never an attribution: the relays report totals and
// know nothing about who made them (plan 42 §0.1), and the people are counted
// from the clients' own reach telemetry. The two never meet in a row — only in
// this quotient, which is what makes the figure computable without the edge
// ever holding a session list.
type trafficBlock struct {
	GBTotal float64 `json:"gbTotal"`
	// GBUp and GBDown split the total by direction as the relays counted it:
	// up is what clients sent, down what they received.
	GBUp   float64 `json:"gbUp"`
	GBDown float64 `json:"gbDown"`
	// GBPerPersonDay is the figure the console shows: the load one person puts
	// on the doorway in a day. nil when nobody reached in the window.
	GBPerPersonDay *float64 `json:"gbPerPersonDay"`
	// Relays is how many reported at all. A number far under the pool means
	// the rest are silent, and the total understates by that much — said here
	// rather than hidden, because an understated total reads as a quiet week.
	Relays int64 `json:"relaysReporting"`
	// Series is the traffic bucket by bucket.
	Series []trafficPoint `json:"series"`
}

// perUserTraffic is what one person moves through the doorway, read per
// account rather than divided out of a total. Medians, not means: one heavy
// account must not make everyone look heavy, and a median is what "a typical
// person" means. Every figure is a quantile over the window's rows; nothing is
// kept as a counter of its own.
//
// It is the message socket only — voice and files take other paths, which
// `missing` names as "traffic.perUser.media" rather than leaving the socket to
// pass for a person's whole traffic.
type perUserTraffic struct {
	// People is how many accounts' sockets carried anything in the window.
	People int64 `json:"people"`
	// MedianMBDay and P90MBDay are a person's socket traffic per day of the
	// window, over all paths.
	MedianMBDay float64 `json:"medianMbDay"`
	P90MBDay    float64 `json:"p90MbDay"`
	// ViaRelayPeople is how many came through a relay at least once, and
	// ViaRelayMedianMBDay the median of what such a person moved through
	// relays — the bypass load per person. nil when nobody used a relay.
	ViaRelayPeople      int64    `json:"viaRelayPeople"`
	ViaRelayMedianMBDay *float64 `json:"viaRelayMedianMbDay"`
	// ViaRelayPct is the share of all socket bytes that came through relays.
	ViaRelayPct float64 `json:"viaRelayPct"`
}

// perUserTrafficQ reads entry_user_traffic per account over the window and
// takes the quantiles across accounts. The one bound parameter is the start
// of the window.
const perUserTrafficQ = `
	SELECT
		count()                                             AS people,
		toFloat64(quantileExact(0.5)(total))                AS median_bytes,
		toFloat64(quantileExact(0.9)(total))                AS p90_bytes,
		countIf(relay > 0)                                  AS via_people,
		toFloat64(quantileExactIf(0.5)(relay, relay > 0))   AS via_median_bytes,
		sum(relay)                                          AS relay_bytes,
		sum(total)                                          AS total_bytes
	FROM (
		SELECT account,
		       sum(bytes_up + bytes_down)                AS total,
		       sumIf(bytes_up + bytes_down, box != '')   AS relay
		FROM entry_user_traffic
		WHERE ts >= ?
		GROUP BY account
	)
`

// fillTrafficPerUser reads what each person moved. Absent when no socket
// flushed in the window: zero would read as "people use nothing", which is a
// claim, not an absence.
func (h *Handlers) fillTrafficPerUser(ctx context.Context, out *entryOverview, since time.Time, window time.Duration) error {
	var (
		people, viaPeople      uint64
		median, p90, viaMedian float64
		relayBytes, totalBytes uint64
	)
	rows, err := h.analytics.Query(ctx, perUserTrafficQ, since)
	if err != nil {
		return fmt.Errorf("per-user traffic: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&people, &median, &p90, &viaPeople, &viaMedian, &relayBytes, &totalBytes); err != nil {
			return fmt.Errorf("scan per-user traffic: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if people == 0 || totalBytes == 0 {
		return nil
	}
	days := window.Hours() / 24
	if days < 1 {
		days = 1
	}
	const mb = 1024 * 1024
	block := perUserTraffic{
		People:         int64(people),
		MedianMBDay:    round2(median / mb / days),
		P90MBDay:       round2(p90 / mb / days),
		ViaRelayPeople: int64(viaPeople),
		ViaRelayPct:    round2(float64(relayBytes) / float64(totalBytes) * 100),
	}
	if viaPeople > 0 {
		v := round2(viaMedian / mb / days)
		block.ViaRelayMedianMBDay = &v
	}
	out.TrafficPerUser = &block
	return nil
}

// incidentRow is one incident as the console's right-hand column shows it.
type incidentRow struct {
	ID       string            `json:"id"`
	Box      string            `json:"box"`
	Cause    string            `json:"cause"`
	Status   string            `json:"status"`
	Advice   string            `json:"advice"`
	Evidence map[string]string `json:"evidence,omitempty"`
	// Playbook is the repair proposed or run; empty when the hub knows none.
	Playbook string `json:"playbook,omitempty"`
	// Spends says the repair costs money — which is why it waits for a person.
	Spends bool `json:"spends"`
	// Mode is how far the hub may go alone with this cause: auto, ask, off.
	// The console shows a button only where pressing it would do something.
	Mode string `json:"mode"`
	Why  string `json:"why"`
	// Handoff is why the hub stopped working on this by itself — repairs that
	// did not help, a wave of this cause, no playbook at all (П9.6). It is
	// what puts an incident in the queue even when its mode is `auto`.
	Handoff   string `json:"handoff,omitempty"`
	DecidedBy string `json:"decidedBy,omitempty"`
	CostCents int    `json:"costCents"`
	Outcome   string `json:"outcome,omitempty"`
	OpenedAt  string `json:"openedAt"`
	ClosedAt  string `json:"closedAt,omitempty"`
}

// EntryOverview answers the console's first screen.
//
// GET /api/v1/admin/entry/overview?window=7d
//
// Windows: 24h | 7d | 30d (default 7d). Every chart spans the window, in
// buckets of [seriesBucket].
func (h *Handlers) EntryOverview(w http.ResponseWriter, r *http.Request) {
	if h.analytics == nil {
		http.Error(w, "analytics store not configured", http.StatusServiceUnavailable)
		return
	}
	window, label, ok := overviewRange(r.URL.Query().Get("window"))
	if !ok {
		http.Error(w, "window is 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	since := time.Now().UTC().Add(-window)

	out := entryOverview{Window: label, Protocols: []protocolBlock{}, Missing: []missingFigure{}}

	if err := h.fillReach(ctx, &out, since); err != nil {
		log.Printf("admin entry overview: reach: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillProtocols(ctx, &out, since, window); err != nil {
		log.Printf("admin entry overview: protocols: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillMoney(ctx, &out, window); err != nil {
		log.Printf("admin entry overview: money: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillSurvival(ctx, &out, window); err != nil {
		log.Printf("admin entry overview: survival: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillTraffic(ctx, &out, since, window); err != nil {
		log.Printf("admin entry overview: traffic: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillTrafficPerUser(ctx, &out, since, window); err != nil {
		log.Printf("admin entry overview: per-user traffic: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillSeries(ctx, &out, since, window); err != nil {
		log.Printf("admin entry overview: series: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillIncidents(ctx, &out); err != nil {
		log.Printf("admin entry overview: incidents: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	// What this build genuinely cannot compute. Named here rather than
	// defaulted, so the screen says "no data" instead of something plausible.
	out.Missing = append(out.Missing,
		missingFigure{
			Figure: "protocols.lossPct",
			Why: "loss as a person experiences it is the client's to report, not the relay's — " +
				"an edge sees its own sockets, not the path; it belongs in the reach telemetry",
		},
	)
	// Not one machine the hub has ever had carries a price, so the article is
	// not a figure at all. One priced box is enough to make it one —
	// understated by the hand-rented rest, which the block says in
	// machinesUnpriced rather than hiding. No machines at all is not this
	// case: every box the hub has is a row in entry_boxes, so an empty fleet
	// costs a known nothing, the same as a ledger with no domain purchases.
	if out.Money.MachinesPriced == 0 && out.Money.MachinesUnpriced > 0 {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "money.machines",
			Why: "every machine in service was rented by hand and carries no price — " +
				"a machine the hub orders itself records what it costs",
		})
	}
	if out.Traffic == nil {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "traffic",
			Why:    "no relay has pulsed in this window — П9.3 is deployed when the boxes are re-provisioned",
		})
	}

	if out.TrafficPerUser == nil {
		out.Missing = append(out.Missing, missingFigure{
			Figure: "traffic.perUser",
			Why:    "no signed-in socket has flushed its byte count in this window",
		})
	}
	// Always: the per-person figure is the message socket, and the rest of a
	// person's traffic has no per-account source.
	out.Missing = append(out.Missing, missingFigure{
		Figure: "traffic.perUser.media",
		Why: "voice and files go through the sfu and the media relay, which count no bytes per account — " +
			"the per-person figure is the message socket only",
	})

	writeJSONResponse(w, out)
}

// overviewRange maps the query's window onto a duration and its canonical
// label.
func overviewRange(q string) (time.Duration, string, bool) {
	switch q {
	case "", "7d":
		return overviewWindow, "7d", true
	case "24h":
		return 24 * time.Hour, "24h", true
	case "30d":
		return 30 * 24 * time.Hour, "30d", true
	}
	return 0, "", false
}

// fillReach computes the doorway shares and the long chart.
//
// The shares are taken over attempts, not over people: one person who cannot
// get through retries, and counting people would hide exactly the case the
// number exists to show.
func (h *Handlers) fillReach(ctx context.Context, out *entryOverview, since time.Time) error {
	const headline = `
		SELECT
			count()                                        AS attempts,
			uniqExact(account)                             AS people,
			countIf(outcome = 'direct' OR (outcome = 'relay' AND relays_tried <= 1)) AS first_try,
			countIf(outcome = 'relay' AND relays_tried > 1) AS retried,
			countIf(outcome = 'failed')                    AS failed
		FROM entry_reach
		WHERE ts >= ?
	`
	rows, err := h.analytics.Query(ctx, headline, since)
	if err != nil {
		return fmt.Errorf("headline: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var attempts, people, firstTry, retried, failed uint64
		if err := rows.Scan(&attempts, &people, &firstTry, &retried, &failed); err != nil {
			return fmt.Errorf("scan headline: %w", err)
		}
		out.Reach.Attempts = int64(attempts)
		out.Reach.People = int64(people)
		if attempts > 0 {
			total := float64(attempts)
			out.Reach.FirstTryPct = round2(float64(firstTry) / total * 100)
			out.Reach.RetriedPct = round2(float64(retried) / total * 100)
			out.Reach.FailedPct = round2(float64(failed) / total * 100)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("headline rows: %w", err)
	}

	return nil
}

// fillProtocols splits the successful attempts by what carried them.
//
// The dataset holds a relay name, not a protocol: the protocol is a property
// of the box (migration 049), and resolving it here rather than writing it
// into every row keeps the write path dumb and the mapping correctable. The
// map is the pool's size — a handful of rows — so it is read whole and folded
// in memory rather than joined across two databases.
func (h *Handlers) fillProtocols(ctx context.Context, out *entryOverview, since time.Time, window time.Duration) error {
	protoOf, err := h.relayProtocols(ctx)
	if err != nil {
		return err
	}

	const q = `
		SELECT
			outcome,
			relay_name,
			count()                     AS attempts,
			quantile(0.5)(rtt_ms)       AS p50,
			quantile(0.95)(rtt_ms)      AS p95,
			quantile(0.5)(connect_ms)   AS connect_p50
		FROM entry_reach
		WHERE ts >= ? AND outcome != 'failed'
		GROUP BY outcome, relay_name
	`
	rows, err := h.analytics.Query(ctx, q, since)
	if err != nil {
		return fmt.Errorf("protocols: %w", err)
	}
	defer rows.Close()

	// Weighted accumulation: each (outcome, relay) group carries its own
	// count, and a percentile cannot be averaged, so the quantiles are taken
	// from the largest group of a protocol rather than blended into nonsense.
	type acc struct {
		attempts  int64
		heaviest  int64
		p50       float64
		p95       float64
		connectP5 float64
	}
	byProto := map[string]*acc{}
	for rows.Next() {
		var outcome, relay string
		var attempts uint64
		var p50, p95, connectP50 float64
		if err := rows.Scan(&outcome, &relay, &attempts, &p50, &p95, &connectP50); err != nil {
			return fmt.Errorf("scan protocols: %w", err)
		}
		proto := "direct"
		if outcome == "relay" {
			var ok bool
			proto, ok = protoOf[relay]
			if !ok {
				// A relay the pool no longer knows. Its attempts happened, so
				// they are not dropped — they just cannot be attributed.
				proto = "unknown"
			}
		}
		a := byProto[proto]
		if a == nil {
			a = &acc{}
			byProto[proto] = a
		}
		a.attempts += int64(attempts)
		if int64(attempts) > a.heaviest {
			a.heaviest = int64(attempts)
			a.p50, a.p95, a.connectP5 = p50, p95, connectP50
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("protocol rows: %w", err)
	}

	var total int64
	for _, a := range byProto {
		total += a.attempts
	}
	for proto, a := range byProto {
		share := 0.0
		if total > 0 {
			share = round2(float64(a.attempts) / float64(total) * 100)
		}
		out.Protocols = append(out.Protocols, protocolBlock{
			Proto:        proto,
			Attempts:     a.attempts,
			SharePct:     share,
			P50Ms:        round2(a.p50),
			P95Ms:        round2(a.p95),
			ConnectP50Ms: round2(a.connectP5),
		})
	}
	sortProtocols(out.Protocols)
	return h.fillProtocolLatency(ctx, out, since, window, protoOf)
}

// fillProtocolLatency measures how each protocol's latency moved over the
// window, one median per bucket.
//
// The series is asked per protocol rather than in one grouped pass on purpose:
// a quantile cannot be averaged, and the protocol lives on the box, not on the
// row. Folding several relays' medians into one would invent a figure, while
// one query per protocol with its own relay names asks ClickHouse for the
// median of exactly the right rows. The pool holds a handful of protocols, so
// that is a handful of queries.
//
// A bucket with no attempts gets a null median, not a zero: the censor pushing
// traffic off a protocol for an hour is not the same as that protocol becoming
// instant, and a line drawn through the gap would claim a measurement that was
// never taken.
func (h *Handlers) fillProtocolLatency(
	ctx context.Context,
	out *entryOverview,
	since time.Time,
	window time.Duration,
	protoOf map[string]string,
) error {
	bucket := seriesBucket(window)
	bucketHours := int64(bucket / time.Hour)

	relaysOf := map[string]clickhouse.ArraySet{}
	for relay, proto := range protoOf {
		relaysOf[proto] = append(relaysOf[proto], relay)
	}

	// Both query texts are compile-time constants; only the interval, the
	// horizon and the relay-name set are bound.
	const relayQ = `
		SELECT
			toStartOfInterval(ts, toIntervalHour(?)) AS bucket,
			count()                                 AS attempts,
			quantile(0.5)(rtt_ms)                   AS p50
		FROM entry_reach
		WHERE ts >= ? AND outcome = 'relay' AND relay_name IN (?)
		GROUP BY bucket
		ORDER BY bucket
	`
	const directQ = `
		SELECT
			toStartOfInterval(ts, toIntervalHour(?)) AS bucket,
			count()                                 AS attempts,
			quantile(0.5)(rtt_ms)                   AS p50
		FROM entry_reach
		WHERE ts >= ? AND outcome = 'direct'
		GROUP BY bucket
		ORDER BY bucket
	`

	for i := range out.Protocols {
		proto := out.Protocols[i].Proto
		out.Protocols[i].Latency = []latencyPoint{}

		var rows driver.Rows
		var err error
		switch {
		case proto == "direct":
			rows, err = h.analytics.Query(ctx, directQ, bucketHours, since)
		case len(relaysOf[proto]) > 0:
			rows, err = h.analytics.Query(ctx, relayQ, bucketHours, since, relaysOf[proto])
		default:
			// A protocol whose relays the pool no longer knows by name —
			// `unknown`. Its attempts are counted in the headline figures, but
			// they cannot be selected back out of the dataset, so the series
			// stays empty and the screen draws no line for it.
			continue
		}
		if err != nil {
			return fmt.Errorf("latency %s: %w", proto, err)
		}

		measured := map[time.Time]latencyPoint{}
		for rows.Next() {
			var at time.Time
			var attempts uint64
			var p50 float64
			if err := rows.Scan(&at, &attempts, &p50); err != nil {
				rows.Close()
				return fmt.Errorf("scan latency %s: %w", proto, err)
			}
			v := round2(p50)
			measured[at.UTC()] = latencyPoint{
				Bucket:   at.UTC().Format(time.RFC3339),
				P50Ms:    &v,
				Attempts: int64(attempts),
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("latency rows %s: %w", proto, err)
		}
		rows.Close()

		for at := since.Truncate(bucket); !at.After(time.Now().UTC()); at = at.Add(bucket) {
			if p, ok := measured[at]; ok {
				out.Protocols[i].Latency = append(out.Protocols[i].Latency, p)
				continue
			}
			out.Protocols[i].Latency = append(out.Protocols[i].Latency, latencyPoint{
				Bucket: at.Format(time.RFC3339),
			})
		}
	}
	return nil
}

// relayProtocols maps every relay name the pool has ever held onto the
// protocol of the box it sits on. Dead relays included: an attempt through a
// relay that has since died is still an attempt that happened.
func (h *Handlers) relayProtocols(ctx context.Context) (map[string]string, error) {
	const q = `
		SELECT r.name, b.proto
		FROM entry_relays r
		JOIN entry_boxes b ON r.name LIKE b.name || '-%'
	`
	rows, err := h.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("relay protocols: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, proto string
		if err := rows.Scan(&name, &proto); err != nil {
			return nil, fmt.Errorf("scan relay protocol: %w", err)
		}
		out[name] = proto
	}
	return out, rows.Err()
}

// fillMoney totals what the doorway cost, and where the month lands at that
// rate.
func (h *Handlers) fillMoney(ctx context.Context, out *entryOverview, window time.Duration) error {
	const q = `
		SELECT
			COALESCE(SUM(cost_cents) FILTER (WHERE created_at >= date_trunc('month', now())), 0) AS month,
			COALESCE(SUM(cost_cents) FILTER (WHERE created_at >= now() - $1::interval), 0)       AS win
		FROM entry_domains
		WHERE source = 'purchased' AND status <> 'failed'
	`
	var month, inWindow int64
	if err := h.pool.QueryRow(ctx, q, window).Scan(&month, &inWindow); err != nil {
		return fmt.Errorf("domain spend: %w", err)
	}

	perDay := int64(float64(inWindow) / window.Hours() * 24)
	now := time.Now().UTC()
	daysIntoMonth := float64(now.Day())
	daysInMonth := float64(time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day())

	// Projection runs the recent daily rate out to the end of the month and
	// adds it to what is already spent — the rate now, not the rate so far,
	// because the thing being watched is a change in it.
	projection := month + int64(float64(perDay)*(daysInMonth-daysIntoMonth))

	out.Money.Domains = spendLine{
		PerDayCents:     perDay,
		MonthCents:      month,
		ProjectionCents: projection,
	}
	if err := h.fillMachineSpend(ctx, out, window); err != nil {
		return err
	}
	out.Money.Total = spendLine{
		PerDayCents:     out.Money.Domains.PerDayCents + out.Money.Machines.PerDayCents,
		MonthCents:      out.Money.Domains.MonthCents + out.Money.Machines.MonthCents,
		ProjectionCents: out.Money.Domains.ProjectionCents + out.Money.Machines.ProjectionCents,
	}
	projection = out.Money.Total.ProjectionCents
	out.Money.ActivePeople = out.Reach.People
	// Cost per active person: the unit economics of getting past the censor.
	// Only computed when both halves are real — a spend divided by nobody is
	// not a large number, it is no number.
	if out.Reach.People > 0 {
		perUser := int64(float64(projection) / float64(out.Reach.People))
		out.Money.PerUserMonth = &perUser
	}

	// The whole doorway's month: both spend groups' caps together, as the
	// total line is both groups' spend (entry.SpendRoles).
	if err := h.pool.QueryRow(ctx,
		`SELECT coalesce(sum(monthly_cap_cents), 0)::bigint FROM entry_spend_caps`,
	).Scan(&out.Money.MonthlyCap); err != nil {
		return fmt.Errorf("spend caps: %w", err)
	}
	return nil
}

// fillMachineSpend is what the rented machines cost (П9.2).
//
// # Why this is accrued and not summed
//
// A machine has no receipts: it has a monthly rent and a stretch of days it
// was in service. So the day rate is its rent spread over the days of the
// month it was alive for, and the month-to-date is that accrual from the first
// of the month (or from the day the box was born) to now, stopping on the day
// it was handed back. A sum of prices would answer "what would a full month
// of today's fleet cost", which is a different and less useful number — it
// says nothing about a machine that lived for three days.
//
// The projection then runs the CURRENT fleet's rent out to the end of the
// month and adds it to what has accrued, the same shape as the domain line:
// today's rate to the month's end, because the thing being watched is a change
// in it.
func (h *Handlers) fillMachineSpend(ctx context.Context, out *entryOverview, window time.Duration) error {
	// days_in_month is read in SQL so the accrual and the clipping agree on
	// one calendar; `tstzrange * tstzrange` clips each box's life to the
	// period being asked about, and `upper - lower` of the result is how long
	// it was in service inside it.
	const q = `
		WITH cal AS (
			SELECT date_trunc('month', now()) AS month_from,
			       now()                      AS now_ts,
			       EXTRACT(DAY FROM date_trunc('month', now()) + interval '1 month - 1 day')::numeric AS days
		),
		boxes AS (
			SELECT monthly_cents,
			       tstzrange(created_at, coalesce(retired_at, now())) AS life
			FROM entry_boxes WHERE monthly_cents IS NOT NULL
		),
		accrued AS (
			SELECT
				SUM(monthly_cents / c.days * (EXTRACT(EPOCH FROM (upper(in_month) - lower(in_month))) / 86400.0))
					FILTER (WHERE NOT isempty(in_month)) AS month,
				SUM(monthly_cents / c.days * (EXTRACT(EPOCH FROM (upper(in_window) - lower(in_window))) / 86400.0))
					FILTER (WHERE NOT isempty(in_window)) AS win
			FROM boxes b, cal c,
			     LATERAL (SELECT b.life * tstzrange(c.month_from, c.now_ts) AS in_month) m,
			     LATERAL (SELECT b.life * tstzrange(c.now_ts - $1::interval, c.now_ts) AS in_window) w
		)
		SELECT COALESCE((SELECT month FROM accrued), 0)::bigint,
		       COALESCE((SELECT win FROM accrued), 0)::bigint,
		       COALESCE((SELECT SUM(monthly_cents) FROM entry_boxes WHERE retired_at IS NULL), 0)::bigint,
		       (SELECT count(*) FROM entry_boxes WHERE retired_at IS NULL AND monthly_cents IS NULL),
		       (SELECT count(*) FROM entry_boxes WHERE monthly_cents IS NOT NULL)
	`
	var month, inWindow, fleetMonthly, unpriced, priced int64
	if err := h.pool.QueryRow(ctx, q, window).Scan(&month, &inWindow, &fleetMonthly, &unpriced, &priced); err != nil {
		return fmt.Errorf("machine spend: %w", err)
	}

	now := time.Now().UTC()
	daysInMonth := float64(time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day())
	daysLeft := daysInMonth - float64(now.Day())

	out.Money.Machines = spendLine{
		PerDayCents:     int64(float64(inWindow) / window.Hours() * 24),
		MonthCents:      month,
		ProjectionCents: month + int64(float64(fleetMonthly)/daysInMonth*daysLeft),
	}
	out.Money.MachinesUnpriced, out.Money.MachinesPriced = unpriced, priced
	return nil
}

// fillSurvival computes the two medians, and the same two one window earlier
// so the console can show which way they are moving.
//
// Derived, never stored: both are a subtraction over rows the hub already
// keeps, and a counter beside them would only be a second truth to keep in
// step.
func (h *Handlers) fillSurvival(ctx context.Context, out *entryOverview, window time.Duration) error {
	const domains = `
		SELECT
			percentile_cont(0.5) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (burned_at - assigned_at)) / 3600.0
			) FILTER (WHERE burned_at >= now() - $1::interval),
			percentile_cont(0.5) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (burned_at - assigned_at)) / 3600.0
			) FILTER (WHERE burned_at < now() - $1::interval AND burned_at >= now() - $1::interval * 2)
		FROM entry_domains
		WHERE burned_at IS NOT NULL AND assigned_at IS NOT NULL
	`
	if err := h.pool.QueryRow(ctx, domains, window).
		Scan(&out.Survival.DomainHours, &out.Survival.DomainHoursBefore); err != nil {
		return fmt.Errorf("domain lifetime: %w", err)
	}

	const relays = `
		SELECT
			percentile_cont(0.5) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (died_at - coalesce(issued_at, created_at))) / 86400.0
			) FILTER (WHERE died_at >= now() - $1::interval),
			percentile_cont(0.5) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (died_at - coalesce(issued_at, created_at))) / 86400.0
			) FILTER (WHERE died_at < now() - $1::interval AND died_at >= now() - $1::interval * 2),
			count(*) FILTER (WHERE died_at >= now() - $1::interval),
			(SELECT count(*) FROM entry_relays)
		FROM entry_relays
		WHERE died_at IS NOT NULL AND died_reason = ANY($2)
	`
	if err := h.pool.QueryRow(ctx, relays, window, entry.CensorDeaths).
		Scan(&out.Survival.RelayDays, &out.Survival.RelayDaysBefore,
			&out.Survival.CensorDeaths, &out.Survival.RelaysEver); err != nil {
		return fmt.Errorf("relay lifetime: %w", err)
	}

	const boxes = `
		SELECT
			percentile_cont(0.5) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (retired_at - created_at)) / 86400.0
			) FILTER (WHERE retired_at >= now() - $1::interval),
			percentile_cont(0.5) WITHIN GROUP (
				ORDER BY EXTRACT(EPOCH FROM (retired_at - created_at)) / 86400.0
			) FILTER (WHERE retired_at < now() - $1::interval AND retired_at >= now() - $1::interval * 2)
		FROM entry_boxes
		WHERE retired_at IS NOT NULL AND retired_reason = ANY($2)
	`
	if err := h.pool.QueryRow(ctx, boxes, window, entry.CensorDeaths).
		Scan(&out.Survival.BoxDays, &out.Survival.BoxDaysBefore); err != nil {
		return fmt.Errorf("box lifetime: %w", err)
	}
	out.Survival.BoxDays = round1p(out.Survival.BoxDays)
	out.Survival.BoxDaysBefore = round1p(out.Survival.BoxDaysBefore)
	out.Survival.RelayDays = round1p(out.Survival.RelayDays)
	out.Survival.RelayDaysBefore = round1p(out.Survival.RelayDaysBefore)
	return nil
}

// sortProtocols puts the heaviest first, so the console's grid reads in order
// of what actually carries people.
func sortProtocols(ps []protocolBlock) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j].Attempts > ps[j-1].Attempts; j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

// writeJSONResponse is the admin plane's usual JSON write.
func writeJSONResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("admin entry overview: encode: %v", err)
	}
}

// fillTraffic totals what the relays carried, and divides it by the people the
// clients' telemetry counted.
//
// The counters are cumulative since each edge started, so the traffic is the
// sum of each relay's pulse-to-pulse deltas, with restarts handled the way
// [relayDeltasQ] says — the same query the chart draws from, summed over the
// window, so the figure and the chart under it cannot disagree.
func (h *Handlers) fillTraffic(ctx context.Context, out *entryOverview, since time.Time, window time.Duration) error {
	var relays uint64
	if err := h.analytics.QueryRow(ctx, &relays,
		`SELECT uniqExact(relay_name) FROM entry_relay_stats WHERE ts >= ?`, since); err != nil {
		return fmt.Errorf("relays reporting: %w", err)
	}
	if relays == 0 {
		// No relay reported. Absent, not zero: zero would read as "nobody used
		// the doorway this week", which is a different and alarming claim.
		return nil
	}
	rows, err := h.analytics.Query(ctx, relayDeltasQ, int64(seriesBucket(window)/time.Hour), since)
	if err != nil {
		return fmt.Errorf("relay traffic: %w", err)
	}
	defer rows.Close()
	var up, down uint64
	for rows.Next() {
		var at time.Time
		var u, d uint64
		if err := rows.Scan(&at, &u, &d); err != nil {
			return fmt.Errorf("scan traffic: %w", err)
		}
		up, down = up+u, down+d
	}
	if err := rows.Err(); err != nil {
		return err
	}

	const gb = 1024 * 1024 * 1024
	total := float64(up+down) / gb
	block := trafficBlock{
		GBTotal: round2(total),
		GBUp:    round2(float64(up) / gb),
		GBDown:  round2(float64(down) / gb),
		Relays:  int64(relays),
		Series:  []trafficPoint{},
	}
	if out.Reach.People > 0 {
		days := window.Hours() / 24
		if days < 1 {
			days = 1
		}
		perPersonDay := round2(total / float64(out.Reach.People) / days)
		block.GBPerPersonDay = &perPersonDay
	}
	out.Traffic = &block
	return nil
}

// fillIncidents splits the feed into the two lists the console shows.
//
// They are separate on purpose. "Needs you" is a queue: it holds only what the
// autopilot cannot finish by itself, and it is short or the queue stops being
// read. "Automation" is a log of what it did, and its job is the opposite —
// to grow, so the owner can see how much was handled without them.
//
// An incident is sorted by what its playbook says, not by its own severity: a
// banned IP that the hub may replace unattended belongs in the log, and a
// cheap domain swap that still wants a yes belongs in the queue.
func (h *Handlers) fillIncidents(ctx context.Context, out *entryOverview) error {
	const q = `
		-- A fleet incident (071) has no box; its pool stands in the same slot.
		SELECT id::text, coalesce(box, pool), cause, status, advice, evidence, playbook,
		       decided_by, cost_cents, outcome, handoff, opened_at, closed_at
		FROM entry_incidents
		WHERE status IN ('open', 'acting') OR closed_at >= now() - interval '24 hours'
		ORDER BY opened_at DESC
		LIMIT 100
	`
	// The mode the hub acts on, the owner's override applied (П9.8): the
	// console must show the mode the mechanic reads, not the code's default.
	modes, err := entry.LoadPlaybookModes(ctx, h.pool)
	if err != nil {
		return err
	}
	rows, err := h.pool.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("incidents: %w", err)
	}
	defer rows.Close()

	out.NeedsYou = []incidentRow{}
	out.Automation = []incidentRow{}
	for rows.Next() {
		var r incidentRow
		var evidence []byte
		var decidedBy, outcome *string
		var opened time.Time
		var closed *time.Time
		if err := rows.Scan(&r.ID, &r.Box, &r.Cause, &r.Status, &r.Advice, &evidence,
			&r.Playbook, &decidedBy, &r.CostCents, &outcome, &r.Handoff, &opened, &closed); err != nil {
			return fmt.Errorf("scan incident: %w", err)
		}
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &r.Evidence); err != nil {
				return fmt.Errorf("incident evidence: %w", err)
			}
		}
		if decidedBy != nil {
			r.DecidedBy = *decidedBy
		}
		if outcome != nil {
			r.Outcome = *outcome
		}
		r.OpenedAt = opened.UTC().Format(time.RFC3339)
		if closed != nil {
			r.ClosedAt = closed.UTC().Format(time.RFC3339)
		}

		p := modes.For(r.Cause)
		r.Spends, r.Mode, r.Why = p.Spends, p.Mode, p.Why
		if r.Playbook == "" {
			r.Playbook = p.Name
		}

		// Still standing and the hub may not finish it alone → the queue.
		// Everything else — closed, or running unattended — is the log.
		//
		// `handoff` is the second way into the queue, and the one that is not
		// about the playbook: a repair the hub was allowed to run, tried and
		// could not make work ends up in front of a person however cheap it
		// was (П9.6).
		standing := r.Status == entry.IncidentOpen || r.Status == entry.IncidentActing
		if standing && (p.NeedsAPerson() || r.Handoff != "") {
			out.NeedsYou = append(out.NeedsYou, r)
		} else {
			out.Automation = append(out.Automation, r)
		}
	}
	return rows.Err()
}
