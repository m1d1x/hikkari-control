package entry_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/config"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/entrywire"
)

// TestLiveFleetTrial is opt-in and SPENDS MONEY. Run inside the main hub's
// container with its normal background runner disabled, a preserved Vultr
// account snapshot, and NEKKO_LIVE_ENTRY_TRIAL=1. It creates at most two extra Reality boxes (definite refusals create none),
// uses the real remote prober for admission, accelerates only the paid-hour
// clock for shrink, then deletes every test box. No domain is purchased.
func TestLiveFleetTrial(t *testing.T) {
	if os.Getenv("NEKKO_LIVE_ENTRY_TRIAL") != "1" {
		t.Skip("paid trial requires NEKKO_LIVE_ENTRY_TRIAL=1")
	}
	if os.Getenv("ENTRY_AUTOPILOT_ENABLED") != "0" {
		t.Fatal("disable the hub's competing background runner first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := cryptobox.Configure(os.Getenv); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	signer, err := entry.LoadSigner(os.Getenv, entry.PinnedKeys())
	if err != nil || signer == nil {
		t.Fatalf("signer unavailable: %v", err)
	}
	svc, err := entry.NewService(pool, signer, entry.DefaultBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	conn := &trialConnector{ProviderConnector: entrywire.Providers()}
	svc.SetProviderConnector(conn)
	if err := svc.LoadProviders(ctx); err != nil {
		t.Fatal(err)
	}
	if conn.host == nil {
		t.Fatal("no enabled hoster")
	}
	before, err := conn.host.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the provider inventory and every existing DB name before arming Create.
	boxes, err := svc.Boxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baselineNames := make(map[string]bool)
	baselineReality := 0
	for _, b := range boxes {
		baselineNames[b.Name] = true
		if b.RetiredAt == nil && b.Proto == entry.ProtoVlessRealityVision {
			baselineReality++
		}
	}
	conn.host.arm(before, baselineNames)
	store, err := analytics.NewStore(os.Getenv("CLICKHOUSE_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc.SetVantages(trialVantages{store})
	domain := config.Default()
	upstream, err := entry.Upstreams(domain.APIHost+":443", os.Getenv("S3_PUBLIC_ENDPOINT"), domain.UpdateURL())
	if err != nil {
		t.Fatal(err)
	}
	prov := entry.NewProvisioner(svc, entry.SSHDialer, entry.DefaultArtifactDir, upstream, nil)
	mech := entry.NewMechanic(svc, prov)
	var clockOffset atomic.Int64
	entry.TrialClockForTest(svc, func() time.Time { return time.Now().Add(time.Duration(clockOffset.Load())) })
	defer func() {
		entry.TrialStopForTest(svc)
		cleanup, done := context.WithTimeout(context.Background(), 2*time.Minute)
		defer done()
		clockOffset.Store(0)
		boxes, err := svc.Boxes(cleanup)
		if err != nil {
			t.Error(err)
			return
		}
		for _, b := range boxes {
			if !conn.host.ownsName(b.Name) {
				continue
			}
			if b.RetiredAt == nil {
				if _, _, err := svc.Retire(cleanup, b.Name, entry.DeathRotated); err != nil {
					t.Error(err)
					continue
				}
			}
			if err := svc.DeleteBoxInstance(cleanup, b.Name); err != nil {
				t.Error(err)
			}
		}
		// Reconcile only this run's reservations, including lost Create answers.
		for {
			if _, err := entry.TrialReleaseForTest(svc, cleanup, conn.host.names()); err != nil {
				t.Error(err)
				return
			}
			list, err := conn.host.Instances(cleanup)
			if err != nil {
				t.Error(err)
				return
			}
			if err := conn.host.baselinePresent(list); err != nil {
				t.Error(err)
				return
			}
			extra := 0
			for _, inst := range list {
				if conn.host.isBaseline(inst.ID) {
					continue
				}
				extra++
				if err := conn.host.Delete(cleanup, inst.ID); err != nil {
					t.Error(err)
				}
			}
			var pending int
			if err := pool.QueryRow(cleanup, `SELECT count(*) FROM entry_box_orders WHERE name = ANY($1::text[]) AND status IN ('reserved', 'created', 'undeleted')`, conn.host.names()).Scan(&pending); err != nil {
				t.Error(err)
				return
			}
			if extra == 0 && pending == 0 {
				t.Logf("CLEANUP VERIFIED: baseline %d intact, zero extra instances; Create calls %d, potentially created %d", len(before), conn.attempts.Load(), conn.creates.Load())
				return
			}
			select {
			case <-cleanup.Done():
				t.Errorf("cleanup left %d extra instances and %d unsettled reservations", extra, pending)
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()

	key := entry.FleetPool(entry.ProtoVlessRealityVision, "")
	entry.TrialPrimeSizingForTest(svc, key, baselineReality+2)
	forecasts, err := svc.FleetForecasts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range forecasts {
		if f.Pool == key && (f.Live != baselineReality || f.Need != 2) {
			t.Fatalf("trial must request exactly two machines: %+v", f)
		}
	}
	if _, _, err := svc.DetectFleet(ctx); err != nil {
		t.Fatal(err)
	}
	incidents, err := svc.Incidents(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var selected *entry.Incident
	for i := range incidents {
		inc := &incidents[i]
		if inc.Pool == key && inc.Cause == entry.CauseShortOfFleet && inc.Status == entry.IncidentOpen {
			selected = inc
			break
		}
	}
	if selected == nil {
		t.Fatal("synthetic load did not open a fleet shortage")
	}
	readyDeadline := time.Now().Add(90 * time.Second)
	for {
		ran, handed := entry.TrialConsiderForTest(mech, ctx, *selected)
		if ran {
			break
		}
		if handed || time.Now().After(readyDeadline) {
			t.Fatalf("automatic decision did not start: handed=%v", handed)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}

	for {
		inc, err := svc.Incident(ctx, selected.ID)
		if err != nil {
			t.Fatal(err)
		}
		if inc.Status != entry.IncidentActing {
			if inc.Status != entry.IncidentResolved {
				t.Fatalf("scale-up failed: status=%s outcome=%s", inc.Status, inc.Outcome)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	list, err := conn.host.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.host.baselinePresent(list); err != nil {
		t.Fatal(err)
	}
	if len(list) != len(before)+2 || conn.creates.Load() != 2 {
		t.Fatalf("expected two machines, got %d (%d potential creations)", len(list), conn.creates.Load())
	}
	t.Log("AUTO SCALE UP VERIFIED: two additional machines admitted; baseline present")
	entry.TrialPrimeHandbackForTest(svc, key, baselineReality)
	clockOffset.Store(int64(50 * time.Minute))
	if _, err := entry.TrialHandBackForTest(prov, ctx, conn.host.names()); err != nil {
		t.Fatal(err)
	}
	wantGone := 2
	if baselineReality == 0 {
		wantGone = 1
	}
	if gone, err := entry.TrialHandBackForTest(prov, ctx, conn.host.names()); err != nil || len(gone) != wantGone {
		t.Fatalf("surplus handback: %v, %v", gone, err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		list, err = conn.host.Instances(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.host.baselinePresent(list); err != nil {
			t.Fatal(err)
		}
		if len(list) == len(before)+2-wantGone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scale down: %d instances after confirmed delete", len(list))
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	t.Log("AUTO SCALE DOWN VERIFIED: only trial machines handed back; baseline present; billing-window clock accelerated")
}

type trialConnector struct {
	entry.ProviderConnector
	creates  atomic.Int32
	attempts atomic.Int32
	host     *trialHost
}

func (c *trialConnector) Hosting(kind string, creds map[string]string) (entry.BoxProvider, error) {
	bp, err := c.ProviderConnector.Hosting(kind, creds)
	if err != nil {
		return nil, err
	}
	h := &trialHost{BoxProvider: bp, creates: &c.creates, attempts: &c.attempts}
	c.host = h
	return h, nil
}

// A trial cannot buy a domain even if the saved registrar is enabled.
func (c *trialConnector) Registrar(string, map[string]string) (entry.DomainRegistrar, error) {
	return nil, entry.ErrNoRegistrar
}

type trialHost struct {
	entry.BoxProvider
	creates       *atomic.Int32
	attempts      *atomic.Int32
	mu            sync.Mutex
	armed         bool
	baselineIDs   map[string]bool
	baselineNames map[string]bool
	labels        map[string]string
	ownedIDs      map[string]bool
}

func (h *trialHost) arm(before []entry.BoxInstance, names map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.baselineIDs = make(map[string]bool)
	for _, inst := range before {
		h.baselineIDs[inst.ID] = true
		if strings.HasPrefix(inst.Label, "nekko-") {
			names[strings.TrimPrefix(inst.Label, "nekko-")] = true
		}
	}
	h.baselineNames = names
	h.labels = make(map[string]string)
	h.ownedIDs = make(map[string]bool)
	h.armed = true
}

func (h *trialHost) Create(ctx context.Context, o entry.BoxOrder) (entry.BoxInstance, error) {
	h.mu.Lock()
	if !h.armed || h.baselineNames[o.Hostname] || o.Hostname == "" || o.Label != "nekko-"+o.Hostname {
		h.mu.Unlock()
		return entry.BoxInstance{}, fmt.Errorf("%w: trial order outside owned namespace", entry.ErrProviderRefused)
	}
	n := h.creates.Add(1)
	if n > 2 {
		h.creates.Add(-1)
		h.mu.Unlock()
		return entry.BoxInstance{}, fmt.Errorf("%w: paid trial reached its two-machine limit", entry.ErrProviderRefused)
	}
	h.labels[o.Label] = o.Hostname
	h.mu.Unlock()
	h.attempts.Add(1)
	inst, err := h.BoxProvider.Create(ctx, o)
	h.mu.Lock()
	defer h.mu.Unlock()
	if inst.ID != "" {
		if h.baselineIDs[inst.ID] {
			return entry.BoxInstance{}, fmt.Errorf("provider returned a baseline ID")
		}
		h.ownedIDs[inst.ID] = true
	}
	if errors.Is(err, entry.ErrProviderRefused) && inst.ID == "" {
		// Only a definite refusal releases a slot. Unknown answers keep it reserved.
		h.creates.Add(-1)
	}
	return inst, err
}

func (h *trialHost) Instances(ctx context.Context) ([]entry.BoxInstance, error) {
	list, err := h.BoxProvider.Instances(ctx)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, inst := range list {
		if _, ok := h.labels[inst.Label]; ok && !h.baselineIDs[inst.ID] {
			h.ownedIDs[inst.ID] = true
		}
	}
	return list, nil
}

func (h *trialHost) Delete(ctx context.Context, id string) error {
	h.mu.Lock()
	allowed := h.armed && !h.baselineIDs[id] && h.ownedIDs[id]
	h.mu.Unlock()
	if !allowed {
		return fmt.Errorf("trial refuses deletion of unowned instance %s", id)
	}
	return h.BoxProvider.Delete(ctx, id)
}

func (h *trialHost) isBaseline(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.baselineIDs[id]
}

func (h *trialHost) ownsName(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return name != "" && !h.baselineNames[name] && h.labels["nekko-"+name] == name
}

func (h *trialHost) names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.labels))
	for _, name := range h.labels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *trialHost) baselinePresent(list []entry.BoxInstance) error {
	found := make(map[string]bool)
	for _, inst := range list {
		found[inst.ID] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range h.baselineIDs {
		if !found[id] {
			return fmt.Errorf("baseline instance %s disappeared", id)
		}
	}
	return nil
}

type trialVantages struct{ store *analytics.Store }

func (v trialVantages) Vantages(ctx context.Context, since, until time.Time) ([]string, error) {
	rows, err := v.store.Query(ctx, `SELECT DISTINCT source_region FROM entry_probes WHERE ts >= ? AND ts <= ? AND source_region != ''`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slot string
		if err := rows.Scan(&slot); err != nil {
			return nil, err
		}
		out = append(out, slot)
	}
	return out, rows.Err()
}
