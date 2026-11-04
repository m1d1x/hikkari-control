package stand

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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

type fakeSubs struct {
	url string
	err error
}

func (f fakeSubs) URLByName(_ context.Context, name string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.url + "#" + name, nil
}

func standOn(t *testing.T, pool *pgxpool.Pool, sub string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE stand_settings SET subscription = $1`, sub); err != nil {
		t.Fatal(err)
	}
}

func TestDBStandIsOffUntilASubscriptionIsNamed(t *testing.T) {
	pool := dbPool(t)
	s := NewScheduler(pool, fakeSlots{"ru-spb"}, fakeSubs{url: "https://sub/x"}, relayruns.NewStore(pool))
	if n, err := s.Tick(context.Background()); err != nil || n != 0 {
		t.Fatalf("queued %d err %v with the stand off", n, err)
	}
}

func TestDBStandQueuesOneRunPerPointAndKnobPerInterval(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, "stand-acc")
	s := NewScheduler(pool, fakeSlots{"lv-main", "ru-spb"}, fakeSubs{url: "https://sub/x"}, relayruns.NewStore(pool))
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
		task.BaselinePath != "/" || task.SubURL != "https://sub/x#stand-acc" {
		t.Fatalf("task %+v", task)
	}
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
	standOn(t, pool, "stand-acc")
	store := relayruns.NewStore(pool)
	s := NewScheduler(pool, fakeSlots{"ru-spb"}, fakeSubs{url: "https://sub/x"}, store)
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
		if r.Vantage != "ru-spb" || r.Source != relayruns.SourceStand || r.Core != res.Core {
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
	if err != nil || len(views) != 1 || views[0].Status != TaskDone || views[0].RowsAdded != 2 {
		t.Fatalf("views %+v err %v", views, err)
	}
}

func TestDBStandFailsARunWhoseSubscriptionCannotBeOpened(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, "gone")
	s := NewScheduler(pool, fakeSlots{"ru-spb"}, fakeSubs{err: errors.New("подписка не живая: expired")}, relayruns.NewStore(pool))
	if _, err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := s.Claim(ctx, "ru-spb")
	if err != nil || task != nil {
		t.Fatalf("handed out %+v err %v", task, err)
	}
	views, err := s.Recent(ctx, 10)
	if err != nil || len(views) != 1 || views[0].Status != TaskFailed || !strings.Contains(views[0].Error, "expired") {
		t.Fatalf("views %+v err %v", views, err)
	}
}

func TestDBStandFailedRunKeepsItsRows(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	standOn(t, pool, "stand-acc")
	s := NewScheduler(pool, fakeSlots{"ru-spb"}, fakeSubs{url: "https://sub/x"}, relayruns.NewStore(pool))
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
