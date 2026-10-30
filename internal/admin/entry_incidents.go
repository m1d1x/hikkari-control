package admin

// entry_incidents.go answers the console's incidents screen (plan 42 К2): one
// request for the whole of it — how the autopilot is doing, how fast it
// notices and repairs, what it handed over, and the feed of everything that
// happened.
//
// # Why this screen exists at all
//
// The incident is the declared bridge between the two halves of the console:
// the overview observes, the tool pages act, and the one thing that crosses is
// an incident. Until this screen there was nowhere to click through to — the
// overview showed two short columns of incidents and the bridge led nowhere.
//
// # It reads, and it reads two stores
//
// Nothing here acts: the three buttons on a card are the entry package's
// (internal/entry/admin_incidents.go), which is the same split the overview
// keeps. And it is the one screen of the section where Postgres and ClickHouse
// are brought together: everything about an incident is in Postgres, but when
// the failure it was opened for first showed up is only in the probe stream,
// so "time to notice" is the difference between the two.
//
// # Nothing here is invented
//
// A median the hub cannot compute is absent from the JSON and named in
// `missing`, with the reason. In particular time-to-notice is absent whenever
// the probe stream cannot answer it — no analytics store, no probes in the
// window, a ClickHouse too old for the window function — rather than quietly
// becoming time-to-diagnose.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// detectLookback is how far before an incident opened its first failing probe
// is looked for.
//
// Thirty minutes: a probe round is a minute, the detector runs on the same
// timer, so a healthy gap between the first failure and the incident is
// seconds. A failing sample half an hour earlier belongs to some other
// episode, and counting it would turn one slow detection into the median.
const detectLookback = 30 * time.Minute

// incidentDays is how many daily points the automation chart carries. Thirty:
// the chart's job is "is the autopilot keeping up", which needs weeks of shape
// and not hours.
const incidentDays = 30

// entryIncidents is the whole screen.
type entryIncidents struct {
	// Window is the headline window, as the request asked for it.
	Window string `json:"window"`
	// Automation is how the incidents of the window ended, by day and in one
	// figure. The growing green area of the chart is the screen's point: how
	// much the autopilot did instead of a person.
	Automation automationBlock `json:"automation"`
	// Stages is how long each stage took, in medians.
	Stages stagesBlock `json:"stages"`
	// Pauses is the wave fuses standing right now. Not incidents, and
	// deliberately a list of their own (К2 §4): a pause is why three
	// look-alike incidents are sitting untouched.
	Pauses []entry.CausePause `json:"pauses"`
	// NeedsYou is the queue — what the autopilot cannot finish by itself.
	NeedsYou []incidentFeedRow `json:"needsYou"`
	// Feed is everything in the window, newest first, standing ones included.
	Feed []incidentFeedRow `json:"feed"`
	// Playbooks is what "another action" may choose from: the repairs the hub
	// has code for.
	Playbooks []entry.PlaybookOption `json:"playbooks"`
	// Missing names every figure this build cannot compute, and why.
	Missing []missingFigure `json:"missing"`
}

// automationBlock is the chart and its headline.
type automationBlock struct {
	// AutoPct is the share of the window's ended incidents the autopilot
	// closed by itself, 0..100. Null when nothing ended in the window — a
	// share of nothing is not zero percent.
	AutoPct *float64 `json:"autoPct"`
	// AutoPctBefore is the same share over the window before this one, so the
	// screen can show which way it is moving. Null on the same terms.
	AutoPctBefore *float64 `json:"autoPctBefore"`
	// Total is how many incidents the window holds, ended or not.
	Total int `json:"total"`
	// By is the window's incidents by how they ended.
	By resolutionCounts `json:"by"`
	// Days is one point per day, oldest first.
	Days []incidentDay `json:"days"`
}

