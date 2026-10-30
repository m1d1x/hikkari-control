package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/auth"
)

// vantageMux mounts the pool's admin routes over ph's provisioner behind an
// owner gate that always lets the test's admin in, and a step-up that refuses
// until touched is set — the shape admin.RequireStepUp has in cmd/server.
func (ph *provHarness) vantageMux(touched *bool) func(method, path string, body any) (int, []byte) {
	ph.t.Helper()
	var adminID string
	if err := ph.pool.QueryRow(context.Background(), `
		INSERT INTO admins (login, display_name, password_hash, role)
		VALUES ($1, 'entry vantage test', 'x', 'owner') RETURNING id::text`,
		"vantage-"+uuid.NewString()[:8]).Scan(&adminID); err != nil {
		ph.t.Fatal(err)
	}
	gate := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(auth.WithAdmin(r.Context(), &auth.AdminInfo{ID: adminID})))
		}
	}
	stepUp := func(next http.HandlerFunc) http.HandlerFunc {
		return gate(func(w http.ResponseWriter, r *http.Request) {
			if !*touched {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "step_up_required"})
				return
			}
			next(w, r)
		})
	}
	mux := http.NewServeMux()
	RegisterAdminRoutes(mux, NewAdminHandlers(ph.svc, ph.prov, ph.pool), gate, stepUp)
	return func(method, path string, body any) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		return rec.Code, rec.Body.Bytes()
	}
}

// stdLog routes the standard logger (which applog duplicates every line to)
// into a buffer for the test.
type stdLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *stdLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *stdLog) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func captureStdLog(t *testing.T) *stdLog {
	t.Helper()
	s := &stdLog{}
	prev := log.Writer()
	log.SetOutput(s)
	t.Cleanup(func() { log.SetOutput(prev) })
	return s
}

