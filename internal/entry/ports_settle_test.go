package entry

import (
	"context"
	"testing"
	"time"
)

// timedVerdict is one probe row: what the vantage said about a relay, and
// when.
type timedVerdict struct {
	relay, verdict string
	at             time.Time
}

// fakeTimedVerdicts honours the window the way the probe dataset does: the
// newest row per relay inside [since, until]. fakeVerdicts ignores the window,
// and the window is what these tests are about.
type fakeTimedVerdicts struct{ rows []timedVerdict }

func (f *fakeTimedVerdicts) LatestVerdictsByVantage(_ context.Context, since, until time.Time) (map[string]RelayVerdicts, error) {
	latest := map[string]timedVerdict{}
	for _, r := range f.rows {
		if r.at.Before(since) || r.at.After(until) {
			continue
		}
		if l, ok := latest[r.relay]; !ok || r.at.After(l.at) {
			latest[r.relay] = r
		}
	}
	out := RelayVerdicts{}
	for name, r := range latest {
		out[name] = r.verdict
	}
	return map[string]RelayVerdicts{testVantage: out}, nil
}

// twoPortBox is a box with two live relays and a ports_banned incident opened
// on the reading "a banned, b clean", and the probe rows of that round.
func (h *harness) twoPortBox() (box, a, b string, inc Incident, rows *fakeTimedVerdicts) {
	h.t.Helper()
	box, _ = h.box()
	a, b = h.relay(box, KindPersonal), h.relay(box, KindPersonal)
	h.admit(a)
	h.admit(b)
	inc = h.openIncident(box, CausePortsBanned, map[string]string{a: "ip_banned", b: "clean"})
	round := inc.OpenedAt.Add(-30 * time.Second)
	rows = &fakeTimedVerdicts{rows: []timedVerdict{{a, "ip_banned", round}, {b, "clean", round}}}
	h.svc.SetVerdicts(rows)
	h.t.Cleanup(func() { h.svc.SetVerdicts(nil) })
	return box, a, b, inc, rows
}

func (h *harness) relayStatus(name string) string {
	h.t.Helper()
	var status string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT status FROM entry_relays WHERE name = $1`, name).Scan(&status); err != nil {
		h.t.Fatalf("relay %s: %v", name, err)
	}
	return status
}

// repairEnded waits for the incident's repair to leave `running`: the repair
// runs in the background, and its end is a row, not a duration.
func (h *harness) repairEnded(inc Incident) {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var running bool
		if err := h.pool.QueryRow(context.Background(), `
			SELECT count(*) FILTER (WHERE status = 'running') > 0 OR count(*) = 0
			FROM entry_repairs WHERE incident_id = $1`, inc.ID).Scan(&running); err != nil {
			h.t.Fatalf("repair status: %v", err)
		}
		if !running {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatal("the repair did not end in 15 s")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *harness) repairsOn(inc Incident) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM entry_repairs WHERE incident_id = $1`, inc.ID).Scan(&n); err != nil {
		h.t.Fatalf("repairs: %v", err)
	}
	return n
}

// A ban of the whole address that lands while a round walks the box reads as
// some ports banned and others clean. Found on the stand 27.10 (П9.7
// ip-banned, ams6): on that one round the hub retired a port for good and
// raised a new one born banned, two minutes before it named the address. It
// now waits for a round that started after the incident opened, and on that
// round the address reads as the address: nothing is retired.
func TestABanLandingMidRoundRetiresNoPort(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, a, b, inc, rows := h.twoPortBox()
	m := NewMechanic(h.svc, nil)

	if started, handed := m.consider(ctx, inc); started || handed {
		t.Fatalf("acted on the round the incident was opened on: started=%v handed=%v", started, handed)
	}

	next := inc.OpenedAt.Add(30 * time.Second)
	rows.rows = append(rows.rows, timedVerdict{a, "ip_banned", next}, timedVerdict{b, "ip_banned", next})
	if started, handed := m.consider(ctx, inc); started || handed {
		t.Fatalf("retired ports of a box whose every port is banned: started=%v handed=%v", started, handed)
	}
	if n := h.repairsOn(inc); n != 0 {
		t.Fatalf("%d repairs written, want none", n)
	}
	for _, r := range []string{a, b} {
		if s := h.relayStatus(r); s != StatusAlive {
			t.Errorf("relay %s is %s, want alive: the address is the evidence, not this port", r, s)
		}
	}
}

// And a port that is banned on its own still is once the next round confirms
// it: one round later, the banned one is retired and its neighbour carries on.
func TestABannedPortIsRetiredOnceTheNextRoundConfirmsIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, a, b, inc, rows := h.twoPortBox()
	m := NewMechanic(h.svc, nil)

	next := inc.OpenedAt.Add(30 * time.Second)
	rows.rows = append(rows.rows, timedVerdict{a, "ip_banned", next}, timedVerdict{b, "clean", next})
	if started, _ := m.consider(ctx, inc); !started {
		t.Fatal("a port banned on two rounds in a row was not retired")
	}
	h.repairEnded(inc)
	if s := h.relayStatus(a); s != StatusDead {
		t.Errorf("banned relay %s is %s, want dead", a, s)
	}
	if s := h.relayStatus(b); s != StatusAlive {
		t.Errorf("clean relay %s is %s, want alive", b, s)
	}
}