// resolutionCounts is the four endings the chart stacks, plus the ones that
// have not ended yet. `Working` is kept apart from all four: an incident the
// hub is still on is not a success and not a failure, and folding it into
// either would make today's column lie until midnight.
type resolutionCounts struct {
	Auto      int `json:"auto"`
	Handed    int `json:"handed"`
	Failed    int `json:"failed"`
	Dismissed int `json:"dismissed"`
	Working   int `json:"working"`
}

type incidentDay struct {
	Day string `json:"day"` // YYYY-MM-DD, UTC
	resolutionCounts
}

// stagesBlock is the three medians the screen shows, in seconds.
//
// Each is null rather than zero when the window holds nothing to measure, and
// `samples` says how many incidents stood behind each one — a median over two
// incidents is a number a person should be allowed to distrust.
type stagesBlock struct {
	// ToNoticeSec: the first failing probe → the incident was opened. The one
	// figure on this screen that needs the probe stream.
	ToNoticeSec *float64 `json:"toNoticeSec"`
	// ToDecideSec: the incident was opened → a repair started.
	ToDecideSec *float64 `json:"toDecideSec"`
	// ToRepairSec: a repair started → the replacement passed a probe and
	// entered the pool (its admission; its closing when there is no admission,
	// which is the case for an incident that ended without a repair landing).
	ToRepairSec   *float64 `json:"toRepairSec"`
	NoticeSamples int      `json:"noticeSamples"`
	DecideSamples int      `json:"decideSamples"`
	RepairSamples int      `json:"repairSamples"`
}

// incidentFeedRow is one incident with everything the screen's timeline needs.
//
// It carries the overview's row and adds the three timestamps the overview has
// no use for, plus how it ended and whether a repair of it failed — which is
// what the feed's filters are over.
type incidentFeedRow struct {
	incidentRow
	ActedAt    string `json:"actedAt,omitempty"`
	AdmittedAt string `json:"admittedAt,omitempty"`
	// Resolution is auto | handed | failed | dismissed | working, as
	// [resolutionCounts] names them.
	Resolution string `json:"resolution"`
	// Repairs is how many attempts were made, and RepairsFailed how many of
	// them did not work. The card's "3 attempts, hetzner quota" line.
	Repairs       int `json:"repairs"`
	RepairsFailed int `json:"repairsFailed"`
}

// resolutionExpr classifies an incident by how it ended, in one place, so the
// chart's aggregate and the feed's rows can never disagree about it.
//
// The order is the precedence: a failed repair outranks everything, because
// that is the one ending that says the hub was wrong about what to do; a
// handoff outranks a dismissal, because being handed over is why a person came
// to dismiss it; and only what ended without any of that counts as the
// autopilot's own work.
const resolutionExpr = `
	CASE
		WHEN count(r.id) FILTER (WHERE r.status = 'failed') > 0 THEN 'failed'
		WHEN i.handoff <> ''                                    THEN 'handed'
		WHEN i.status = 'dismissed'                             THEN 'dismissed'
		WHEN i.status IN ('open', 'acting')                     THEN 'working'
		ELSE 'auto'
	END`

