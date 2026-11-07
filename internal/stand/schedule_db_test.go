package stand

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/capacity"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/relayruns"
)

// dbPool applies the real migrations to a scratch database.
func dbPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	adminURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if adminURL == "" {
		t.Skip("no test DB — set TEST_DATABASE_URL to run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Skipf("cannot reach test DB: %v", err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("hikkari_stand_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return pool
}

type fakeSlots []string

func (f fakeSlots) ActiveSlots(context.Context) ([]string, error) { return f, nil }

type fakeFleet []string

func (f fakeFleet) Links(context.Context) ([]string, error) { return f, nil }

const testAPI = "https://api.example"

func newTestScheduler(pool *pgxpool.Pool, slots fakeSlots, rows Rows) *Scheduler {
	return NewScheduler(pool, slots, fakeFleet{"vless://a@1.2.3.4:443#amsterdam-1-001", "vless://b@1.2.3.4:8443#amsterdam-1-002"}, rows, testAPI+"/")
}

// standOn switches the stand on or off with fp as its only knob: the tests
// count the runs of one knob, whatever knobs the migrations seed.
// TestDBStandQueuesARunForEveryEnabledKnob covers several.
func standOn(t *testing.T, pool *pgxpool.Pool, on bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE stand_settings SET enabled = $1`, on); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE stand_knobs SET enabled = (knob = 'fp')`); err != nil {
		t.Fatal(err)
	}
}

// linksTokenOf is the token of a claimed run's link.
func linksTokenOf(t *testing.T, task *Task) string {
	t.Helper()
	token, ok := strings.CutPrefix(task.SubURL, testAPI+LinksPath)
	if !ok || len(token) != 64 {
		t.Fatalf("task link %q is not the hub's links path with a token", task.SubURL)
	}
	return token
}

func TestDBStandIsOffUntilEnabled(t *testing.T) {
	pool := dbPool(t)
	s := newTestScheduler(pool, fakeSlots{"ru-spb"}, relayruns.NewStore(pool))
	if n, err := s.Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("queued %d err %v with the stand off", n, err)
	}
}

func TestDBStandQueuesARunForEveryEnabledKnob(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, true)
	if _, err := pool.Exec(ctx, `UPDATE stand_knobs SET enabled = true WHERE knob IN ('fp', 'mode')`); err != nil {
		t.Fatal(err)
	}
	s := newTestScheduler(pool, fakeSlots{"lv-main", "ru-spb"}, relayruns.NewStore(pool))
	if n, err := s.Tick(ctx); err != nil || n != 4 {
		t.Fatalf("queued %d err %v, want fp and mode at each of two points", n, err)
	}
	var vals []string
	if err := pool.QueryRow(ctx, `SELECT vals FROM stand_tasks WHERE knob = 'mode' AND slot = 'ru-spb'`).Scan(&vals); err != nil {
		t.Fatal(err)
	}
	if strings.Join(vals, ",") != "stream-up,packet-up" {
		t.Fatalf("mode values %v", vals)
	}
}

