package entry

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// recordingSink keeps what RecordReach handed the dataset.
type recordingSink struct {
	rows  []ReachRecord
	calls int
	err   error
}

func (s *recordingSink) InsertEntryReach(_ context.Context, rows []ReachRecord) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.rows = append(s.rows, rows...)
	return nil
}

// reachService builds a service with no database: RecordReach touches the sink
// and the clock, and nothing else. The batch and reach keys come from the
// ephemeral content key TestMain configures.
func reachService(t *testing.T, now time.Time) (*Service, *recordingSink) {
	t.Helper()
	svc, err := NewService(nil, nil, 0)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	svc.now = func() time.Time { return now }
	sink := &recordingSink{}
	svc.SetReachSink(sink)
	return svc, sink
}

func TestReachKeepsTheThreeOutcomesAndClampsWhatAClientClaims(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	svc, sink := reachService(t, now)
	account := uuid.New()

	kept, err := svc.RecordReach(context.Background(), account, []ReachReport{
		{Outcome: ReachDirect, ObservedAt: now.Add(-time.Minute), ConnectMS: 180},
		{Outcome: ReachRelay, Relay: "ams1-1", RelaysTried: 2, ConnectMS: 1400, RTTMS: 48, ObservedAt: now},
		{Outcome: ReachFailed, RelaysTried: 999, ConnectMS: 99_999_999, ObservedAt: now},
	})
	if err != nil {
		t.Fatalf("record reach: %v", err)
	}
	if kept != 3 {
		t.Fatalf("kept = %d, want 3", kept)
	}
	if len(sink.rows) != 3 {
		t.Fatalf("sink took %d rows, want 3", len(sink.rows))
	}

	// The claims a client controls are clamped before the dataset sees them,
	// so no query over entry_reach has to defend itself against one liar.
	failed := sink.rows[2]
	if failed.RelaysTried != maxRelaysTried {
		t.Errorf("relays tried = %d, want clamped to %d", failed.RelaysTried, maxRelaysTried)
	}
	if failed.ConnectMS != maxReachMillis {
		t.Errorf("connect ms = %d, want clamped to %d", failed.ConnectMS, maxReachMillis)
	}
}

func TestReachAttributesARelayOnlyToARelayOutcome(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	svc, sink := reachService(t, now)

	// A name on a direct attempt would credit a relay that carried nothing;
	// a relay outcome without a valid name says nothing at all.
	kept, err := svc.RecordReach(context.Background(), uuid.New(), []ReachReport{
		{Outcome: ReachDirect, Relay: "ams1-1", ObservedAt: now},
		{Outcome: ReachFailed, Relay: "ams1-1", ObservedAt: now},
		{Outcome: ReachRelay, Relay: "NOT A NAME", ObservedAt: now},
		{Outcome: "made up", ObservedAt: now},
	})
	if err != nil {
		t.Fatalf("record reach: %v", err)
	}
	if kept != 2 {
		t.Fatalf("kept = %d, want 2 (the two valid rows, stripped)", kept)
	}
	for i, row := range sink.rows {
		if row.Relay != "" {
			t.Errorf("row %d kept relay %q on a %s outcome", i, row.Relay, row.Outcome)
		}
	}
}

func TestReachClampsAClientsClockAndCapsOneBatch(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	svc, sink := reachService(t, now)

	reports := []ReachReport{
		// Tomorrow and last year both land on receipt time: a client's clock
		// must not be able to write into a window the console already drew.
		{Outcome: ReachDirect, ObservedAt: now.Add(24 * time.Hour)},
		{Outcome: ReachDirect, ObservedAt: now.Add(-365 * 24 * time.Hour)},
	}
	// Past the cap, so the overflow is dropped rather than refused.
	for i := 0; i < MaxReachReports; i++ {
		reports = append(reports, ReachReport{Outcome: ReachFailed, ObservedAt: now})
	}

	kept, err := svc.RecordReach(context.Background(), uuid.New(), reports)
	if err != nil {
		t.Fatalf("record reach: %v", err)
	}
	if kept != MaxReachReports {
		t.Fatalf("kept = %d, want the cap %d", kept, MaxReachReports)
	}
	for i := 0; i < 2; i++ {
		if !sink.rows[i].TS.Equal(now) {
			t.Errorf("row %d ts = %v, want clamped to %v", i, sink.rows[i].TS, now)
		}
	}
}

func TestReachCarriesAPseudonymStablePerAccountAndNeverTheAccountID(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	svc, sink := reachService(t, now)
	alice, bob := uuid.New(), uuid.New()

	for _, who := range []uuid.UUID{alice, alice, bob} {
		if _, err := svc.RecordReach(context.Background(), who, []ReachReport{
			{Outcome: ReachDirect, ObservedAt: now},
		}); err != nil {
			t.Fatalf("record reach: %v", err)
		}
	}

	if sink.rows[0].Account != sink.rows[1].Account {
		t.Error("the same account must map to the same pseudonym, or people cannot be counted")
	}
	if sink.rows[0].Account == sink.rows[2].Account {
		t.Error("two accounts must not share a pseudonym")
	}
	for _, row := range sink.rows {
		if row.Account == alice.String() || row.Account == bob.String() {
			t.Fatalf("the dataset holds an account id (%q), not a pseudonym", row.Account)
		}
	}
}

func TestReachWithoutASinkKeepsNothingAndFailsNothing(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	svc, _ := reachService(t, now)
	svc.SetReachSink(nil)

	kept, err := svc.RecordReach(context.Background(), uuid.New(), []ReachReport{
		{Outcome: ReachDirect, ObservedAt: now},
	})
	if err != nil {
		t.Fatalf("a hub without analytics must not fail the request: %v", err)
	}
	if kept != 0 {
		t.Fatalf("kept = %d, want 0", kept)
	}
}

func TestReachSendsNothingWhenEveryRowWasDropped(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	svc, sink := reachService(t, now)

	kept, err := svc.RecordReach(context.Background(), uuid.New(), []ReachReport{
		{Outcome: "", ObservedAt: now},
		{Outcome: ReachRelay, Relay: "", ObservedAt: now},
	})
	if err != nil {
		t.Fatalf("record reach: %v", err)
	}
	if kept != 0 {
		t.Fatalf("kept = %d, want 0", kept)
	}
	if sink.calls != 0 {
		t.Errorf("the sink was called %d times for an empty batch", sink.calls)
	}
}
