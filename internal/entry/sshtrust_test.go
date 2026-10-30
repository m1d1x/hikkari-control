package entry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/sshexec"
)

// logCapture is an applog sink that keeps every line, for asserting what the
// hub's log (ClickHouse, the console's live log) never carries.
type logCapture struct {
	mu   sync.Mutex
	recs []applog.Record
}

func (c *logCapture) Write(rs []applog.Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, rs...)
}

func (c *logCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, r := range c.recs {
		b.WriteString(r.Message + " " + r.Context + "\n")
	}
	return b.String()
}

// captureLog routes the package-level applog into a capture for the test.
func captureLog(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := applog.Default()
	applog.SetDefault(applog.New(c, "test", "", ""))
	t.Cleanup(func() { applog.SetDefault(prev) })
	return c
}

// provisioned is a box the operator's job took from password to the pool:
// host key on record, hub key in, one live relay.
func (ph *provHarness) provisioned() (box, addr string) {
	ph.t.Helper()
	ph.vantage("clean")
	box, id := ph.newBox(ph.box.password)
	if j := ph.wait(id); j.Status != "done" {
		ph.t.Fatalf("provision %+v\n%s", j, ph.logText(id))
	}
	acc, err := ph.svc.boxAccess(context.Background(), box)
	if err != nil {
		ph.t.Fatal(err)
	}
	if acc.hostKey == "" {
		ph.t.Fatal("the operator's provisioning recorded no host key")
	}
	return box, acc.address
}

func (ph *provHarness) hostKeyOf(box string) string {
	ph.t.Helper()
	acc, err := ph.svc.boxAccess(context.Background(), box)
	if err != nil {
		ph.t.Fatal(err)
	}
	return acc.hostKey
}

func (ph *provHarness) aliveRelays(box string) []PoolRelay {
	ph.t.Helper()
	all, err := ph.svc.List(context.Background())
	if err != nil {
		ph.t.Fatal(err)
	}
	var out []PoolRelay
	for _, r := range all {
		if r.Box == box {
			out = append(out, r)
		}
	}
	return out
}

// ── С6: trust on first use belongs to the operator's provisioning only ──

// After "forget the host key" the next thing to reach the box may be the
// grid, a rollout or a repair rather than the operator's own job. Whichever
// of them logged in first used to record whatever key answered on :22 — a
// man in the middle included — and the rollout then shipped him the relay
// identities and the pulse token (audit 42 С6). Only the provisioning job
// may take a key on first use; everything automatic refuses a box whose key
// is not on record.
func TestOnlyTheProvisioningJobTrustsAHostKeyOnFirstUse(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.provisioned()
	if err := ph.svc.ForgetHostKey(ctx, box); err != nil {
		t.Fatal(err)
	}
	// Whoever answers now presents another key: the machine was reinstalled,
	// or someone sits in the middle. Nothing automatic can tell which.
	ph.box.mu.Lock()
	ph.box.hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMitmHostKey"
	ph.box.mu.Unlock()

	// The grid.
	g := ph.prov.Inspect(ctx, box, ph.aliveRelays(box))
	if g.SSH == GridOK || len(g.Units) != 0 {
		t.Errorf("the grid logged into a box with no key on record: %+v", g)
	}
	if k := ph.hostKeyOf(box); k != "" {
		t.Fatalf("the grid recorded host key %q", k)
	}

	// A rollout's login, and a repair that redeploys the box.
	j := &jobRun{p: ph.prov, ctx: ctx, box: box}
	if r, err := j.loginByKey(); err == nil {
		r.Close()
		t.Errorf("a rollout logged into a box with no key on record")
	}
	if err := ph.prov.Reprovision(ctx, box); err == nil {
		t.Errorf("a repair redeployed a box with no key on record")
	}
	// The hub's own stand-up may meet a machine it rented and never saw, but
	// not one whose key the operator forgot.
	standUp := &jobRun{p: ph.prov, ctx: ctx, box: box, firstUse: trustIfNeverMet}
	if r, err := standUp.loginByKey(); !errors.Is(err, ErrHostKeyUnknown) {
		if r != nil {
			r.Close()
		}
		t.Errorf("the hub's stand-up on a forgotten key: %v, want ErrHostKeyUnknown", err)
	}
	if k := ph.hostKeyOf(box); k != "" {
		t.Fatalf("an automatic job recorded host key %q", k)
	}

	// The operator's own provisioning is the one place a key is taken.
	ph.vantage("clean")
	id, err := ph.prov.Provision(ctx, box, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if jb := ph.wait(id); jb.Status != "done" {
		t.Fatalf("the operator's re-provisioning: %+v\n%s", jb, ph.logText(id))
	}
	if k := ph.hostKeyOf(box); k != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMitmHostKey" {
		t.Fatalf("host key after the operator's job: %q", k)
	}
}