func (ph *provHarness) auditOf(slot string) string {
	ph.t.Helper()
	rows, err := ph.pool.Query(context.Background(), `
		SELECT action || ' ' || payload::text FROM admin_audit_log
		WHERE target_type = 'entry_vantage' AND target_id = $1`, slot)
	if err != nil {
		ph.t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			ph.t.Fatal(err)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func dropSlot(t *testing.T, ph *provHarness, slot string) {
	t.Cleanup(func() {
		_, _ = ph.pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot = $1`, slot)
	})
}

// Audit 42 tail of К1/В1/В2: a slot's token is its voice in the detector and
// fetches the pool's dialling credentials, and opening a slot used to hand it
// out behind the owner gate alone. A stolen session without a passkey touch
// must not get one, nor cut a working prober off by reissuing.
func TestIssuingAVantageTokenNeedsAStepUp(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	touched := false
	do := ph.vantageMux(&touched)

	slot := "su-" + uuid.NewString()[:6]
	dropSlot(t, ph, slot)
	if code, body := do("POST", "/api/v1/admin/entry/vantages",
		map[string]string{"slot": slot, "host": "198.51.100.21"}); code != http.StatusForbidden {
		t.Fatalf("create without a touch: %d %s, want 403", code, body)
	}
	if _, err := ph.svc.VantageSlot(ctx, slot); !errors.Is(err, ErrNoSuchVantage) {
		t.Fatalf("create without a touch opened the slot: %v", err)
	}

	v, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: slot, Host: "198.51.100.21"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if code, body := do("POST", "/api/v1/admin/entry/vantages/"+slot+"/token", nil); code != http.StatusForbidden {
		t.Fatalf("reissue without a touch: %d %s, want 403", code, body)
	}
	if got, err := ph.svc.VantageByToken(ctx, v.Token); err != nil || got != slot {
		t.Fatalf("a refused reissue cut the prober off: %q %v", got, err)
	}

	// The touch is what opens them.
	touched = true
	code, body := do("POST", "/api/v1/admin/entry/vantages/"+slot+"/token", nil)
	if code != http.StatusOK {
		t.Fatalf("reissue with a touch: %d %s", code, body)
	}
	var a vantageAnswer
	if err := json.Unmarshal(body, &a); err != nil || a.Token == "" {
		t.Fatalf("reissue answer %s (%v), want a token", body, err)
	}
	if got, _ := ph.svc.VantageByToken(ctx, a.Token); got != slot {
		t.Fatalf("the reissued token speaks for %q, want %s", got, slot)
	}
	if _, err := ph.svc.VantageByToken(ctx, v.Token); !errors.Is(err, ErrNoSuchVantage) {
		t.Fatalf("the old token still works after a reissue: %v", err)
	}
}

// A standalone slot's token is in the one answer that issued it, with the env
// its prober reads, and nowhere else: not in the list, not in the logs, not in
// the audit journal (which records only the fact).
func TestAStandaloneVantageTokenIsShownOnce(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	applogs, stdlogs := captureLog(t), captureStdLog(t)
	touched := true
	do := ph.vantageMux(&touched)

	slot := "ext-" + uuid.NewString()[:6]
	dropSlot(t, ph, slot)
	code, body := do("POST", "/api/v1/admin/entry/vantages",
		map[string]string{"slot": slot, "title": "внешняя", "host": "198.51.100.22"})
	if code != http.StatusOK {
		t.Fatalf("create: %d %s", code, body)
	}
	var created vantageAnswer
	if err := json.Unmarshal(body, &created); err != nil || created.Token == "" {
		t.Fatalf("create answer %s (%v), want a token", body, err)
	}
	if !strings.Contains(created.Env, "PROBE_INGEST_TOKEN="+created.Token+"\n") ||
		!strings.Contains(created.Env, "PROBE_INGEST_URL=https://api.example.org/api/v1/internal/entry-probes\n") {
		t.Fatalf("env for the prober = %q", created.Env)
	}
	if got, _ := ph.svc.VantageByToken(ctx, created.Token); got != slot {
		t.Fatalf("the shown token speaks for %q, want %s", got, slot)
	}

	code, body = do("POST", "/api/v1/admin/entry/vantages/"+slot+"/token", nil)
	var reissued vantageAnswer
	if err := json.Unmarshal(body, &reissued); code != http.StatusOK || err != nil || reissued.Token == "" {
		t.Fatalf("reissue: %d %s", code, body)
	}

	code, list := do("GET", "/api/v1/admin/entry/vantages", nil)
	if code != http.StatusOK || !bytes.Contains(list, []byte(`"`+slot+`"`)) {
		t.Fatalf("list: %d %s", code, list)
	}
	for _, tok := range []string{created.Token, reissued.Token} {
		if bytes.Contains(list, []byte(tok)) {
			t.Errorf("GET /vantages carries a token")
		}
		if strings.Contains(applogs.text(), tok) || strings.Contains(stdlogs.text(), tok) {
			t.Errorf("the log carries a token")
		}
		if strings.Contains(ph.auditOf(slot), tok) {
			t.Errorf("the audit journal carries a token")
		}
	}
	if bytes.Contains(list, []byte(`"token"`)) {
		t.Errorf("GET /vantages has a token field: %s", list)
	}
	if j := ph.auditOf(slot); !strings.Contains(j, "entry.vantage.create") || !strings.Contains(j, "entry.vantage.token") {
		t.Errorf("the audit journal misses the issue: %s", j)
	}
}

// A slot on a box the hub reaches: the hub deploys the prober and plants the
// token, the owner never sees it — not on create, not on a reissue — and it
// is not in the job's log either.
func TestABoxVantageTokenGoesStraightToTheBox(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	applogs, stdlogs := captureLog(t), captureStdLog(t)
	touched := true
	do := ph.vantageMux(&touched)

	box, _ := ph.harness.box()
	ph.box.mu.Lock()
	ph.box.keyIn = true
	ph.box.mu.Unlock()
	slot := "box-" + uuid.NewString()[:6]
	dropSlot(t, ph, slot)

	planted := func() string {
		ph.box.mu.Lock()
		defer ph.box.mu.Unlock()
		tok := ""
		for _, in := range ph.box.stdins {
			for _, line := range strings.Split(in, "\n") {
				if v, ok := strings.CutPrefix(line, "PROBE_INGEST_TOKEN="); ok {
					tok = v
				}
			}
		}
		return tok
	}

	var tokens, jobs []string
	for _, call := range []struct {
		path string
		body any
	}{
		{"/api/v1/admin/entry/vantages", map[string]string{"slot": slot, "box": box}},
		{"/api/v1/admin/entry/vantages/" + slot + "/token", nil},
	} {
		code, body := do("POST", call.path, call.body)
		var a vantageAnswer
		if err := json.Unmarshal(body, &a); code != http.StatusOK || err != nil {
			t.Fatalf("POST %s: %d %s", call.path, code, body)
		}
		if a.Token != "" || bytes.Contains(body, []byte(`"token"`)) || a.Env != "" {
			t.Fatalf("%s answered a token for a box slot: %s", call.path, body)
		}
		if a.JobID == nil || a.DeployError != "" {
			t.Fatalf("%s did not deploy: %s", call.path, body)
		}
		if j := ph.wait(*a.JobID); j.Status != "done" || j.Kind != JobProber {
			t.Fatalf("prober job %+v\n%s", j, ph.logText(*a.JobID))
		}
		tok := planted()
		if got, _ := ph.svc.VantageByToken(ctx, tok); got != slot {
			t.Fatalf("the planted token speaks for %q, want %s", got, slot)
		}
		tokens = append(tokens, tok)
		jobs = append(jobs, ph.logText(*a.JobID))
	}
	if tokens[0] == tokens[1] {
		t.Fatal("a reissue planted the same token")
	}
	if _, err := ph.svc.VantageByToken(ctx, tokens[0]); !errors.Is(err, ErrNoSuchVantage) {
		t.Fatalf("the token before the reissue still works: %v", err)
	}

	_, list := do("GET", "/api/v1/admin/entry/vantages", nil)
	for _, tok := range tokens {
		if bytes.Contains(list, []byte(tok)) {
			t.Errorf("GET /vantages carries a planted token")
		}
		if strings.Contains(applogs.text(), tok) || strings.Contains(stdlogs.text(), tok) {
			t.Errorf("the log carries a planted token")
		}
		if strings.Contains(ph.auditOf(slot), tok) {
			t.Errorf("the audit journal carries a planted token")
		}
		for _, l := range jobs {
			if strings.Contains(l, tok) {
				t.Errorf("the job's log carries a planted token")
			}
		}
	}
}
