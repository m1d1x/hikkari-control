package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/capacity"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// measuredSource has numbers for every key it is asked about, and remembers
// the keys.
type measuredSource struct{ keys []string }

func (s *measuredSource) Metrics(_ context.Context, keys []string) ([]capacity.Metric, error) {
	s.keys = keys
	now := time.Now()
	out := make([]capacity.Metric, 0, len(keys))
	for _, key := range keys {
		out = append(out, capacity.Metric{Key: key, Mbps: 300, RTTMS: 120, Points: 2, MeasuredAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	}
	return out, nil
}

// A cascade profile (нидерланды-102) showed no ping and no speed: the capacity
// run dialled only the exit relay straight, and the profile's link holds the
// entry's address and port. The run now dials the cascade too, with the
// relay's own credential, and the subscriber's link (dup=1, its own key) is
// handed the numbers of that run under the key the client matches on.
func TestACascadeProfileIsMeasuredThroughTheEntryAndHandedItsNumbers(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	exit := hikkariRecord("unl1-2", "198.51.100.4")
	batch := &cascadeBatch{
		fakeBatch: fakeBatch{envelopes: [][]byte{
			signed(t, signer, exit),
			signed(t, signer, hikkariRecord("other-1", "198.51.100.9")),
		}},
		entryAddr: "192.0.2.10",
		relays:    map[string]bool{"unl1-2": true},
	}
	s := New(pool, batch, []entry.Key{signer.Key()})
	source := &measuredSource{}
	s.Capacity = source
	for box, location := range map[string]string{"ruin1": "Moscow, RU", "unl1": "Amsterdam, NL"} {
		if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto)
			VALUES ($1, '\x00', 'test', $2, 'hikkari') ON CONFLICT (name) DO UPDATE SET location = $2`, box, location); err != nil {
			t.Fatal(err)
		}
	}
	slot := "cap-cascade-" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `INSERT INTO entry_vantages(slot,in_russia) VALUES($1,true)`, slot); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM entry_vantages WHERE slot=$1`, slot)
	name := "t-" + uuid.NewString()[:8]
	token, err := s.Add(ctx, name, unlimited(t, s))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCascade(ctx, name, "unl1", "ruin1", 8443); err != nil {
		t.Fatal(err)
	}
	defer s.DropCascade(ctx, name)

	fleet := fakeFleet{records: batch.envelopes, nodes: map[string]entry.NodeName{"unl1-2": {Slug: "ams-node", Label: "амстердам-001"}}}
	probe, err := s.CapacityFleetLinks(ctx, fleet, slot)
	if err != nil {
		t.Fatal(err)
	}
	var through []string
	for _, link := range probe {
		if strings.Contains(link, "192.0.2.10:8443") {
			through = append(through, link)
		}
	}
	if len(through) != 1 || strings.Contains(through[0], "dup=") || !strings.Contains(through[0], "probe:") {
		t.Fatalf("capacity links %q, want the exit relay once through the entry, with its own credential and no dup", probe)
	}
	if len(probe) != 3 {
		t.Fatalf("capacity links %q, want both relays straight and the cascade", probe)
	}

	req := httptest.NewRequest(http.MethodGet, "/sub/"+token, nil)
	req.SetPathValue("token", token)
	req.Header.Set(featuresHeader, "hikkari, hikkari-dup, relay-metrics-v2")
	rec := httptest.NewRecorder()
	s.Handler()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body, err := openSubscription(token, rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Links   []string          `json:"links"`
		Metrics []capacity.Metric `json:"metrics"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Links) != 1 {
		t.Fatalf("envelope %s: %v", body, err)
	}
	served := envelope.Links[0]
	if !strings.Contains(served, "dup=1") {
		t.Fatalf("served %s, want the dup client's link", served)
	}
	if capacity.MeasuredKey(served) != capacity.Key(through[0]) {
		t.Fatal("the subscriber's cascade link is looked up under a key the run never measures")
	}
	if len(source.keys) != 1 || source.keys[0] != capacity.Key(through[0]) {
		t.Fatalf("looked up %q, want the measured key", source.keys)
	}
	if len(envelope.Metrics) != 1 || envelope.Metrics[0].Key != capacity.Key(served) || envelope.Metrics[0].RTTMS != 120 {
		t.Fatalf("metrics %+v, want the run's numbers under the client's own key", envelope.Metrics)
	}
}
