package entry

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeMultiVerdicts implements both VerdictSource and VantageVerdictSource so
// the detector can be exercised against two observation points without ClickHouse.
type fakeMultiVerdicts struct {
	byVantage map[string]RelayVerdicts
}

func (f *fakeMultiVerdicts) LatestVerdictsByVantage(context.Context, time.Time, time.Time) (map[string]RelayVerdicts, error) {
	return f.byVantage, nil
}

// Machine addresses in entry_vantages are sealed under cryptobox just like
// entry_boxes: the table never holds a plaintext IP, and replacing the machine
// in an active slot must go through SaveVantage rather than CreateVantage.
func TestVantageSlotSealsHostAndLinksToFleetBox(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	slot := "ru-v-" + uuid.NewString()[:6]
	t.Cleanup(func() {
		_, _ = h.svc.pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot = $1`, slot)
	})

	plainIP := "198.51.100.42"
	created, err := h.svc.CreateVantage(ctx, VantageSpec{
		Slot:     slot,
		Title:    "Санкт-Петербург",
		Host:     plainIP,
		Provider: "selectel",
	}, "owner-test")
	if err != nil {
		t.Fatalf("create vantage: %v", err)
	}
	if created.Host != plainIP || created.Provider != "selectel" || created.Status != VantageActive {
		t.Fatalf("created vantage = %+v", created)
	}

	// Verify the raw column in Postgres does not contain the plaintext IP.
	var rawCT []byte
	if err := h.svc.pool.QueryRow(ctx, `SELECT host_ct FROM entry_vantages WHERE slot = $1`, slot).Scan(&rawCT); err != nil {
		t.Fatalf("read raw host_ct: %v", err)
	}
	if len(rawCT) == 0 || bytes.Contains(rawCT, []byte(plainIP)) {
		t.Fatalf("host_ct was stored empty or in plaintext: %x", rawCT)
	}

	// Creating the same active slot again via CreateVantage is refused:
	// switching the machine in an active slot must go through SaveVantage.
	if _, err := h.svc.CreateVantage(ctx, VantageSpec{
		Slot: slot, Host: "198.51.100.99", Provider: "aeza",
	}, "owner-test"); !errors.Is(err, ErrRejected) {
		t.Fatalf("duplicate CreateVantage err = %v, want ErrRejected", err)
	}

	// Switching the slot to a fleet box via SaveVantage inherits its sealed
	// address and provider while keeping the slot's title.
	boxName, boxIP := h.box()
	saved, err := h.svc.SaveVantage(ctx, VantageSpec{
		Slot: slot,
		Box:  boxName,
	}, "owner-test")
	if err != nil {
		t.Fatalf("save vantage with box: %v", err)
	}
	if saved.Box != boxName || saved.Host != boxIP || saved.Title != "Санкт-Петербург" {
		t.Fatalf("after switching to box: %+v (want box=%s host=%s)", saved, boxName, boxIP)
	}

	// Retiring the slot records retired_at and marks status = retired.
	retired, err := h.svc.RetireVantage(ctx, slot, "owner-test")
	if err != nil {
		t.Fatalf("retire vantage: %v", err)
	}
	if retired.Status != VantageRetired || retired.RetiredAt == nil {
		t.Fatalf("after retire: %+v", retired)
	}
}

// When require_two_vantages is enabled in entry_detection_policy, a ban seen
// from one observation point alone does not open an incident; once the second
// observation point confirms a blocking verdict, the detector opens it.
func TestDetectorRequiresTwoVantagesWhenPolicyTurnsItOn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	relay := h.relay(box, KindPersonal)
	h.admit(relay)
	// The box's spare port (ports.go), clean from both points throughout:
	// without it a box that reads healthy is short of a port and opens an
	// incident for that, which is not what this test is about.
	spare := h.relay(box, KindPersonal)
	h.admit(spare)

	v1 := "ru-msk-" + uuid.NewString()[:4]
	v2 := "ru-spb-" + uuid.NewString()[:4]
	t.Cleanup(func() {
		off := false
		_, _ = h.svc.SaveDetectionPolicy(context.Background(),
			DetectionPolicyPatch{RequireTwoVantages: &off}, "test-restore")
		_, _ = h.svc.pool.Exec(context.Background(),
			`DELETE FROM entry_vantages WHERE slot IN ($1, $2)`, v1, v2)
	})

	// Both points registered: a region nobody registered is not one, however
	// many rows it writes.
	for _, v := range []string{v1, v2} {
		if _, err := h.svc.CreateVantage(ctx, VantageSpec{Slot: v, Host: "198.51.100.8"}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.SetVantages(fakeVantages{regions: []string{v1, v2}})
	on := true
	if _, err := h.svc.SaveDetectionPolicy(ctx, DetectionPolicyPatch{RequireTwoVantages: &on}, "test"); err != nil {
		t.Fatalf("enable require_two_vantages: %v", err)
	}

	mv := &fakeMultiVerdicts{
		byVantage: map[string]RelayVerdicts{
			v1: {relay: "ip_banned", spare: "clean"},
			v2: {relay: "clean", spare: "clean"},
		},
	}
	h.svc.SetVerdicts(mv)

	// Only one vantage sees a ban while the second sees clean: no incident.
	opened, _, err := h.svc.Detect(ctx)
	if err != nil {
		t.Fatalf("detect with one vantage banning: %v", err)
	}
	if opened != 0 {
		t.Fatalf("one vantage alone opened %d incident(s) under require_two_vantages", opened)
	}

	// Second vantage confirms the block: incident opens.
	mv.byVantage[v2] = RelayVerdicts{relay: "ip_banned", spare: "clean"}
	opened, _, err = h.svc.Detect(ctx)
	if err != nil {
		t.Fatalf("detect with two vantages banning: %v", err)
	}
	if opened != 1 {
		t.Fatalf("two vantages confirmed ip_banned and opened = %d, want 1", opened)
	}
}

// One vantage's local network outage (`network_down`) must not overwrite
// another vantage's reading or turn a healthy box into CauseUnknown.
func TestDiagnoseAndMergeIgnoreNetworkDownOnOneVantageOrPort(t *testing.T) {
	policy := DetectionPolicy{IgnoreNetworkDown: true, RequireTwoVantages: false}

	merged := MergeVantageVerdicts(map[string]RelayVerdicts{
		"ru-msk": {"r1": "clean", "r2": "network_down"},
		"ru-spb": {"r1": "network_down", "r2": "network_down"},
	}, policy)
	if merged["r1"] != "clean" {
		t.Fatalf("r1 merged = %q, want clean (one vantage had network)", merged["r1"])
	}
	if merged["r2"] != "network_down" {
		t.Fatalf("r2 merged = %q, want network_down (both vantages blind)", merged["r2"])
	}

	d := DiagnoseBoxWithPolicy("b1", relaysOf("r1", "r2"), merged, policy)
	if d.Cause != CauseHealthy {
		t.Fatalf("cause = %q, want %q when one port is clean and the other only saw network_down",
			d.Cause, CauseHealthy)
	}
}

// Creating a slot without specifying a slot key assigns the next system slot
// letter ("a", "b", "c", ...), and deploying a box into a slot uploads
// nekko-entry-prober over SSH and starts nekko-entry-prober.service with the
// slot's own token — a new one, which voids the one the slot had.
func TestVantageAutoAssignsSystemSlotAndDeploysProberOverSSH(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()

	// Clean any pre-existing single-letter slots so we deterministically see a, b.
	_, _ = ph.svc.pool.Exec(ctx, `DELETE FROM entry_vantages WHERE slot IN ('a', 'b', 'c')`)
	t.Cleanup(func() {
		_, _ = ph.svc.pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot IN ('a', 'b', 'c')`)
	})

	// A machine with no relays — a vantage on one of the fleet is refused
	// (TestAFleetBoxWithRelaysCannotBeAVantage) — that the hub can reach.
	boxName, _ := ph.harness.box()
	ph.box.mu.Lock()
	ph.box.keyIn = true
	ph.box.mu.Unlock()

	v1, err := ph.svc.CreateVantage(ctx, VantageSpec{Title: "Москва", Box: boxName}, "owner-test")
	if err != nil {
		t.Fatalf("create first auto slot: %v", err)
	}
	if v1.Slot != "a" || v1.Token == "" {
		t.Fatalf("first auto slot = %q (token issued: %v), want \"a\" with a token", v1.Slot, v1.Token != "")
	}

	v2, err := ph.svc.CreateVantage(ctx, VantageSpec{Title: "Санкт-Петербург"}, "owner-test")
	if err != nil {
		t.Fatalf("create second auto slot: %v", err)
	}
	if v2.Slot != "b" {
		t.Fatalf("second auto slot = %q, want \"b\"", v2.Slot)
	}

	proberJobID, err := ph.prov.DeployProber(ctx, boxName, v1.Slot, "")
	if err != nil {
		t.Fatalf("DeployProber: %v", err)
	}
	j := ph.wait(proberJobID)
	if j.Status != "done" || j.Kind != JobProber {
		t.Fatalf("prober job = %+v, want status=done kind=prober", j)
	}
	// The slot of another box is not this deploy's to take.
	if _, err := ph.prov.DeployProber(ctx, boxName, v2.Slot, ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("deploy into a slot that holds another machine: %v, want ErrRejected", err)
	}

	// Verify that nekko-entry-prober was uploaded and started with a token
	// that speaks for slot "a" — and that the token create handed out is void.
	ph.box.mu.Lock()
	cmds := append([]string(nil), ph.box.cmds...)
	stdins := append([]string(nil), ph.box.stdins...)
	ph.box.mu.Unlock()
	var uploaded bool
	var planted string
	for _, c := range cmds {
		if strings.HasPrefix(c, "cat > /usr/local/bin/nekko-entry-prober.new") {
			uploaded = true
		}
	}
	for _, in := range stdins {
		for _, line := range strings.Split(in, "\n") {
			if tok, ok := strings.CutPrefix(line, "PROBE_INGEST_TOKEN="); ok {
				planted = tok
			}
		}
		if strings.Contains(in, "ENTRY_PROBE_SOURCE_REGION") {
			t.Fatalf("the env still names a region: %q", in)
		}
	}
	if !uploaded || planted == "" {
		t.Fatalf("prober SSH commands missing upload (%v) or token (%q); cmds = %v", uploaded, planted, cmds)
	}
	if slot, err := ph.svc.VantageByToken(ctx, planted); err != nil || slot != "a" {
		t.Fatalf("the planted token speaks for %q (%v), want a", slot, err)
	}
	if _, err := ph.svc.VantageByToken(ctx, v1.Token); !errors.Is(err, ErrNoSuchVantage) {
		t.Fatalf("the token create issued still works after the deploy: %v", err)
	}
}

