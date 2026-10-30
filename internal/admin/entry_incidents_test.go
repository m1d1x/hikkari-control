package admin

// entry_incidents_test.go: the incidents screen's reader (plan 42 К2). What is
// tested here is the classification and the medians, because those are the two
// things the screen claims and the two a wrong query would get silently wrong:
// a chart that counts a failed repair as the autopilot's own work would read as
// an autopilot doing better than it is.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/analytics"
)

// seedIncidentBox makes a machine for incidents to be about, and takes it back
// out afterwards. entry_boxes.name is ^[a-z]+[0-9]+$.
func seedIncidentBox(t *testing.T, pool *pgxpool.Pool, tag string) string {
	t.Helper()
	box := fmt.Sprintf("%s%d", tag, time.Now().UnixNano()%1_000_000_000)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
		VALUES ($1, '\x00', 'vultr', 'amsterdam', 'vless-reality-vision')
	`, box); err != nil {
		t.Fatalf("seed box: %v", err)
	}
	t.Cleanup(func() {
		// The incidents go with the box by cascade, and the repairs with them.
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_boxes WHERE name = $1`, box)
	})
	return box
}

// seedIncident writes one incident as the hub would have left it, and returns
// its id. Times are given relative to now so a test can describe a timeline
// without freezing a clock the handler does not take.
type seededIncident struct {
	cause    string
	status   string
	handoff  string
	decided  string
	opened   time.Duration // ago
	acted    *time.Duration
	closed   *time.Duration
	admitted *time.Duration
	repair   string // '' | 'done' | 'failed'
}

