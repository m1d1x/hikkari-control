package cascadewatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDBTheLoadEndpointRefusesThePointOnTheEntry: a synthetic users run from a
// point on the entry's own box would load the CPU it is there to hear, a point
// abroad is no user of the cascade, and a second run on a busy point is a 409.
func TestDBTheLoadEndpointRefusesThePointOnTheEntry(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	now := time.Now().UTC()
	w, queue, _, _ := newTestWatch(t, pool, &fakeBoxes{}, &now)
	if _, err := pool.Exec(ctx, `INSERT INTO entry_vantages (slot, title, in_russia) VALUES ('ru-onentry', 'on the entry', true)`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterOpsRoutes(mux, w, func(_ string, h http.HandlerFunc) http.HandlerFunc { return h })
	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ops/cascade-load", strings.NewReader(body)))
		return rec.Code, rec.Body.String()
	}
	const tail = `"entry":"ruin1","users":2,"minutes":10,"game_pps":200,"echo":"198.51.100.7:2053"}`

	if code, body := post(`{"slot":"ru-onentry",` + tail); code != http.StatusBadRequest || !strings.Contains(body, "самом входе") {
		t.Fatalf("point on the entry: %d %s", code, body)
	}
	if code, body := post(`{"slot":"lv-main",` + tail); code != http.StatusBadRequest {
		t.Fatalf("point abroad: %d %s", code, body)
	}
	if code, body := post(`{"slot":"ru-eurodir","entry":"ruin1","users":2,"minutes":10,"game_pps":200,"echo":"10.0.0.1:2053"}`); code != http.StatusBadRequest {
		t.Fatalf("private echo: %d %s", code, body)
	}
	if len(queue.loads) != 0 {
		t.Fatalf("refused runs were queued: %+v", queue.loads)
	}

	if code, body := post(`{"slot":"ru-eurodir",` + tail); code != http.StatusAccepted {
		t.Fatalf("good run: %d %s", code, body)
	}
	if len(queue.loads) != 1 || queue.loads[0].entry != "ruin1" || queue.loads[0].spec.Users != 2 || queue.loads[0].spec.GamePPS != 200 {
		t.Fatalf("queued: %+v", queue.loads)
	}

	queue.busy = true
	if code, body := post(`{"slot":"ru-eurodir",` + tail); code != http.StatusConflict {
		t.Fatalf("busy point: %d %s", code, body)
	}
}