// A slot's token: issued on create, kept by a save that leaves the machine
// where it was, replaced when the machine changes, gone when the slot
// retires. Only its hash is stored.
func TestAVantageTokenLivesAndDiesWithTheMachine(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	slot := "tok-" + uuid.NewString()[:6]
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot = $1`, slot)
	})
	v, err := h.svc.CreateVantage(ctx, VantageSpec{Slot: slot, Host: "198.51.100.9"}, "test")
	if err != nil || v.Token == "" {
		t.Fatalf("create: %+v %v", v, err)
	}
	first := v.Token
	if got, err := h.svc.VantageByToken(ctx, first); err != nil || got != slot {
		t.Fatalf("token speaks for %q (%v)", got, err)
	}
	var stored []byte
	if err := h.pool.QueryRow(ctx, `SELECT token_hash FROM entry_vantages WHERE slot = $1`, slot).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(first)) || len(stored) != 32 {
		t.Fatalf("stored %x, want a 32-byte hash", stored)
	}

	// A rename on the same machine keeps the prober talking.
	v, err = h.svc.SaveVantage(ctx, VantageSpec{Slot: slot, Title: "renamed", Host: "198.51.100.9"}, "test")
	if err != nil || v.Token != "" {
		t.Fatalf("same-machine save: token %q, %v", v.Token, err)
	}
	if got, _ := h.svc.VantageByToken(ctx, first); got != slot {
		t.Fatal("a rename cut the prober off")
	}

	// Another machine: a new token, and the old machine is mute.
	v, err = h.svc.SaveVantage(ctx, VantageSpec{Slot: slot, Host: "198.51.100.10"}, "test")
	if err != nil || v.Token == "" || v.Token == first {
		t.Fatalf("new-machine save: %v, token reissued %v", err, v.Token != "" && v.Token != first)
	}
	if _, err := h.svc.VantageByToken(ctx, first); !errors.Is(err, ErrNoSuchVantage) {
		t.Fatalf("the old machine still speaks: %v", err)
	}
	second := v.Token

	if _, err := h.svc.RetireVantage(ctx, slot, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.VantageByToken(ctx, second); !errors.Is(err, ErrNoSuchVantage) {
		t.Fatalf("a retired slot still speaks: %v", err)
	}
	if _, err := h.svc.IssueVantageToken(ctx, slot); !errors.Is(err, ErrNoSuchVantage) {
		t.Fatalf("a retired slot got a token: %v", err)
	}
}

// The other side of the fleet ban: a box inside an active slot gets no relays.
func TestAVantageBoxGetsNoRelays(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	slot := "vb-" + uuid.NewString()[:6]
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot = $1`, slot)
	})
	if _, err := h.svc.CreateVantage(ctx, VantageSpec{Slot: slot, Box: box}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.AddRelay(ctx, box, KindPersonal); !errors.Is(err, ErrRejected) {
		t.Fatalf("a relay on a vantage's box: %v, want ErrRejected", err)
	}
}