func seedIncident(t *testing.T, pool *pgxpool.Pool, box string, s seededIncident) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	at := func(d *time.Duration) *time.Time {
		if d == nil {
			return nil
		}
		ts := now.Add(-*d)
		return &ts
	}
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO entry_incidents (box, cause, status, advice, evidence, handoff,
		                             decided_by, playbook, opened_at, acted_at, closed_at, admitted_at)
		VALUES ($1, $2, $3, 'тест', '{}'::jsonb, $4, $5, '', $6, $7, $8, $9)
		RETURNING id::text
	`, box, s.cause, s.status, s.handoff, s.decided,
		now.Add(-s.opened), at(s.acted), at(s.closed), at(s.admitted)).Scan(&id); err != nil {
		t.Fatalf("seed incident %s: %v", s.cause, err)
	}
	if s.repair != "" {
		finished := now
		if _, err := pool.Exec(ctx, `
			INSERT INTO entry_repairs (incident_id, box, cause, playbook, decided_by, status,
			                           started_at, finished_at)
			VALUES ($1, $2, $3, 'retire_ports', 'auto', $4, $5, $5)
		`, id, box, s.cause, s.repair, finished); err != nil {
			t.Fatalf("seed repair: %v", err)
		}
	}
	return id
}

func readIncidents(t *testing.T, h *Handlers, window string) entryIncidents {
	t.Helper()
	rec := httptest.NewRecorder()
	h.EntryIncidents(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/entry/incidents?window="+window, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got entryIncidents
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func rowOf(feed []incidentFeedRow, id string) (incidentFeedRow, bool) {
	for _, r := range feed {
		if r.ID == id {
			return r, true
		}
	}
	return incidentFeedRow{}, false
}

// The four endings, told apart. A failed repair outranks everything: it is the
// one ending that says the hub was wrong about what to do, and folding it into
// "closed by itself" would make the headline flatter the worse things got.
func TestEntryIncidentsClassifiesHowEachOneEnded(t *testing.T) {
	pool := livePostgres(t)
	box := seedIncidentBox(t, pool, "inca")
	hour := time.Hour
	halfHour := 30 * time.Minute

	auto := seedIncident(t, pool, box, seededIncident{
		cause: "fingerprint", status: "resolved", decided: "auto",
		opened: 2 * hour, acted: &halfHour, closed: &halfHour, repair: "done",
	})
	failed := seedIncident(t, pool, box, seededIncident{
		cause: "ports_banned", status: "resolved", decided: "auto",
		opened: 3 * hour, acted: &hour, closed: &hour, repair: "failed",
	})
	handed := seedIncident(t, pool, box, seededIncident{
		cause: "ip_banned", status: "open", handoff: "пробовали трижды",
		opened: hour,
	})
	dismissed := seedIncident(t, pool, box, seededIncident{
		cause: "destination", status: "dismissed", opened: 4 * hour, closed: &hour,
	})
	working := seedIncident(t, pool, box, seededIncident{
		cause: "provider_down", status: "acting", decided: "auto",
		opened: 10 * time.Minute, acted: &halfHour,
	})

	got := readIncidents(t, &Handlers{pool: pool}, "24h")

	for _, want := range []struct {
		id, resolution string
	}{
		{auto, "auto"},
		{failed, "failed"},
		{handed, "handed"},
		{dismissed, "dismissed"},
		{working, "working"},
	} {
		row, ok := rowOf(got.Feed, want.id)
		if !ok {
			t.Errorf("incident %s is missing from the feed", want.resolution)
			continue
		}
		if row.Resolution != want.resolution {
			t.Errorf("resolution = %q, want %q (incident %s)", row.Resolution, want.resolution, want.id)
		}
	}

	// And the chart's counters have to agree with the rows: they come from the
	// same expression precisely so they cannot drift.
	if got.Automation.By.Failed < 1 || got.Automation.By.Handed < 1 ||
		got.Automation.By.Dismissed < 1 || got.Automation.By.Auto < 1 || got.Automation.By.Working < 1 {
		t.Errorf("the chart's counters do not match the feed: %+v", got.Automation.By)
	}
	if got.Automation.AutoPct == nil {
		t.Fatal("four incidents ended and the headline share is still null")
	}
}

// A standing incident is in the queue whatever its age, and the queue is the
// same judgement the overview makes. Two screens disagreeing about what needs
// a person would make both of them useless.
func TestEntryIncidentsQueueHoldsWhatTheHubCannotFinish(t *testing.T) {
	pool := livePostgres(t)
	box := seedIncidentBox(t, pool, "incb")

	// unknown has no repair the hub knows, so it waits for a person even
	// untouched; fingerprint is a free rotation the hub does alone. (This
	// used domain_blocked until П9.11б made its repair free and `auto`; no
	// cause about one box spends money any more — purchases are the pools'.)
	paid := seedIncident(t, pool, box, seededIncident{
		cause: "unknown", status: "open", opened: time.Hour,
	})
	free := seedIncident(t, pool, box, seededIncident{
		cause: "fingerprint", status: "open", opened: time.Hour,
	})

	got := readIncidents(t, &Handlers{pool: pool}, "24h")

	if _, ok := rowOf(got.NeedsYou, paid); !ok {
		t.Error("an incident the hub cannot repair is not in the queue")
	}
	if _, ok := rowOf(got.NeedsYou, free); ok {
		t.Error("a repair the hub may run alone was put in front of a person")
	}
	if _, ok := rowOf(got.Feed, free); !ok {
		t.Error("the feed is not the whole of it: a standing incident is missing")
	}
}

// Time to repair ends at the admission where there is one: a repair closes its
// incident optimistically, and the moment that matters is when the
// replacement actually carried traffic.
func TestEntryIncidentsMeasuresRepairToTheAdmission(t *testing.T) {
	pool := livePostgres(t)
	box := seedIncidentBox(t, pool, "incc")

	opened := 60 * time.Minute
	acted := 59 * time.Minute  // one minute to decide
	closed := 58 * time.Minute // one minute of repair…
	admitted := 50 * time.Minute

	id := seedIncident(t, pool, box, seededIncident{
		cause: "ports_banned", status: "resolved", decided: "auto",
		opened: opened, acted: &acted, closed: &closed, admitted: &admitted, repair: "done",
	})

	got := readIncidents(t, &Handlers{pool: pool}, "24h")

	row, ok := rowOf(got.Feed, id)
	if !ok {
		t.Fatal("the incident is missing from the feed")
	}
	if row.AdmittedAt == "" {
		t.Error("the admission is not on the row, so the timeline has no last stage")
	}
	if got.Stages.ToDecideSec == nil || got.Stages.ToRepairSec == nil {
		t.Fatalf("the medians Postgres can answer are null: %+v", got.Stages)
	}
	// …so the repair reads as nine minutes (acted → admitted), not one
	// (acted → closed). The median is over whatever else the shared test
	// database holds, so what is asserted is that the admission is the end
	// being measured: the figure cannot be the one-minute closing.
	if *got.Stages.ToRepairSec < 60 {
		t.Errorf("time to repair = %.0fs, measured to the optimistic closing rather than the admission", *got.Stages.ToRepairSec)
	}
	if got.Stages.RepairSamples == 0 {
		t.Error("the median claims no samples while an incident was measured")
	}
}

// Without a probe stream time-to-notice is absent and says why, rather than
// quietly becoming time-to-diagnose. The rule the old system page broke.
func TestEntryIncidentsNamesTimeToNoticeAsMissingWithoutProbes(t *testing.T) {
	pool := livePostgres(t)

	got := readIncidents(t, &Handlers{pool: pool}, "24h") // no analytics store

	if got.Stages.ToNoticeSec != nil {
		t.Error("time to notice was computed with no probe stream to compute it from")
	}
	var named bool
	for _, m := range got.Missing {
		if m.Figure == "stages.toNoticeSec" && m.Why != "" {
			named = true
		}
	}
	if !named {
		t.Errorf("the absent median is not named in missing: %+v", got.Missing)
	}
}

// The menu of repairs is part of the payload: "another action" is a choice
// from what the hub has code for, never a free-form command (К2 §4).
func TestEntryIncidentsOffersThePlaybookMenu(t *testing.T) {
	pool := livePostgres(t)
	got := readIncidents(t, &Handlers{pool: pool}, "7d")
	if len(got.Playbooks) == 0 {
		t.Fatal("the screen would show an empty menu and the operator could only ssh")
	}
	for _, p := range got.Playbooks {
		if p.Name == "" || p.Cause == "" {
			t.Errorf("playbook option is half-filled: %+v", p)
		}
	}
}

// The chart spans its own thirty days whatever the window, because its job is
// the trend and not the headline.
func TestEntryIncidentsChartSpansItsOwnWindow(t *testing.T) {
	pool := livePostgres(t)
	got := readIncidents(t, &Handlers{pool: pool}, "24h")
	if len(got.Automation.Days) < incidentDays {
		t.Fatalf("chart has %d points, want at least %d", len(got.Automation.Days), incidentDays)
	}
	for i := 1; i < len(got.Automation.Days); i++ {
		if got.Automation.Days[i-1].Day >= got.Automation.Days[i].Day {
			t.Fatalf("chart points are not oldest-first around %q", got.Automation.Days[i].Day)
		}
	}
}

// The screen's one join of the two stores: how long the failure was already
// visible in the probes before the hub opened an incident for it.
//
// This is the figure that cannot be derived from Postgres at all, and the one
// that must not degrade into something else when the probes are thin — so the
// test gives it a failure streak to find and checks that the lag is measured
// from the start of the streak and not from the probe before it.
func TestEntryIncidentsMeasuresTimeToNoticeFromTheProbeStream(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	box := seedIncidentBox(t, pool, "incd")
	relay := box + "-1"
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_relays (name, kind, status, box, identity_ct)
		VALUES ($1, 'personal', 'pending', $2, '\x00')
	`, relay, box); err != nil {
		t.Fatalf("seed relay: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_relays WHERE name = $1`, relay)
	})

	// A clean round, then two failing ones: the streak begins at the first
	// failing sample, four minutes before the hub opened its incident.
	base := time.Now().UTC().Truncate(time.Second).Add(-20 * time.Minute)
	probe := func(at time.Time, verdict string, realityOK uint8) analytics.EntryProbeRecord {
		return analytics.EntryProbeRecord{
			Timestamp: at, SourceRegion: "ru-test", RelayName: relay,
			RawTCP: 1, RealityOK: realityOK, K1BareTLS: realityOK, K2Control: 1,
			Cover: "example.com", Verdict: verdict,
		}
	}
	if err := store.InsertEntryProbes(ctx, []analytics.EntryProbeRecord{
		probe(base, "clean", 1),
		probe(base.Add(2*time.Minute), "ip_banned", 0),
		probe(base.Add(3*time.Minute), "ip_banned", 0),
	}); err != nil {
		t.Fatalf("insert probes: %v", err)
	}
	waitRows(t, store, relay, 3)

	// Opened four minutes after the streak began: that is the lag.
	openedAgo := 20*time.Minute - 6*time.Minute
	seedIncident(t, pool, box, seededIncident{
		cause: "ip_banned", status: "open", opened: openedAgo,
	})

	got := readIncidents(t, &Handlers{pool: pool, analytics: store}, "24h")

	if got.Stages.ToNoticeSec == nil {
		t.Fatalf("time to notice is absent with a streak to measure: missing=%+v", got.Missing)
	}
	if got.Stages.NoticeSamples == 0 {
		t.Error("the median claims no samples while an incident was measured")
	}
	// Four minutes, give or take the second the timestamps were truncated to.
	// What this rules out is the two obvious wrong answers: measuring from the
	// clean sample before the streak (six minutes) or from the latest failing
	// one (three).
	if *got.Stages.ToNoticeSec < 180 || *got.Stages.ToNoticeSec > 300 {
		t.Errorf("time to notice = %.0fs, want about 240 — measured from the wrong sample", *got.Stages.ToNoticeSec)
	}
	for _, m := range got.Missing {
		if m.Figure == "stages.toNoticeSec" {
			t.Errorf("the figure was computed and still named missing: %s", m.Why)
		}
	}
}
