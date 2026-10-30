package entry

// covers_test.go covers the second half of a dead cover (roadmap 42 П9.7,
// П9.11): the box that rotate_dest narrowed is noticed short of its covers
// once the second chance cleared the dropped one, and the proven ones that
// answer are written back in race order.

import (
	"context"
	"strings"
	"testing"
)

// A relay racing two of the three proven covers is short, and only on a box
// that is otherwise healthy.
func TestABoxWithANarrowedRelayIsShortOfCovers(t *testing.T) {
	two := []string{"www.samsung.com", "www.tesla.com"}
	relays := []PoolRelay{
		{Name: "b-1", Status: StatusAlive, Proto: ProtoVlessRealityVision, Covers: DefaultCovers},
		{Name: "b-2", Status: StatusAlive, Proto: ProtoVlessRealityVision, Covers: two},
	}
	d := shortOfCovers(Diagnosis{Box: "b", Cause: CauseHealthy}, relays, coverLedger{})
	if d.Cause != CauseShortOfCovers {
		t.Fatalf("cause = %q, want %q", d.Cause, CauseShortOfCovers)
	}
	if !strings.Contains(d.Advice, "b-2") {
		t.Errorf("advice %q does not name the relay", d.Advice)
	}

	// A real failure is that failure first.
	if d := shortOfCovers(Diagnosis{Box: "b", Cause: CauseIPBanned}, relays, coverLedger{}); d.Cause != CauseIPBanned {
		t.Errorf("cause = %q, a banned box must stay banned", d.Cause)
	}
	// And a missing port is fixed before a missing cover.
	if d := shortOfCovers(Diagnosis{Box: "b", Cause: CauseShortOfPorts}, relays, coverLedger{}); d.Cause != CauseShortOfPorts {
		t.Errorf("cause = %q, short of ports goes first", d.Cause)
	}
}

// A missing cover still under suspicion is not an incident: nothing can be
// restored yet, and an incident would only feed add_covers refusals into the
// strikes until the box went to a person for good. One struck by its failed
// second chance is not missing at all: the proven set shrank.
func TestAMissingCoverUnderSuspicionOrStruckIsNotAnIncident(t *testing.T) {
	relays := []PoolRelay{
		{Name: "b-1", Status: StatusAlive, Proto: ProtoVlessRealityVision,
			Covers: []string{"www.samsung.com", "www.tesla.com"}},
	}
	for _, state := range []string{suspectOpen, suspectDead} {
		l := coverLedger{"www.nvidia.com": state}
		if d := shortOfCovers(Diagnosis{Box: "b", Cause: CauseHealthy}, relays, l); d.Cause != CauseHealthy {
			t.Fatalf("cause = %q with the missing cover %s, want healthy", d.Cause, state)
		}
	}
	if got := (coverLedger{"www.nvidia.com": suspectDead}).proven(); strings.Join(got, ",") != "www.samsung.com,www.tesla.com" {
		t.Errorf("proven = %v, want the struck cover gone and the race order kept", got)
	}
	if got := (coverLedger{"www.nvidia.com": suspectOpen}).proven(); len(got) != len(DefaultCovers) {
		t.Errorf("proven = %v, a suspect cover is still proven", got)
	}
}

// The hub dials each host once a pass, however many ask for it.
func TestTheCoverDialerAsksEachHostOnce(t *testing.T) {
	calls := 0
	s := &Service{coverAnswers: func(string) bool { calls++; return true }}
	dial := s.coverDialer()
	dial("www.nvidia.com")
	dial("www.nvidia.com")
	if calls != 1 {
		t.Fatalf("dialled %d times, want 1", calls)
	}
}

// What the box cannot say is not missing: a dead relay, one the server key
// cannot open, and a protocol with no covers at all never make it short.
func TestOnlyALiveReadableRealityRelayCountsAsShort(t *testing.T) {
	one := []string{"www.samsung.com"}
	relays := []PoolRelay{
		{Name: "b-1", Status: StatusDead, Proto: ProtoVlessRealityVision, Covers: one},
		{Name: "b-2", Status: StatusAlive, Proto: ProtoVlessRealityVision, Unreadable: true},
		{Name: "b-3", Status: StatusAlive, Proto: ProtoVlessXhttp},
		{Name: "b-4", Status: StatusAlive, Proto: ProtoVlessRealityVision, Covers: DefaultCovers},
	}
	if d := shortOfCovers(Diagnosis{Box: "b", Cause: CauseHealthy}, relays, coverLedger{}); d.Cause != CauseHealthy {
		t.Fatalf("cause = %q, want healthy", d.Cause)
	}
}