// A changed host key stays recognisable through every wrapper, and the grid
// reports it as a finding of its own rather than an ordinary refused login:
// "someone else answers on :22" and "sshd is down" are different news.
func TestAChangedHostKeyIsItsOwnSignal(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.provisioned()
	ph.box.mu.Lock()
	ph.box.hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherHostKey"
	ph.box.mu.Unlock()

	j := &jobRun{p: ph.prov, ctx: ctx, box: box}
	_, err := j.loginByKey()
	if !errors.Is(err, sshexec.ErrHostKeyChanged) {
		t.Fatalf("login error %v does not carry ErrHostKeyChanged", err)
	}
	err = ph.prov.Reprovision(ctx, box)
	if !errors.Is(err, sshexec.ErrHostKeyChanged) {
		t.Fatalf("job verdict %v does not carry ErrHostKeyChanged", err)
	}
	g := ph.prov.Inspect(ctx, box, ph.aliveRelays(box))
	if g.SSH != GridHostKey {
		t.Fatalf("grid ssh = %q, want %q", g.SSH, GridHostKey)
	}
	if ev := g.Evidence(); ev["ssh"] != GridHostKey {
		t.Fatalf("the incident's evidence does not carry it: %v", ev)
	}
}

// ── С3: the address stays sealed — not in the hub's log, not in a job's ──

// leakyRemote fails every command with an error that quotes the box's
// address, the way a remote tool's own message might.
type leakyRemote struct {
	Remote
	host string
}

func (r leakyRemote) Run(context.Context, string, io.Reader) (string, error) {
	return "connect to " + r.host + " port 443 failed", fmt.Errorf("exit status 1: connect to %s port 443 failed", r.host)
}

// The address is the secret (migration 039) and the job log promises no raw
// remote output (migration 043). An ssh failure used to put the address into
// applog — ClickHouse and the console's live log — and into entry_job_log in
// clear (audit 42 С3).
func TestTheAddressNeverReachesTheHubsLogOrAJobLog(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, addr := ph.provisioned()
	logs := captureLog(t)

	jobText := func(id uuid.UUID) string {
		jb, err := ph.svc.Job(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return jb.Error + "\n" + ph.logText(id)
	}

	// 1. The dial fails, and the dialer's error quotes the address.
	ph.prov.dial = func(_ context.Context, tg sshexec.Target, _ ...ssh.AuthMethod) (Remote, error) {
		return nil, fmt.Errorf("dial tcp %s: connect: connection refused", tg.Addr)
	}
	g := ph.prov.Inspect(ctx, box, ph.aliveRelays(box))
	if g.SSH != GridFail {
		t.Fatalf("grid ssh = %q, want fail", g.SSH)
	}
	id, err := ph.prov.Provision(ctx, box, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if jb := ph.wait(id); jb.Status != "failed" {
		t.Fatalf("job with a dead dial: %+v", jb)
	}
	if txt := jobText(id); strings.Contains(txt, addr) || !strings.Contains(txt, box) {
		t.Errorf("the job (failed dial) carries the address or misses the box:\n%s", txt)
	}

	// 2. The login works and a command fails, quoting the address.
	inner := ph.box.dial
	ph.prov.dial = func(c context.Context, tg sshexec.Target, auth ...ssh.AuthMethod) (Remote, error) {
		r, err := inner(c, tg, auth...)
		if err != nil {
			return nil, err
		}
		host, _, _ := net.SplitHostPort(tg.Addr)
		return leakyRemote{Remote: r, host: host}, nil
	}
	g = ph.prov.Inspect(ctx, box, ph.aliveRelays(box))
	id, err = ph.prov.Provision(ctx, box, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if jb := ph.wait(id); jb.Status != "failed" {
		t.Fatalf("job with a failing command: %+v", jb)
	}
	if txt := jobText(id); strings.Contains(txt, addr) {
		t.Errorf("the job (failed command) carries the address:\n%s", txt)
	}

	if txt := logs.text(); strings.Contains(txt, addr) {
		t.Errorf("the hub's log carries the address %s:\n%s", addr, txt)
	}
	if !strings.Contains(logs.text(), box) {
		t.Errorf("the hub's log does not name the box at all:\n%s", logs.text())
	}
}