// EntryIncidents answers the incidents screen.
//
// GET /api/v1/admin/entry/incidents?window=7d
//
// Windows: 24h | 7d | 30d (default 7d). The chart always spans
// [incidentDays], regardless, because its job is the trend.
func (h *Handlers) EntryIncidents(w http.ResponseWriter, r *http.Request) {
	window, label, ok := overviewRange(r.URL.Query().Get("window"))
	if !ok {
		http.Error(w, "window is 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()

	out := entryIncidents{
		Window:    label,
		Pauses:    []entry.CausePause{},
		NeedsYou:  []incidentFeedRow{},
		Feed:      []incidentFeedRow{},
		Playbooks: entry.Playbooks(),
		Missing:   []missingFigure{},
	}

	if err := h.fillIncidentFeed(ctx, &out, now.Add(-window)); err != nil {
		log.Printf("admin entry incidents: feed: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillAutomation(ctx, &out, now, window); err != nil {
		log.Printf("admin entry incidents: automation: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillStages(ctx, &out, now.Add(-window)); err != nil {
		log.Printf("admin entry incidents: stages: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if err := h.fillPauses(ctx, &out); err != nil {
		log.Printf("admin entry incidents: pauses: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	// Time to notice is the one figure here that needs the probe stream, and
	// it is the one that goes missing rather than degrading: an incident whose
	// first failing probe cannot be found is not one that was noticed
	// instantly.
	if why := h.fillTimeToNotice(ctx, &out, now.Add(-window)); why != "" {
		out.Missing = append(out.Missing, missingFigure{Figure: "stages.toNoticeSec", Why: why})
	}

	writeJSONResponse(w, out)
}

// fillIncidentFeed reads the window's incidents, standing ones included
// whatever their age, and splits the queue out of them.
//
// An open incident is never paged out of the queue, for the same reason
// Service.Incidents pages only the closed half: the screen's whole claim is
// that it shows what needs a person.
func (h *Handlers) fillIncidentFeed(ctx context.Context, out *entryIncidents, since time.Time) error {
	const q = `
		-- A fleet incident (071) has no box; its pool stands in the same slot.
		SELECT i.id::text, coalesce(i.box, i.pool), i.cause, i.status, i.advice, i.evidence, i.playbook,
		       i.decided_by, i.cost_cents, i.outcome, i.handoff,
		       i.opened_at, i.acted_at, i.closed_at, i.admitted_at,
		       count(r.id)                                   AS repairs,
		       count(r.id) FILTER (WHERE r.status = 'failed') AS repairs_failed,` +
		resolutionExpr + ` AS resolution
		FROM entry_incidents i
		LEFT JOIN entry_repairs r ON r.incident_id = i.id
		WHERE i.opened_at >= $1 OR i.status IN ('open', 'acting')
		GROUP BY i.id
		ORDER BY i.opened_at DESC
		LIMIT 300
	`
	// The mode the hub acts on, the owner's override applied (П9.8): the
	// console must show the mode the mechanic reads, not the code's default.
	modes, err := entry.LoadPlaybookModes(ctx, h.pool)
	if err != nil {
		return err
	}
	rows, err := h.pool.Query(ctx, q, since)
	if err != nil {
		return fmt.Errorf("incident feed: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var fr incidentFeedRow
		var evidence []byte
		var opened time.Time
		var acted, closed, admitted *time.Time
		if err := rows.Scan(&fr.ID, &fr.Box, &fr.Cause, &fr.Status, &fr.Advice, &evidence,
			&fr.Playbook, &fr.DecidedBy, &fr.CostCents, &fr.Outcome, &fr.Handoff,
			&opened, &acted, &closed, &admitted,
			&fr.Repairs, &fr.RepairsFailed, &fr.Resolution); err != nil {
			return fmt.Errorf("scan incident: %w", err)
		}
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &fr.Evidence); err != nil {
				return fmt.Errorf("incident evidence: %w", err)
			}
		}
		fr.OpenedAt = opened.UTC().Format(time.RFC3339)
		fr.ActedAt = isoOrEmpty(acted)
		fr.ClosedAt = isoOrEmpty(closed)
		fr.AdmittedAt = isoOrEmpty(admitted)

		p := modes.For(fr.Cause)
		fr.Spends, fr.Mode, fr.Why = p.Spends, p.Mode, p.Why
		if fr.Playbook == "" {
			fr.Playbook = p.Name
		}

		// The queue is what is still standing and cannot be finished by the
		// hub alone — the mode says so, or the hub already gave up on it. The
		// same rule the overview's right-hand column uses, deliberately: two
		// screens disagreeing about what needs a person would make both
		// useless.
		standing := fr.Status == entry.IncidentOpen || fr.Status == entry.IncidentActing
		if standing && (p.NeedsAPerson() || fr.Handoff != "") {
			out.NeedsYou = append(out.NeedsYou, fr)
		}
		out.Feed = append(out.Feed, fr)
	}
	return rows.Err()
}

// fillAutomation counts the window's endings by day, and the headline share
// against the window before it.
//
// Buckets are by the day the incident opened, not the day it closed: the
// question the chart answers is "of what broke that day, how much did the hub
// handle", and an incident that broke on Monday and was closed on Tuesday is
// Monday's work.
func (h *Handlers) fillAutomation(ctx context.Context, out *entryIncidents, now time.Time, window time.Duration) error {
	const q = `
		SELECT to_char(date_trunc('day', i.opened_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,` +
		resolutionExpr + ` AS resolution,
		       count(DISTINCT i.id) AS n
		FROM entry_incidents i
		LEFT JOIN entry_repairs r ON r.incident_id = i.id
		WHERE i.opened_at >= $1
		GROUP BY day, i.id, i.handoff, i.status
		ORDER BY day
	`
	// One row per incident, summed into days here. The grouping has to be per
	// incident because the classification is: a count over the joined rows
	// would count repairs rather than incidents, and an aggregate of the CASE
	// would be the duplication resolutionExpr exists to prevent. So `n` is
	// always 1 and the arithmetic is in addResolution.
	since := now.Add(-window)
	before := now.Add(-2 * window)
	chartSince := now.AddDate(0, 0, -incidentDays)
	from := before
	if chartSince.Before(from) {
		from = chartSince
	}

	rows, err := h.pool.Query(ctx, q, from)
	if err != nil {
		return fmt.Errorf("automation: %w", err)
	}
	defer rows.Close()

	byDay := map[string]*resolutionCounts{}
	var inWindow, inBefore resolutionCounts
	for rows.Next() {
		var day, resolution string
		var n int
		if err := rows.Scan(&day, &resolution, &n); err != nil {
			return fmt.Errorf("scan automation row: %w", err)
		}
		if _, ok := byDay[day]; !ok {
			byDay[day] = &resolutionCounts{}
		}
		addResolution(byDay[day], resolution, n)
		// Which of the two windows this row falls in is decided from the day
		// it is in, which is why the query hands the day back rather than the
		// timestamp: a chart point and a headline figure disagreeing by one
		// row would be unexplainable on screen.
		if d, err := time.Parse("2006-01-02", day); err == nil {
			switch {
			case !d.Before(truncDay(since)):
				addResolution(&inWindow, resolution, n)
			case !d.Before(truncDay(before)):
				addResolution(&inBefore, resolution, n)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	days := make([]incidentDay, 0, incidentDays+1)
	for d := truncDay(chartSince); !d.After(truncDay(now)); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		point := incidentDay{Day: key}
		if c, ok := byDay[key]; ok {
			point.resolutionCounts = *c
		}
		days = append(days, point)
	}

	out.Automation = automationBlock{
		AutoPct:       autoShare(inWindow),
		AutoPctBefore: autoShare(inBefore),
		Total:         inWindow.Auto + inWindow.Handed + inWindow.Failed + inWindow.Dismissed + inWindow.Working,
		By:            inWindow,
		Days:          days,
	}
	return nil
}

// autoShare is the share of ended incidents the autopilot closed by itself.
//
// Over the ended ones only: an incident still being worked on is not yet
// anybody's success, and counting it as a failure would make the figure sag
// every time something broke a minute ago.
func autoShare(c resolutionCounts) *float64 {
	ended := c.Auto + c.Handed + c.Failed + c.Dismissed
	if ended == 0 {
		return nil
	}
	pct := round2(float64(c.Auto) * 100 / float64(ended))
	return &pct
}

func addResolution(c *resolutionCounts, resolution string, n int) {
	switch resolution {
	case "auto":
		c.Auto += n
	case "handed":
		c.Handed += n
	case "failed":
		c.Failed += n
	case "dismissed":
		c.Dismissed += n
	default:
		c.Working += n
	}
}

func truncDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func isoOrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// fillStages computes the two medians Postgres can answer on its own.
//
// Time to repair ends at the admission where there is one and at the closing
// otherwise. That is not a fallback to a worse number: a repair closes its
// incident optimistically and the admission is when the replacement actually
// carried traffic, so an incident that never got one never had a replacement
// land, and its closing is the honest end of it.
func (h *Handlers) fillStages(ctx context.Context, out *entryIncidents, since time.Time) error {
	const q = `
		SELECT
			percentile_cont(0.5) WITHIN GROUP (
				ORDER BY extract(epoch FROM acted_at - opened_at)
			) FILTER (WHERE acted_at IS NOT NULL),
			count(*) FILTER (WHERE acted_at IS NOT NULL),
			percentile_cont(0.5) WITHIN GROUP (
				ORDER BY extract(epoch FROM coalesce(admitted_at, closed_at) - acted_at)
			) FILTER (WHERE acted_at IS NOT NULL AND closed_at IS NOT NULL),
			count(*) FILTER (WHERE acted_at IS NOT NULL AND closed_at IS NOT NULL)
		FROM entry_incidents
		WHERE opened_at >= $1
	`
	var decide, repair *float64
	var decideN, repairN int
	if err := h.pool.QueryRow(ctx, q, since).Scan(&decide, &decideN, &repair, &repairN); err != nil {
		return fmt.Errorf("stages: %w", err)
	}
	out.Stages.ToDecideSec, out.Stages.DecideSamples = roundPtr(decide), decideN
	out.Stages.ToRepairSec, out.Stages.RepairSamples = roundPtr(repair), repairN
	return nil
}

// fillPauses reads the standing wave fuses.
func (h *Handlers) fillPauses(ctx context.Context, out *entryIncidents) error {
	const q = `
		SELECT id::text, cause, reason, boxes, paused_at
		FROM entry_cause_pauses WHERE lifted_at IS NULL
		ORDER BY paused_at ASC
	`
	rows, err := h.pool.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("pauses: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p entry.CausePause
		if err := rows.Scan(&p.ID, &p.Cause, &p.Reason, &p.Boxes, &p.PausedAt); err != nil {
			return fmt.Errorf("scan pause: %w", err)
		}
		out.Pauses = append(out.Pauses, p)
	}
	return rows.Err()
}

// fillTimeToNotice is the screen's one join of the two stores: how long the
// failure was already visible in the probes before the hub opened an incident
// for it.
//
// Returns the reason the figure is absent, or "" when it filled it. It never
// fails the request — a screen with two of three medians is useful, and a
// ClickHouse that cannot answer is a missing figure and not an error.
//
// # What counts as "the first failing probe"
//
// A failure streak's start: the sample where a (relay, vantage) pair went from
// reading clean to reading anything else. Not simply the earliest failing
// sample in the lookback, which for a relay that has been failing for days
// would be the start of the whole episode rather than of this one.
//
// The earliest such start across the box's relays within [detectLookback] of
// the opening is the moment the evidence appeared. A box with no streak start
// in that stretch contributes nothing — the probes do not say when its failure
// began, and inventing a zero would make the median flatter the blinder the
// hub was.
func (h *Handlers) fillTimeToNotice(ctx context.Context, out *entryIncidents, since time.Time) string {
	if h.analytics == nil {
		return "аналитика не подключена: первый провал пробы лежит в ClickHouse, без него «до обнаружения» не посчитать"
	}
	relayBox, err := h.relayBoxes(ctx)
	if err != nil {
		log.Printf("admin entry incidents: relay→box: %v", err)
		return "не удалось прочитать, какой релей на какой машине — без этого провал пробы не привязать к инциденту"
	}
	if len(relayBox) == 0 {
		return "в пуле нет релеев: привязать провал пробы к машине нечем"
	}

	// The streak starts, oldest first, over the window plus the lookback: an
	// incident opened a minute into the window has its first failure just
	// before it.
	const q = `
		SELECT relay_name, ts FROM (
			SELECT
				relay_name, source_region, ts, verdict,
				lagInFrame(toString(verdict)) OVER (
					PARTITION BY relay_name, source_region ORDER BY ts
					ROWS BETWEEN 1 PRECEDING AND CURRENT ROW
				) AS prev
			FROM entry_probes
			WHERE ts >= ?
		)
		WHERE verdict NOT IN ('clean', 'unknown') AND prev IN ('clean', 'unknown')
		ORDER BY ts
	`
	rows, err := h.analytics.Query(ctx, q, since.Add(-detectLookback))
	if err != nil {
		log.Printf("admin entry incidents: probe streaks: %v", err)
		return "поток проб не отдал начала провалов: «до обнаружения» нечем считать"
	}
	defer rows.Close()

	// Per box, every moment at which a failure began. Kept in order, so the
	// lookup below is a scan backwards from the opening.
	starts := map[string][]time.Time{}
	for rows.Next() {
		var relay string
		var ts time.Time
		if err := rows.Scan(&relay, &ts); err != nil {
			log.Printf("admin entry incidents: scan streak: %v", err)
			return "поток проб отдал начала провалов в неожиданном виде"
		}
		box, ok := relayBox[relay]
		if !ok {
			// A relay that is no longer in the pool. Its probes are still in
			// ClickHouse and there is no box to attribute them to.
			continue
		}
		starts[box] = append(starts[box], ts.UTC())
	}
	if err := rows.Err(); err != nil {
		log.Printf("admin entry incidents: iterate streaks: %v", err)
		return "поток проб оборвался на середине: «до обнаружения» считать не на чем"
	}
	if len(starts) == 0 {
		return "за окно в пробах нет ни одного начала провала — считать «до обнаружения» нечего"
	}

	lags := []float64{}
	for _, fr := range out.Feed {
		opened, err := time.Parse(time.RFC3339, fr.OpenedAt)
		if err != nil {
			continue
		}
		if opened.Before(since) {
			// A standing incident older than the window. It is in the feed
			// because a queue must not hide anything; it is not in this
			// median, which is about the window.
			continue
		}
		first, ok := earliestStart(starts[fr.Box], opened)
		if !ok {
			continue
		}
		lags = append(lags, opened.Sub(first).Seconds())
	}
	if len(lags) == 0 {
		return "ни у одного инцидента окна в пробах нет начала провала в пределах получаса до него — привязать нечего"
	}
	out.Stages.ToNoticeSec, out.Stages.NoticeSamples = roundPtr(median(lags)), len(lags)
	return ""
}

// earliestStart finds the beginning of the failure the incident was opened
// for: the earliest streak start inside [opened-detectLookback, opened].
//
// Earliest and not latest: a box whose relays started failing one after
// another over two minutes was broken when the first of them went, and the
// hub's delay is measured from the evidence it could have acted on.
func earliestStart(starts []time.Time, opened time.Time) (time.Time, bool) {
	floor := opened.Add(-detectLookback)
	for _, ts := range starts { // oldest first, from the query's ORDER BY
		if ts.Before(floor) {
			continue
		}
		if ts.After(opened) {
			break
		}
		return ts, true
	}
	return time.Time{}, false
}

// relayBoxes maps a relay's name to the machine it runs on. Names only — the
// address is sealed and this screen has no business opening it.
func (h *Handlers) relayBoxes(ctx context.Context) (map[string]string, error) {
	rows, err := h.pool.Query(ctx, `SELECT name, box FROM entry_relays`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, box string
		if err := rows.Scan(&name, &box); err != nil {
			return nil, err
		}
		out[name] = box
	}
	return out, rows.Err()
}

// median of a non-empty slice. Even counts take the mean of the middle two,
// which is what percentile_cont(0.5) does on the Postgres side — the two
// medians on this screen must be computed the same way or they cannot be read
// side by side.
func median(vs []float64) *float64 {
	if len(vs) == 0 {
		return nil
	}
	sort.Float64s(vs)
	mid := len(vs) / 2
	if len(vs)%2 == 1 {
		return &vs[mid]
	}
	m := (vs[mid-1] + vs[mid]) / 2
	return &m
}

func roundPtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := round2(*v)
	return &r
}