// The revived cover goes back into its place in the race, not at the end:
// the order is the proven one, and a client races it as written.
func TestRestoreCoversPutsARevivedCoverBackInRaceOrder(t *testing.T) {
	restored, added := restoreCovers(
		[]string{"www.samsung.com", "www.tesla.com"},
		map[string]bool{"www.nvidia.com": true},
	)
	if strings.Join(restored, ",") != strings.Join(DefaultCovers, ",") {
		t.Fatalf("restored = %v, want %v", restored, DefaultCovers)
	}
	if strings.Join(added, ",") != "www.nvidia.com" {
		t.Fatalf("added = %v, want the revived one", added)
	}
}

// A cover still dead stays out, one never asked about stays out too, and a
// name from outside the proven set is kept where it was — removing it is not
// this repair's decision.
func TestRestoreCoversAddsOnlyWhatAnswers(t *testing.T) {
	restored, added := restoreCovers(
		[]string{"www.samsung.com", "cover.example"},
		map[string]bool{"www.nvidia.com": false},
	)
	if strings.Join(restored, ",") != "www.samsung.com,cover.example" {
		t.Fatalf("restored = %v", restored)
	}
	if len(added) != 0 {
		t.Fatalf("added = %v, want nothing", added)
	}
}

// The detector reads the narrowed relay off the pool and opens the incident
// the free repair answers, without the grid looking at a healthy box.
func TestTheDetectorOpensShortOfCoversOnANarrowedRelay(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	a := h.relay(box, KindPersonal)
	h.admit(a)
	b := h.relay(box, KindPersonal)
	h.admit(b)

	id, err := h.svc.RelayIdentity(ctx, b)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	narrowed, err := id.withCovers([]string{"www.samsung.com", "www.tesla.com"})
	if err != nil {
		t.Fatalf("withCovers: %v", err)
	}
	if err := h.svc.setRelayIdentity(ctx, b, narrowed); err != nil {
		t.Fatalf("set identity: %v", err)
	}

	// While the dropped cover is suspect the box is merely healthy — and the
	// detector does not dial it: the second chance does, on its own clock.
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO entry_suspects (kind, subject, reason) VALUES ('cover', 'www.nvidia.com', 'destination')`); err != nil {
		t.Fatalf("suspect cover: %v", err)
	}
	h.svc.coverAnswers = func(string) bool { t.Error("the detector dialled a cover"); return true }
	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{a: "clean", b: "clean"}})
	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect: %v", err)
	}
	if before, err := h.svc.Incidents(ctx, 200); err != nil {
		t.Fatalf("incidents: %v", err)
	} else {
		for _, inc := range before {
			if inc.Box == box && inc.Status == IncidentOpen {
				t.Fatalf("incident %s opened while the missing cover was still suspect", inc.Cause)
			}
		}
	}

	// Its second chance clears it.
	if _, err := h.pool.Exec(ctx, `
		UPDATE entry_suspects SET status = 'returned', ended_at = now()
		WHERE kind = 'cover' AND subject = 'www.nvidia.com' AND status = 'suspect'`); err != nil {
		t.Fatalf("return cover: %v", err)
	}
	if _, _, err := h.svc.Detect(ctx); err != nil {
		t.Fatalf("detect: %v", err)
	}
	incs, err := h.svc.Incidents(ctx, 200)
	if err != nil {
		t.Fatalf("incidents: %v", err)
	}
	var got *Incident
	for i := range incs {
		if incs[i].Box == box && incs[i].Status == IncidentOpen {
			got = &incs[i]
		}
	}
	if got == nil || got.Cause != CauseShortOfCovers {
		t.Fatalf("open incident on %s = %+v, want %s", box, got, CauseShortOfCovers)
	}
	if p := PlaybookForCause(got.Cause); p.Name != PlaybookAddCovers || !p.MayRunUnattended() || p.Spends {
		t.Errorf("playbook = %+v, want add_covers, free and unattended", p)
	}

	// The covers come back (as add_covers would write them) and the next
	// round closes it as recovered.
	if err := h.svc.setRelayIdentity(ctx, b, id); err != nil {
		t.Fatalf("restore identity: %v", err)
	}
	if _, closed, err := h.svc.Detect(ctx); err != nil || closed == 0 {
		t.Fatalf("detect after restore: closed %d, err %v", closed, err)
	}
}
