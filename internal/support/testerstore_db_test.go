package support

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
)

// dbTesterStore applies the real migrations to a scratch database.
func dbTesterStore(t *testing.T) (*DBTesterStore, *pgxpool.Pool) {
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
	name := fmt.Sprintf("hikkari_support_%d", time.Now().UnixNano())
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
	return &DBTesterStore{pool: pool}, pool
}

func TestDBAliasesAndCommands(t *testing.T) {
	s, pool := dbTesterStore(t)
	ctx := context.Background()
	send := func(req TesterChunkRequest) {
		t.Helper()
		if req.Platform == "" {
			req.Platform, req.Version = "macos", "1.0.24"
		}
		if err := s.AppendChunk(ctx, req); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// A heartbeat names the install, moves its signal and stores no chunk.
	send(TesterChunkRequest{InstallID: "t-aaa", Heartbeat: true})
	send(TesterChunkRequest{InstallID: "t-aaa", Heartbeat: true})
	send(TesterChunkRequest{InstallID: "t-bbb", Lines: []string{"x"}})
	installs, err := s.ListInstalls(ctx)
	if err != nil || len(installs) != 2 {
		t.Fatalf("list: %v %+v", err, installs)
	}
	byID := map[string]TesterInstall{}
	for _, in := range installs {
		byID[in.InstallID] = in
	}
	if a := byID["t-aaa"]; a.Alias != "tester-001" || a.Chunks != 0 || !a.Online || a.Version != "1.0.24" {
		t.Fatalf("heartbeat install: %+v", a)
	}
	if b := byID["t-bbb"]; b.Alias != "tester-002" || b.Chunks != 1 || b.Lines != 1 {
		t.Fatalf("chunk install: %+v", b)
	}

	// Every form of a reference.
	for _, ref := range []string{"tester-001", "tester-1", "001", "1", "t-aaa", "t-a"} {
		id, alias, err := s.ResolveInstall(ctx, ref)
		if err != nil || id != "t-aaa" || alias != "tester-001" {
			t.Fatalf("resolve %q: %q %q %v", ref, id, alias, err)
		}
	}
	if _, _, err := s.ResolveInstall(ctx, "tester-0"); !errors.Is(err, ErrAmbiguousInstall) {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	if _, _, err := s.ResolveInstall(ctx, "t-zzz"); !errors.Is(err, ErrAliasNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	// A name taken by hand is refused for another install, and the numbering
	// steps over a number an operator took.
	if _, err := s.RenameAlias(ctx, "t-bbb", "tester-001", ""); !errors.Is(err, ErrAliasTaken) {
		t.Fatalf("taken: %v", err)
	}
	if _, err := s.RenameAlias(ctx, "t-bbb", "tester-003", "note"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	send(TesterChunkRequest{InstallID: "t-ccc", Heartbeat: true})
	if _, alias, err := s.ResolveInstall(ctx, "t-ccc"); err != nil || alias != "tester-004" {
		t.Fatalf("number after a hand-taken one: %q %v", alias, err)
	}

	// A command: queued once, delivered once, done on the ack, only by its owner.
	c1, err := s.EnqueueCommand(ctx, "t-aaa", TesterCommandRunDoctor)
	if err != nil || c1.State != TesterCommandPending {
		t.Fatalf("enqueue: %+v %v", c1, err)
	}
	c2, err := s.EnqueueCommand(ctx, "t-aaa", TesterCommandRunDoctor)
	if err != nil || c2.Nonce != c1.Nonce {
		t.Fatalf("second ask must join the open run: %+v %v", c2, err)
	}
	took, err := s.TakeCommands(ctx, "t-aaa")
	if err != nil || len(took) != 1 || took[0].Nonce != c1.Nonce || took[0].Kind != TesterCommandRunDoctor {
		t.Fatalf("take: %+v %v", took, err)
	}
	if again, _ := s.TakeCommands(ctx, "t-aaa"); len(again) != 0 {
		t.Fatalf("delivered twice: %+v", again)
	}
	if err := s.AckCommands(ctx, "t-bbb", []string{c1.Nonce}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.ReadCommand(ctx, "t-aaa", c1.Nonce); c.State != TesterCommandDelivered {
		t.Fatalf("another install's ack must not count: %+v", c)
	}
	if err := s.AckCommands(ctx, "t-aaa", []string{c1.Nonce}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.ReadCommand(ctx, "t-aaa", c1.Nonce); c.State != TesterCommandDone {
		t.Fatalf("ack: %+v", c)
	}
	if _, err := s.ReadCommand(ctx, "t-bbb", c1.Nonce); !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("a command is read through its install: %v", err)
	}

	// A command nobody took in time stops being delivered and reads as expired.
	old, err := s.EnqueueCommand(ctx, "t-ccc", TesterCommandSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE support_tester_commands SET created_at = now() - interval '1 hour' WHERE nonce = $1`, old.Nonce); err != nil {
		t.Fatal(err)
	}
	if took, _ := s.TakeCommands(ctx, "t-ccc"); len(took) != 0 {
		t.Fatalf("expired command delivered: %+v", took)
	}
	if c, _ := s.ReadCommand(ctx, "t-ccc", old.Nonce); c.State != TesterCommandExpired {
		t.Fatalf("expired: %+v", c)
	}
}
