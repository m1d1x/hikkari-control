package entry

import (
	"context"
	"errors"
	"slices"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// Subscriptions raise a pool above what its traffic asks for and never lower
// it; a Hysteria2 lane counts one relay per subscription.
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
	f = FleetForecast{Target: 0, Keep: 0}
	_ = s.applySubscriberFloor(context.Background(), &f, ProtoHysteria2)
	if f.Target != 3 || f.Keep != 3 {
		t.Fatalf("400 subscriptions on a Hysteria2 lane: target %d keep %d, want 3", f.Target, f.Keep)
	}
	s.subscribers = liveCount(1)
	f = FleetForecast{}
	_ = s.applySubscriberFloor(context.Background(), &f, ProtoHysteria2)
	if f.Target != 1 {
		t.Fatalf("the first subscription left the Hysteria2 lane empty: %+v", f)
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

// A tester's account (all_relays) holds every alive personal relay of the
// fleet, a full machine included, and takes no room on a machine from others.
func TestATesterHoldsTheWholeFleet(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	fleet := h.personal(5)

	tester := testAccount(t, h.pool)
	if _, err := h.pool.Exec(ctx, `UPDATE hub_accounts SET all_relays = true WHERE id = $1`, tester); err != nil {
		t.Fatal(err)
	}
	got := h.batch(tester)
	if len(got) != len(fleet) {
		t.Fatalf("tester batch %v, want all of %v", got, fleet)
	}
	// a relay added later arrives on the next request
	more := h.personal(1)
	if got := h.batch(tester); len(got) != len(fleet)+len(more) {
		t.Fatalf("tester batch %v after a new relay, want %d", got, len(fleet)+len(more))
	}
	// an ordinary account is still just a batch
	if got := h.batch(testAccount(t, h.pool)); len(got) != 2 {
		t.Fatalf("ordinary batch %v, want two", got)
	}
}

// slugOf is the logical node a relay stands behind.
func (h *harness) slugOf(relay string) string {
	h.t.Helper()
	if err := h.svc.syncFleetNodes(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	var slug string
	if err := h.pool.QueryRow(context.Background(), `
		SELECT n.slug FROM entry_nodes n JOIN entry_relays r ON r.id = n.relay_id WHERE r.name = $1`, relay).Scan(&slug); err != nil {
		h.t.Fatalf("node of %s: %v", relay, err)
	}
	return slug
}

func (h *harness) setNodes(account uuid.UUID, slugs ...string) {
	h.t.Helper()
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if err := SetAccountNodes(ctx, tx, account, slugs); err != nil {
		h.t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		h.t.Fatal(err)
	}
}

// A fleet picked by hand: the account holds exactly the relays behind its
// chosen nodes, a full machine and two on one box included; the batch it held
// before goes with reason fleet the moment the fleet is set, and an empty
// fleet hands it back to the pool.
func TestAPickedFleetHoldsExactlyItsNodes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	fleet := h.personal(5)
	box, _ := h.box()
	twin1, twin2 := h.relay(box, KindPersonal), h.relay(box, KindPersonal)
	h.admit(twin1)
	h.admit(twin2)

	acc := testAccount(t, h.pool)
	before := h.batch(acc)
	if len(before) != 2 {
		t.Fatalf("batch %v, want two", before)
	}
	want := []string{fleet[0], twin1, twin2}
	slugs := make([]string, len(want))
	for i, r := range want {
		slugs[i] = h.slugOf(r)
	}
	h.setNodes(acc, slugs...)

	var live int
	if err := h.pool.QueryRow(ctx, `
		SELECT count(*) FROM entry_grants g JOIN entry_relays r ON r.id = g.relay_id
		WHERE g.account_id = $1 AND g.revoked_at IS NULL AND NOT (r.name = ANY($2))`, acc, want).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d grants off the fleet stayed live after it was set", live)
	}
	got := h.batch(acc)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("pinned batch %v, want %v", got, want)
	}
	var reasons []string
	rows, err := h.pool.Query(ctx, `SELECT DISTINCT revoke_reason FROM entry_grants WHERE account_id = $1 AND revoked_at IS NOT NULL`, acc)
	if err != nil {
		t.Fatal(err)
	}
	if reasons, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatal(err)
	}
	for _, r := range reasons {
		if r != "fleet" {
			t.Fatalf("an off-fleet grant went with reason %q, want fleet", r)
		}
	}

	// narrowed: the dropped node's relay goes, the rest stays
	h.setNodes(acc, h.slugOf(twin1))
	if got := h.batch(acc); !slices.Equal(got, []string{twin1}) {
		t.Fatalf("narrowed batch %v, want [%s]", got, twin1)
	}

	// back to the pool
	h.setNodes(acc)
	if got := h.batch(acc); len(got) < 1 || len(got) > 2 {
		t.Fatalf("batch %v after the fleet was cleared, want the pool's", got)
	}

	// an unknown node is refused, the fleet untouched
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := SetAccountNodes(ctx, tx, acc, []string{"no-such-node"}); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("unknown node: %v, want ErrUnknownNode", err)
	}
}

// A tester's account keeps grants on the whole fleet whatever was picked:
// its links are cut to the pick by the subscription, and the relays it does
// not show still know its key.
func TestATesterKeepsTheFleetUnderAPick(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	fleet := h.personal(3)
	tester := testAccount(t, h.pool)
	if _, err := h.pool.Exec(ctx, `UPDATE hub_accounts SET all_relays = true WHERE id = $1`, tester); err != nil {
		t.Fatal(err)
	}
	h.setNodes(tester, h.slugOf(fleet[0]))
	if got := h.batch(tester); len(got) < len(fleet) {
		t.Fatalf("tester batch %v under a pick, want all of %v", got, fleet)
	}
}
