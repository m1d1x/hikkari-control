package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/m1d1x/hikkari-control/internal/capacity"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capacitySource struct {
	fail bool
	keys []string
}

func (s *capacitySource) Metrics(_ context.Context, keys []string) ([]capacity.Metric, error) {
	s.keys = keys
	if s.fail {
		return nil, errors.New("metrics unavailable")
	}
	return []capacity.Metric{{Key: keys[0], Mbps: 123, Points: 2, MeasuredAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix()}}, nil
}
func TestCapacitySummaryRidesSubscriptionAndCannotBreakIt(t *testing.T) {
	pool := livePool(t)
	signer := entry.NewSigner("entry-1", bytes.Repeat([]byte{9}, 32))
	batch := &fakeBatch{envelopes: [][]byte{signed(t, signer, realityRecord())}}
	service := New(pool, batch, []entry.Key{signer.Key()})
	source := &capacitySource{}
	service.Capacity = source
	token, err := service.Add(context.Background(), "capacity-"+uuid.NewString()[:8], unlimited(t, service))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sub/{token}", service.Handler())
	request := func() *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, httptest.NewRequest("GET", "/sub/"+token, nil))
		return out
	}
	out := request()
	if out.Code != 200 || out.Header().Get(capacity.Header) == "" || len(source.keys) != 1 {
		t.Fatalf("missing measurement summary: %d %v", out.Code, out.Header())
	}
	req := httptest.NewRequest("GET", "/sub/"+token, nil)
	req.Header.Set("Hikkari-Features", "relay-metrics-v2")
	sealed := httptest.NewRecorder()
	mux.ServeHTTP(sealed, req)
	if sealed.Code != 200 || sealed.Header().Get("Content-Type") != metricsContentType || sealed.Header().Get(capacity.Header) != "" {
		t.Fatal("unsealed negotiated response")
	}
	body, err := openSubscription(token, sealed.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Version int               `json:"version"`
		Links   []string          `json:"links"`
		Metrics []capacity.Metric `json:"metrics"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Version != 2 || len(envelope.Links) != 1 || len(envelope.Metrics) != 1 || !strings.Contains(envelope.Links[0], "vless://") {
		t.Fatalf("bad envelope: %s %v", body, err)
	}
	source.fail = true
	out = request()
	if out.Code != 200 || out.Body.Len() == 0 || out.Header().Get(capacity.Header) != "" {
		t.Fatal("measurement failure broke subscription")
	}
}
