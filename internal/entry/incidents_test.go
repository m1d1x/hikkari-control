package entry

import (
	"context"
	"testing"
	"time"
)

// fakeVerdicts answers with whatever the test set, so the detector can be
// exercised without a probe dataset.
type fakeVerdicts struct {
	v   RelayVerdicts
	err error
}

// The test vantage is the one observation point that saw them.
func (f *fakeVerdicts) LatestVerdictsByVantage(context.Context, time.Time, time.Time) (map[string]RelayVerdicts, error) {
	return map[string]RelayVerdicts{testVantage: f.v}, f.err
}

// A diagnosis that needs nobody opens nothing. Most boxes are fine most of
// the time, and saying so has to be free — a feed that grows on healthy boxes
// teaches its reader to ignore it.
func TestOpeningAHealthyDiagnosisRecordsNothing(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()

	inc, err := h.svc.Open(context.Background(), Diagnosis{Box: box, Cause: CauseHealthy})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if inc != nil {
		t.Fatalf("a healthy box opened incident %s", inc.ID)
	}
}

// The detector runs on a timer and says the same thing every minute. One open
// incident per (box, cause), or the console fills with duplicates of one
// problem.
func TestOpeningTheSameCauseTwiceKeepsOneIncident(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	d := Diagnosis{
		Box: box, Cause: CauseIPBanned, Advice: "сменить ip",
		Verdicts: map[string]string{box + "-1": "ip_banned"},
	}

	first, err := h.svc.Open(context.Background(), d)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	second, err := h.svc.Open(context.Background(), d)
	if err != nil {
		t.Fatalf("open again: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("two incidents for one standing cause: %s and %s", first.ID, second.ID)
	}
	if first.Evidence[box+"-1"] != "ip_banned" {
		t.Errorf("evidence did not survive: %v", first.Evidence)
	}
}

// Deciding and acting are two moments, so time-to-repair splits into
// time-to-decide and time-to-act — two different answers with two different
// remedies when the chaos run asks whether it was fast enough.
func TestActingOnAnIncidentRecordsWhoDecidedAndWhen(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()

	inc, err := h.svc.Open(ctx, Diagnosis{Box: box, Cause: CauseFingerprint, Advice: "сменить ключ"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if inc.ActedAt != nil {
		t.Fatal("a fresh incident already claims it was acted on")
	}
	if err := h.svc.Act(ctx, inc.ID, PlaybookRotateReality, DecidedAuto); err != nil {
		t.Fatalf("act: %v", err)
	}

	got, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Status != IncidentActing || got.Playbook != PlaybookRotateReality || got.DecidedBy != DecidedAuto {
		t.Fatalf("after acting: %+v", got)
	}
	if got.ActedAt == nil {
		t.Error("the moment of deciding was not recorded")
	}

	// A made-up decider is refused: the column is the dataset П9.8 reads, and
	// a value nobody defined would quietly corrupt it.
	if err := h.svc.Act(ctx, inc.ID, PlaybookRotateReality, "whoever"); err == nil {
		t.Error("an undefined decider was accepted")
	}
}

// Closing says how it ended. An incident cannot be closed twice, and cannot
// be closed into a state that is not an ending.
func TestClosingAnIncidentIsFinalAndNamed(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	ctx := context.Background()

	inc, err := h.svc.Open(ctx, Diagnosis{Box: box, Cause: CauseDomainBlocked, Advice: "новый домен"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := h.svc.Close(ctx, inc.ID, "sleeping", "", 0); err == nil {
		t.Error("an incident was closed into a state that is not an ending")
	}
	if err := h.svc.Close(ctx, inc.ID, IncidentResolved, "куплен новый домен", 913); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := h.svc.Close(ctx, inc.ID, IncidentResolved, "ещё раз", 0); err == nil {
		t.Error("a closed incident was closed again")
	}

	got, err := h.svc.Incident(ctx, inc.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Status != IncidentResolved || got.CostCents != 913 || got.ClosedAt == nil {
		t.Fatalf("after closing: %+v", got)
	}
}

// A box that heals closes its own incident — and leaves the record that it
// was broken, which is the thing the dataset is for.
func TestDetectorClosesWhatRecoveredAndKeepsTheRecord(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.admit(relay)
	// A Reality box holds its spare port (ports.go); without it the recovered
	// box would read short_of_ports and open an incident of its own.
	spare := h.relay(box, KindPersonal)
	h.admit(spare)

	sick := &fakeVerdicts{v: RelayVerdicts{relay: "ip_banned", spare: "ip_banned"}}
	h.svc.SetVerdicts(sick)

	opened, _, err := h.svc.Detect(ctx)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if opened == 0 {
		t.Fatal("a banned box opened no incident")
	}

	// The box recovers; the next pass must notice without anybody asking.
	sick.v = RelayVerdicts{relay: "clean", spare: "clean"}
	_, closed, err := h.svc.Detect(ctx)
	if err != nil {
		t.Fatalf("detect again: %v", err)
	}
	if closed == 0 {
		t.Fatal("the box recovered and its incident still stands")
	}

	feed, err := h.svc.Incidents(ctx, 200)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	for _, inc := range feed {
		if inc.Box == box {
			if inc.Status != IncidentResolved {
				t.Fatalf("incident for the recovered box is %q", inc.Status)
			}
			if inc.Outcome == "" {
				t.Error("the incident closed without saying why")
			}
			return
		}
	}
	t.Fatal("the record of the outage is gone from the feed")
}

// Our own blindness must not open anything: acting on it would be acting on
// noise, and retiring healthy machines on the strength of our outage.
func TestDetectorOpensNothingWhenTheVantageIsBlind(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.admit(relay)

	h.svc.SetVerdicts(&fakeVerdicts{v: RelayVerdicts{relay: "network_down"}})
	opened, _, err := h.svc.Detect(context.Background())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if opened != 0 {
		t.Fatalf("our own outage opened %d incident(s)", opened)
	}
}

// With no probe dataset the detector does nothing, rather than diagnosing
// every box as unjudged and filling the console with "no evidence".
func TestDetectorWithoutAVerdictSourceDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.svc.SetVerdicts(nil)
	opened, closed, err := h.svc.Detect(context.Background())
	if err != nil || opened != 0 || closed != 0 {
		t.Fatalf("opened %d, closed %d, err %v — want a no-op", opened, closed, err)
	}
}

// The dispatcher's table, which is the owner's rule of 22.10 written once:
// free repairs run unattended, anything that spends waits for a person.
func TestPlaybooksSpendNothingUnattended(t *testing.T) {
	for _, cause := range []string{CauseDomainBlocked, CauseFingerprint, CausePortsBanned, CauseIPBanned} {
		p := PlaybookForCause(cause)
		if p.Name == "" {
			t.Errorf("%s: no playbook at all", cause)
			continue
		}
		if p.Spends && p.MayRunUnattended() {
			t.Errorf("%s: %s spends money and may run without asking", cause, p.Name)
		}
		if !p.Spends && !p.MayRunUnattended() {
			t.Errorf("%s: %s is free and still waits for a person", cause, p.Name)
		}
	}
}

// A cause nobody wrote a repair for is handed to a person, not to the
// nearest-looking playbook.
func TestAnUnknownCauseProposesNoRepair(t *testing.T) {
	p := PlaybookForCause("something-new")
	if p.Name != "" {
		t.Fatalf("an unknown cause proposed %q", p.Name)
	}
	if p.MayRunUnattended() {
		t.Fatal("an unknown cause may be acted on unattended")
	}
	if !p.NeedsAPerson() {
		t.Fatal("an unknown cause is not put in front of anybody")
	}
}
