package presets

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func released(t *testing.T, s *Store, stage int) {
	t.Helper()
	ctx := context.Background()
	must(t, s.PutGroup(ctx, steam()))
	must(t, s.PutGroup(ctx, discord()))
	must(t, s.CreatePreset(ctx, "world", PresetDraft{Name: "мир", Default: true, Bypass: []string{"steam"}}))
	_, err := s.Release(ctx, "world", 100, "owner")
	must(t, err)
	must(t, s.UpdatePreset(ctx, "world", PresetDraft{Name: "мир", Default: true, Bypass: []string{"steam"}, Proxy: []string{"discord"}}))
	_, err = s.Release(ctx, "world", stage, "owner")
	must(t, err)
}

func get(t *testing.T, h http.Handler, path, etag string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if etag != "" {
		r.Header.Set("If-None-Match", etag)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// What the client's Refresh reads: a schema-1 document by bucket, 304 on
// the same ETag, 204 when nothing is released, 400 without a bucket.
func TestTheCatalogIsServedByBucketWithETags(t *testing.T) {
	s := liveStore(t)
	mux := http.NewServeMux()
	NewPublic(s, nil).Register(mux)

	if rec := get(t, mux, "/v1/presets/catalog?bucket=3", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("nothing released: %d", rec.Code)
	}
	released(t, s, 10)
	for _, bad := range []string{"", "?bucket=100", "?bucket=-1", "?bucket=x"} {
		if rec := get(t, mux, "/v1/presets/catalog"+bad, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d", bad, rec.Code)
		}
	}

	in := get(t, mux, "/v1/presets/catalog?bucket=9", "")
	out := get(t, mux, "/v1/presets/catalog?bucket=10", "")
	if in.Code != 200 || out.Code != 200 || in.Header().Get("ETag") == out.Header().Get("ETag") {
		t.Fatalf("codes %d %d, etags %q %q", in.Code, out.Code, in.Header().Get("ETag"), out.Header().Get("ETag"))
	}
	var doc struct {
		SchemaVersion int       `json:"schemaVersion"`
		Version       int64     `json:"version"`
		Services      []Service `json:"services"`
	}
	d := json.NewDecoder(in.Body)
	d.DisallowUnknownFields() // as the client parses it
	if err := d.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 1 || doc.Version < 3 || len(doc.Services) != 2 {
		t.Fatalf("bucket 9 got %+v", doc)
	}
	if err := ValidateCatalog(doc.Services); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, mux, "/v1/presets/catalog?bucket=9", in.Header().Get("ETag")); rec.Code != http.StatusNotModified {
		t.Fatalf("same etag: %d", rec.Code)
	}
}

// A preset claiming the request's country wins over the default.
func TestTheCatalogFollowsTheRequestsCountry(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	released(t, s, 100)
	must(t, s.CreatePreset(ctx, "ru", PresetDraft{Name: "россия", Countries: []string{"RU"}, Proxy: []string{"discord"}}))
	_, err := s.Release(ctx, "ru", 100, "owner")
	must(t, err)
	mux := http.NewServeMux()
	NewPublic(s, func(ip string) string {
		if ip == "192.0.2.1" {
			return "RU"
		}
		return "DE"
	}).Register(mux)

	r := httptest.NewRequest("GET", "/v1/presets/catalog?bucket=0", nil)
	r.RemoteAddr = "192.0.2.1:5000"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	if !strings.Contains(rec.Body.String(), `"discord"`) || strings.Contains(rec.Body.String(), `"steam"`) {
		t.Fatalf("RU got %s", rec.Body.String())
	}
}

// An open stream hears a change once the watcher reads the new epoch.
func TestAStreamHearsAChange(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	p := NewPublic(s, nil)
	mux := http.NewServeMux()
	p.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	v, err := s.Epoch(ctx)
	must(t, err)
	p.publish(v)

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, "GET", srv.URL+"/v1/presets/events", nil)
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	lines := bufio.NewScanner(resp.Body)
	lines.Scan() // ": hello"

	released(t, s, 50)
	v, err = s.Epoch(ctx)
	must(t, err)
	p.publish(v)
	for lines.Scan() {
		if strings.HasPrefix(lines.Text(), "data: ") {
			return
		}
	}
	t.Fatalf("no event: %v", lines.Err())
}

// D3 of the client's default-mode plan: a release carries the routing mode
// for a person who never picked one, rolled out by stage like its groups,
// and only a client that asks with defaultmode=1 gets the field; an older
// client parses the catalog strictly and would refuse it.
func TestTheDefaultModeRollsOutWithItsReleaseToClientsThatAsk(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	mux := http.NewServeMux()
	NewPublic(s, nil).Register(mux)
	must(t, s.PutGroup(ctx, steam()))
	must(t, s.PutGroup(ctx, discord()))
	must(t, s.CreatePreset(ctx, "ru", PresetDraft{Name: "россия", Default: true, Bypass: []string{"steam"}}))
	_, err := s.Release(ctx, "ru", 100, "owner")
	must(t, err)
	if err := s.UpdatePreset(ctx, "ru", PresetDraft{Name: "россия", Default: true, Proxy: []string{"discord"}, DefaultMode: "all"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("an unknown default mode was saved:", err)
	}
	must(t, s.UpdatePreset(ctx, "ru", PresetDraft{Name: "россия", Default: true, Bypass: []string{"steam"}, Proxy: []string{"discord"}, DefaultMode: "proxy"}))
	rel, err := s.Release(ctx, "ru", 0, "owner")
	must(t, err)
	if rel.DefaultMode != "proxy" {
		t.Fatal("the release did not freeze the mode:", rel.DefaultMode)
	}

	mode := func(path string) string {
		t.Helper()
		rec := get(t, mux, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
		var doc struct {
			SchemaVersion int       `json:"schemaVersion"`
			Version       int64     `json:"version"`
			DefaultMode   string    `json:"defaultMode"`
			Services      []Service `json:"services"`
		}
		must(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		if !strings.Contains(path, "defaultmode=1") {
			old := json.NewDecoder(strings.NewReader(rec.Body.String()))
			old.DisallowUnknownFields()
			var strict struct {
				SchemaVersion int       `json:"schemaVersion"`
				Version       int64     `json:"version"`
				Services      []Service `json:"services"`
			}
			if err := old.Decode(&strict); err != nil {
				t.Fatalf("%s: an older client would refuse it: %v", path, err)
			}
		}
		return doc.DefaultMode
	}
	if got := mode("/v1/presets/catalog?bucket=5&tester=1&defaultmode=1"); got != "proxy" {
		t.Fatalf("a tester that asks got mode %q", got)
	}
	if got := mode("/v1/presets/catalog?bucket=5&tester=1"); got != "" {
		t.Fatalf("a tester that did not ask got mode %q", got)
	}
	if got := mode("/v1/presets/catalog?bucket=5&defaultmode=1"); got != "" {
		t.Fatalf("stage 0 gave the mode to a device that is not a tester: %q", got)
	}
}