func TestDBStandQueuesOneRunPerPointAndKnobPerInterval(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, true)
	s := newTestScheduler(pool, fakeSlots{"lv-main", "ru-spb"}, relayruns.NewStore(pool))
	if n, err := s.Tick(ctx); err != nil || n != 2 {
		t.Fatalf("queued %d err %v, want one per point", n, err)
	}
	if n, err := s.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("queued %d again within the interval (err %v)", n, err)
	}
	task, err := s.Claim(ctx, "ru-spb")
	if err != nil || task == nil {
		t.Fatalf("claim %v %v", task, err)
	}
	if task.Knob != "fp" || strings.Join(task.Values, ",") != "chrome,chrome_155,firefox,safari" || task.Rounds != 3 ||
		task.BaselinePath != "/" {
		t.Fatalf("task %+v", task)
	}
	linksTokenOf(t, task)
	if again, err := s.Claim(ctx, "ru-spb"); err != nil || again != nil {
		t.Fatalf("a second claim got %+v %v", again, err)
	}
	// Past the interval, a point still busy with its run gets no second one.
	if _, err := pool.Exec(ctx, `UPDATE stand_tasks SET created_at = now() - interval '31 minutes'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("queued %d (err %v): want only lv-main, whose queued run expired", n, err)
	}
	var expired string
	if err := pool.QueryRow(ctx, `SELECT error FROM stand_tasks WHERE slot = 'lv-main' AND status = 'expired'`).Scan(&expired); err != nil {
		t.Fatalf("lv-main's unclaimed run not expired: %v", err)
	}
}

func TestDBStandFinishWritesRowsAsThePointsOwn(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, true)
	// The point's network is the operator's: the rows carry it.
	if _, err := pool.Exec(ctx, `INSERT INTO entry_vantages (slot, title, network) VALUES ('ru-spb', 'spb', 'AS41733')`); err != nil {
		t.Fatal(err)
	}
	store := relayruns.NewStore(pool)
	s := newTestScheduler(pool, fakeSlots{"ru-spb"}, store)
	if _, err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := s.Claim(ctx, "ru-spb")
	if err != nil || task == nil {
		t.Fatalf("claim %v %v", task, err)
	}
	ts := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	res := Result{Core: "sing-box-lx v1.14.3-lx.21+489ac0f3", Rows: []Row{
		{TS: ts, Vantage: "lv", Profile: "amsterdam-1-002", Knob: Baseline, Value: "no-protocol", Round: 1, Dl: 1000, DlEnd: "ok", Burst: 50, BurstTotal: 50},
		{TS: ts, Vantage: "lv", Profile: "amsterdam-1-002", Knob: "fp", Value: "chrome", Round: 1, Dl: Bytes, DlEnd: "ok", Burst: 31, BurstTotal: 50, BurstTimeout: 19, After: 3,
			Params: map[string]string{"fp": "chrome", "sni": "www.samsung.com"}},
	}}
	// Another point cannot finish this one's run.
	if _, err := s.Finish(ctx, "lv-main", task.ID, res); !errors.Is(err, ErrNoSuchTask) {
		t.Fatalf("finished by another point: %v", err)
	}
	bad := res
	bad.Rows = append([]Row{}, res.Rows...)
	bad.Rows[1].Knob = "sni"
	if _, err := s.Finish(ctx, "ru-spb", task.ID, bad); !errors.Is(err, ErrNotAResult) {
		t.Fatalf("a row of another knob taken: %v", err)
	}
	added, err := s.Finish(ctx, "ru-spb", task.ID, res)
	if err != nil || added != 2 {
		t.Fatalf("added %d err %v", added, err)
	}
	rows, err := store.Rows(ctx, relayruns.Query{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Minute)})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %+v err %v", rows, err)
	}
	for _, r := range rows {
		if r.Vantage != "ru-spb" || r.Source != relayruns.SourceStand || r.Core != res.Core || r.Network != "AS41733" {
			t.Fatalf("row %+v is not the point's own", r)
		}
	}
	if rows[1].AfterTotal != After || rows[1].BurstTimeout != 19 {
		t.Fatalf("row %+v", rows[1])
	}
	if _, err := s.Finish(ctx, "ru-spb", task.ID, res); !errors.Is(err, ErrNoSuchTask) {
		t.Fatalf("finished twice: %v", err)
	}
	views, err := s.Recent(ctx, 10)
	// The capacity run Tick queues beside the diagnostic one is another test's
	// subject: this one is about the run the point finished.
	var diag []TaskView
	for _, v := range views {
		if v.Knob != capacity.TaskKind {
			diag = append(diag, v)
		}
	}
	if err != nil || len(diag) != 1 || diag[0].Status != TaskDone || diag[0].RowsAdded != 2 {
		t.Fatalf("views %+v err %v", views, err)
	}
}

func TestDBStandFailsARunQueuedBeforeItWasSwitchedOff(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, true)
	s := newTestScheduler(pool, fakeSlots{"ru-spb"}, relayruns.NewStore(pool))
	if _, err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	standOn(t, pool, false)
	task, err := s.Claim(ctx, "ru-spb")
	if err != nil || task != nil {
		t.Fatalf("handed out %+v err %v", task, err)
	}
	views, err := s.Recent(ctx, 10)
	if err != nil || len(views) != 1 || views[0].Status != TaskFailed || !strings.Contains(views[0].Error, "выключен") {
		t.Fatalf("views %+v err %v", views, err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stand_tasks WHERE links_token_hash IS NOT NULL`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("a failed run kept its link (%d, %v)", left, err)
	}
}

