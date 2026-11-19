package cascadewatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDBTheOpsEndpointGivesTheNewestRowsOfALayer: on 19.11 a day of the call
// layer came back cut at 17:35 the day before with no word of it, because the
// endpoint read the oldest 20000 rows of every layer and filtered the layer
// after. The reader asks about the end of the window: the rows are the newest
// of that layer, rows of other layers take no room, and a cut is said.
func TestDBTheOpsEndpointGivesTheNewestRowsOfALayer(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	now := time.Now().UTC().Truncate(time.Minute)
	w, _, _, _ := newTestWatch(t, pool, &fakeBoxes{}, &now)

	// More rows of other layers early in the window than the endpoint reads
	// at once, then an hour of calls at its end.
	var runs []Run
	early := now.Add(-23 * time.Hour)
	for i := 0; i < maxRows+100; i++ {
		runs = append(runs, Run{TS: early.Add(time.Duration(i) * time.Second), Entry: "ruin1", Exit: "unl1", Layer: LayerEntry, Vantage: "entry",
			Data: map[string]any{"j": i}})
	}
	for i := 0; i < 60; i++ {
		runs = append(runs, Run{TS: now.Add(-time.Duration(60-i) * time.Minute), Entry: "ruin1", Exit: "unl1", Layer: LayerCall, Vantage: "entry",
			Target: "copies1", OK: boolp(true), Data: map[string]any{"sent": 3000, "i": i}})
	}
	if err := w.store.Insert(ctx, runs...); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	RegisterOpsRoutes(mux, w, func(_ string, h http.HandlerFunc) http.HandlerFunc { return h })
	get := func(url string) map[string]json.RawMessage {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", url, rec.Code, rec.Body.String())
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	out := get("/api/v1/ops/trunk?entry=ruin1&last=24h&layer=call&limit=10")
	var rows []Run
	if err := json.Unmarshal(out["rows"], &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 10 {
		t.Fatalf("rows: %d, want 10", len(rows))
	}
	for k, r := range rows {
		if r.Layer != LayerCall {
			t.Fatalf("row %d of layer %q", k, r.Layer)
		}
		if got, want := int(r.Data["i"].(float64)), 50+k; got != want {
			t.Fatalf("row %d is minute %d, want the newest ten oldest first (%d)", k, got, want)
		}
	}
	var cut int
	if err := json.Unmarshal(out["truncated"], &cut); err != nil || cut != 50 {
		t.Fatalf("truncated: %s, want 50", out["truncated"])
	}

	var sw struct {
		Omitted int `json:"omitted"`
	}
	if err := json.Unmarshal(out["summary_window"], &sw); err != nil || sw.Omitted != 160 {
		t.Fatalf("summary_window: %s, want 160 rows left out of the summary", out["summary_window"])
	}

	out = get("/api/v1/ops/trunk?entry=ruin1&last=2h&layer=call&limit=100")
	if _, ok := out["truncated"]; ok {
		t.Fatalf("a whole layer says it was cut: %s", out["truncated"])
	}
	if _, ok := out["summary_window"]; ok {
		t.Fatalf("a window under the cap says the summary was cut: %s", out["summary_window"])
	}
}
