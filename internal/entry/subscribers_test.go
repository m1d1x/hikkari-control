package entry

import (
	"context"
	"testing"
)

type liveCount int64

func (n liveCount) CountLive(context.Context) (int64, error) { return int64(n), nil }

// The floor: nothing without subscriptions, the batch for the first, and
// one machine per SubscribersPerBox shares after that.
func TestTheSubscriberFloor(t *testing.T) {
	for _, c := range []struct {
		live int64
		want int
	}{{0, 0}, {1, 2}, {75, 2}, {76, 2}, {150, 2}, {151, 3}, {1000, 14}} {
		if got := subscriberFloor(c.live, 2); got != c.want {
			t.Errorf("%d subscriptions: %d machines, want %d", c.live, got, c.want)
		}
	}
}

// Subscriptions raise a stream pool above what its traffic asks for, never
// lower it, and leave voice pools alone.
func TestSubscriptionsRaiseAStreamPool(t *testing.T) {
	s := &Service{batchSize: 2, subscribers: liveCount(400)}
	f := FleetForecast{Target: 1, Keep: 1, TargetWhy: "traffic"}
	if err := s.applySubscriberFloor(context.Background(), &f, ProtoVlessXhttp); err != nil {
		t.Fatal(err)
	}
	if f.Target != 6 || f.Keep != 6 {
		t.Fatalf("400 subscriptions: target %d keep %d, want 6", f.Target, f.Keep)
	}
	f = FleetForecast{Target: 9, Keep: 12}
	_ = s.applySubscriberFloor(context.Background(), &f, ProtoVlessXhttp)
	if f.Target != 9 || f.Keep != 12 {
		t.Fatalf("a busier pool was lowered: %+v", f)
	}
	f = FleetForecast{Target: 1, Keep: 1}
	_ = s.applySubscriberFloor(context.Background(), &f, ProtoHysteria2)
	if f.Target != 1 {
		t.Fatalf("a voice pool was sized by subscriptions: %+v", f)
	}
}

// A batch never puts two of its relays on one machine: they would be one
// address, and a batch is there to outlive an address.
func TestABatchSpansDistinctMachines(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	a, b := h.relay(box, KindPersonal), h.relay(box, KindPersonal)
	h.admit(a)
	h.admit(b)
	other := h.personal(1)

	for range 5 {
		got := h.batch(testAccount(t, h.pool))
		boxes := map[string]bool{}
		for _, name := range got {
			var on string
			if err := h.pool.QueryRow(context.Background(),
				`SELECT box FROM entry_relays WHERE name = $1`, name).Scan(&on); err != nil {
				t.Fatal(err)
			}
			if boxes[on] {
				t.Fatalf("batch %v holds two relays of %s (other machine %v)", got, on, other)
			}
			boxes[on] = true
		}
		if len(got) != 2 {
			t.Fatalf("batch %v, want two relays on two machines", got)
		}
	}
}

// A machine shared by SubscribersPerBox accounts takes nobody new.
func TestAFullMachineTakesNobodyNew(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	full := h.personal(1)[0]
	if _, err := h.pool.Exec(ctx, `
		WITH accounts AS (
			INSERT INTO hub_accounts (name)
			SELECT 'f' || substr(md5(random()::text), 1, 20) FROM generate_series(1, $2)
			RETURNING id)
		INSERT INTO entry_grants (account_id, relay_id, trigger)
		SELECT a.id, r.id, 'initial' FROM accounts a, entry_relays r WHERE r.name = $1`,
		full, SubscribersPerBox); err != nil {
		t.Fatal(err)
	}
	free := h.personal(2)
	for range 3 {
		got := h.batch(testAccount(t, h.pool))
		for _, name := range got {
			if name == full {
				t.Fatalf("a full machine took another account: %v", got)
			}
		}
		if len(got) != 2 {
			t.Fatalf("batch %v, want the two free relays %v", got, free)
		}
	}
}