// A claimed run's link serves the fleet's links to whoever holds it, and
// only while the run is claimed: a wrong token, another run's finished link
// or one claimed longer ago than claimTTL get nothing.
func TestDBStandLinksServeTheFleetOnlyWhileTheRunIsClaimed(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, true)
	s := newTestScheduler(pool, fakeSlots{"ru-spb", "lv-main"}, relayruns.NewStore(pool))
	if _, err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	spb, err := s.Claim(ctx, "ru-spb")
	if err != nil || spb == nil {
		t.Fatalf("claim %v %v", spb, err)
	}
	lv, err := s.Claim(ctx, "lv-main")
	if err != nil || lv == nil {
		t.Fatalf("claim %v %v", lv, err)
	}
	if linksTokenOf(t, spb) == linksTokenOf(t, lv) {
		t.Fatal("two runs share a link")
	}

	mux := http.NewServeMux()
	RegisterProberRoutes(mux, s, func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }
	}, func(*http.Request) string { return "" })
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	code, body := get(LinksPath + linksTokenOf(t, spb))
	raw, _ := base64.StdEncoding.DecodeString(body)
	if code != http.StatusOK || string(raw) != "vless://a@1.2.3.4:443#amsterdam-1-001\nvless://b@1.2.3.4:8443#amsterdam-1-002" {
		t.Fatalf("links %d %q", code, raw)
	}
	if code, _ := get(LinksPath + strings.Repeat("0", 64)); code != http.StatusNotFound {
		t.Fatalf("a made-up token got %d", code)
	}
	if code, _ := get(LinksPath + "short"); code != http.StatusNotFound {
		t.Fatalf("a malformed token got %d", code)
	}

	if _, err := s.Finish(ctx, "ru-spb", spb.ID, Result{Core: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FleetLinks(ctx, linksTokenOf(t, spb)); !errors.Is(err, ErrNoSuchLinks) {
		t.Fatalf("a finished run's link still served: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE stand_tasks SET claimed_at = now() - interval '76 minutes' WHERE id = $1`, lv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FleetLinks(ctx, linksTokenOf(t, lv)); !errors.Is(err, ErrNoSuchLinks) {
		t.Fatalf("a run claimed past claimTTL still served: %v", err)
	}
}

func TestDBStandFailedRunKeepsItsRows(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, true)
	s := newTestScheduler(pool, fakeSlots{"ru-spb"}, relayruns.NewStore(pool))
	if _, err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	task, _ := s.Claim(ctx, "ru-spb")
	ts := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	added, err := s.Finish(ctx, "ru-spb", task.ID, Result{Core: "c", Error: "profile hy2: the profile has no TLS",
		Rows: []Row{{TS: ts, Profile: "p", Knob: "fp", Value: "firefox", Round: 1, LoadError: true, BurstTotal: 50}}})
	if err != nil || added != 1 {
		t.Fatalf("added %d err %v", added, err)
	}
	views, _ := s.Recent(ctx, 1)
	if views[0].Status != TaskFailed || views[0].Error == "" {
		t.Fatalf("%+v", views[0])
	}
}
