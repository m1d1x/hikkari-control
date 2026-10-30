package entry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/sshexec"
)

// fakeBox is a machine reached through a fake ssh: it accepts the provider's
// password until password login is switched off, the hub key once it is in
// authorized_keys, and records every command.
type fakeBox struct {
	mu          sync.Mutex
	password    string
	hostKey     string
	keyIn       bool
	passwordOff bool
	cmds        []string
	stdins      []string
	binary      []byte // what runs as nekko-edge
	staged      []byte // nekko-edge.new, waiting for the swap
	prev        []byte // nekko-edge.prev, kept by the swap for a rollback
	// conns answers the relay connection count, one value per ask; the last
	// one repeats.
	conns []int
}

func (b *fakeBox) dial(_ context.Context, t sshexec.Target, auth ...ssh.AuthMethod) (Remote, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t.HostKey != "" && t.HostKey != b.hostKey {
		return nil, sshexec.ErrHostKeyChanged
	}
	for _, a := range auth {
		switch reflect.TypeOf(a).String() {
		case "ssh.publicKeyCallback":
			if b.keyIn {
				return &fakeRemote{b: b}, nil
			}
		case "ssh.passwordCallback":
			if !b.passwordOff && b.password != "" {
				// The fake cannot read the callback's value; a wrong password is
				// modelled by an empty b.password.
				return &fakeRemote{b: b}, nil
			}
		}
	}
	return nil, sshexec.ErrAuth
}

type fakeRemote struct{ b *fakeBox }

func (r *fakeRemote) HostKey() string { return r.b.hostKey }
func (r *fakeRemote) Close() error    { return nil }
func (r *fakeRemote) Run(_ context.Context, cmd string, stdin io.Reader) (string, error) {
	var in []byte
	if stdin != nil {
		in, _ = io.ReadAll(stdin)
	}
	r.b.mu.Lock()
	defer r.b.mu.Unlock()
	r.b.cmds = append(r.b.cmds, cmd)
	r.b.stdins = append(r.b.stdins, string(in))
	switch {
	case strings.Contains(cmd, "authorized_keys"):
		r.b.keyIn = true
	case strings.Contains(string(in), "authorized_keys.nekko"):
		// The wipe: relay gone, and the hub key taken out last.
		r.b.binary, r.b.keyIn = nil, false
		return "1\n", nil
	case strings.Contains(string(in), "00-nekko-hardening.conf"):
		r.b.passwordOff = true
	case cmd == "uname -m":
		return "x86_64\n", nil
	case strings.HasPrefix(cmd, "cat > /usr/local/bin/nekko-edge.new"):
		r.b.staged = in
	case strings.HasPrefix(cmd, "test -f /usr/local/bin/nekko-edge.prev"):
		if r.b.prev == nil {
			return "", errors.New("exit 1")
		}
		r.b.staged = r.b.prev
	case strings.HasPrefix(cmd, "sha256sum /usr/local/bin/nekko-edge"):
		if r.b.binary == nil {
			return "", nil
		}
		sum := sha256.Sum256(r.b.binary)
		return hex.EncodeToString(sum[:]) + "\n", nil
	case strings.Contains(string(in), "state established"):
		n := 0
		if len(r.b.conns) > 0 {
			n = r.b.conns[0]
			if len(r.b.conns) > 1 {
				r.b.conns = r.b.conns[1:]
			}
		}
		return strconv.Itoa(n) + "\n", nil
	case strings.HasPrefix(cmd, "bash -s -- ") && strings.Contains(string(in), "nekko-edge@$name"):
		// The swap script: the staged build replaces the running one, which
		// is kept as .prev when it differs.
		if r.b.staged != nil {
			if r.b.binary != nil && !bytes.Equal(r.b.binary, r.b.staged) {
				r.b.prev = r.b.binary
			}
			r.b.binary, r.b.staged = r.b.staged, nil
		}
		f := strings.Fields(cmd)
		return "nekko-edge@" + f[3] + " active on :" + f[4] + "\n", nil
	case strings.HasPrefix(cmd, "bash -s -- ") && strings.Contains(string(in), "nekko-entry-prober.service"):
		f := strings.Fields(cmd)
		return "nekko-entry-prober active (slot " + f[3] + ")\n", nil
	}
	return "", nil
}

type provHarness struct {
	*harness
	box  *fakeBox
	prov *Provisioner
}

func newProvHarness(t *testing.T) *provHarness {
	h := newHarness(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "linux-amd64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "linux-amd64", "nekko-edge"), []byte("edge-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "linux-amd64", "nekko-entry-prober"), []byte("prober-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := &fakeBox{password: "provider-pw-123", hostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeHostKey"}
	p := NewProvisioner(h.svc, b.dial, dir, "api.example.org:443", nil)
	p.SetProber("https://api.example.org")
	p.probeTimeout, p.probePoll = 10*time.Second, 50*time.Millisecond
	return &provHarness{harness: h, box: b, prov: p}
}

// vantage answers every admission probe with verdict, once.
func (ph *provHarness) vantage(verdict string) {
	go func() {
		ctx := context.Background()
		for i := 0; i < 200; i++ {
			task, err := ph.svc.ClaimProbeTask(ctx, testVantage, TaskAdmission)
			if err == nil && task != nil {
				v := map[string]string{}
				for _, r := range task.Relays {
					broken := r.Address == "" || r.UUID == "" || r.PublicKey == "" || r.EchoPort == 0
					if r.Proto == ProtoHysteria2 {
						broken = r.Address == "" || r.Auth == "" || r.CertSHA256 == "" ||
							r.EchoPort == 0 || len(r.FinalMask) == 0 || r.UUID != ""
					}
					if broken {
						// A refusal: the verdict words are the prober's
						// (checkAnswer), and a relay handed out without its
						// credentials must not be admitted.
						v[r.Name] = VerdictFingerprint
						continue
					}
					v[r.Name] = verdict
				}
				_ = ph.svc.FinishProbeTask(ctx, task.ID, testVantage, v, nil)
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
}

func (ph *provHarness) wait(id uuid.UUID) Job {
	ph.t.Helper()
	for i := 0; i < 400; i++ {
		j, err := ph.svc.Job(context.Background(), id)
		if err != nil {
			ph.t.Fatal(err)
		}
		if j.Status != "running" {
			return j
		}
		time.Sleep(25 * time.Millisecond)
	}
	ph.t.Fatal("job still running")
	return Job{}
}

// boxName is a fresh box name: prefix and twelve decimal digits drawn from u.
// The digits used to be the decimal characters of u's hex, cut to twelve —
// and a UUID can carry fewer than twelve, which panicked the test about once
// in three hundred names.
func boxName(prefix string, u uuid.UUID) string {
	return fmt.Sprintf("%s%012d", prefix, binary.BigEndian.Uint64(u[:8])%1_000_000_000_000)
}

func (ph *provHarness) newBox(password string) (string, uuid.UUID) {
	ph.t.Helper()
	u := uuid.New()
	name := boxName("p", u)
	id, err := ph.prov.CreateAndProvision(context.Background(), NewBox{
		BoxSpec:  BoxSpec{Name: name, Address: "2001:db8::" + u.String()[:4], Provider: "vultr", Location: "amsterdam"},
		Password: password,
	}, "")
	if err != nil {
		ph.t.Fatalf("create and provision: %v", err)
	}
	ph.made = append(ph.made, name+"-1")
	return name, id
}

func (ph *provHarness) logText(id uuid.UUID) string {
	lines, err := ph.svc.JobLog(context.Background(), id, 0)
	if err != nil {
		ph.t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Line + "\n")
	}
	return b.String()
}

func TestANewBoxGoesFromPasswordToThePool(t *testing.T) {
	ph := newProvHarness(t)
	ph.prov.media = fixedMedia{{IP: "203.0.113.9", From: 40000, To: 40099}}
	ph.vantage("clean")
	box, id := ph.newBox(ph.box.password)
	j := ph.wait(id)
	log := ph.logText(id)
	if j.Status != "done" {
		t.Fatalf("job %+v\n%s", j, log)
	}
	relays, _ := ph.svc.List(context.Background())
	var status string
	for _, r := range relays {
		if r.Name == box+"-1" {
			status = r.Status
		}
	}
	if status != "alive" {
		t.Fatalf("%s-1 is %q after the job\n%s", box, status, log)
	}
	if !ph.box.keyIn || !ph.box.passwordOff {
		t.Fatalf("hub key in: %v, password off: %v", ph.box.keyIn, ph.box.passwordOff)
	}
	acc, _ := ph.svc.boxAccess(context.Background(), box)
	if acc.hostKey != ph.box.hostKey {
		t.Fatalf("host key on record %q", acc.hostKey)
	}

	// The relay runs the identity the hub holds; none of it — nor the
	// password — reaches the job log.
	id0, _ := ph.svc.RelayIdentity(context.Background(), box+"-1")
	var envSent bool
	for _, in := range ph.box.stdins {
		// The TCP rung of a call (voice plan Ф5): the stream relay forwards
		// datagrams to the shards' media and serves the probe's echo.
		if strings.Contains(in, "EDGE_PRIVATE_KEY="+id0.PrivateKey) && strings.Contains(in, "EDGE_UPSTREAMS=api.example.org:443") &&
			strings.Contains(in, "EDGE_MEDIA=203.0.113.9:40000-40099") &&
			strings.Contains(in, "EDGE_ECHO_PORT="+strconv.Itoa(id0.LocalPortBase+relayLoopbackBlock-1)) {
			envSent = true
		}
	}
	if !envSent {
		t.Fatal("the relay env never reached the box")
	}
	for _, secret := range []string{ph.box.password, id0.PrivateKey, id0.UUID, id0.ShortID} {
		if strings.Contains(log, secret) {
			t.Fatalf("the job log holds a secret %q:\n%s", secret, log)
		}
	}
	// Password login goes off only after the hub key proved it opens the box.
	keyAt, hardenAt := -1, -1
	for i, c := range ph.box.cmds {
		if keyAt < 0 && strings.Contains(c, "authorized_keys") {
			keyAt = i
		}
		if hardenAt < 0 && strings.Contains(ph.box.stdins[i], "00-nekko-hardening.conf") {
			hardenAt = i
		}
	}
	if keyAt < 0 || hardenAt < keyAt {
		t.Fatalf("key installed at %d, hardened at %d", keyAt, hardenAt)
	}

	// Again: the update path, with the key and without a password.
	ph.vantage("clean")
	id2, err := ph.prov.Provision(context.Background(), box, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if j := ph.wait(id2); j.Status != "done" {
		t.Fatalf("re-run %+v\n%s", j, ph.logText(id2))
	}
	if l := ph.logText(id2); !strings.Contains(l, "переподписан") || !strings.Contains(l, "уже на месте") {
		t.Fatalf("re-run log:\n%s", l)
	}
}

func TestADirtyBoxStaysOutOfThePool(t *testing.T) {
	ph := newProvHarness(t)
	ph.vantage("ip_banned")
	box, id := ph.newBox(ph.box.password)
	j := ph.wait(id)
	if j.Status != "failed" || j.Step != "probe" || !strings.Contains(j.Error, "ip_banned") {
		t.Fatalf("job %+v", j)
	}
	if err := ph.svc.Admit(context.Background(), box+"-1"); err != nil {
		t.Fatalf("the relay should still be pending: %v", err)
	}
}

func TestNoKeyAndNoPasswordStopsAtLogin(t *testing.T) {
	ph := newProvHarness(t)
	_, id := ph.newBox("")
	j := ph.wait(id)
	if j.Status != "failed" || j.Step != "login" || !strings.Contains(j.Error, "пароль не дан") {
		t.Fatalf("job %+v", j)
	}
}

func TestAChangedHostKeyStopsTheJob(t *testing.T) {
	ph := newProvHarness(t)
	ph.vantage("clean")
	box, id := ph.newBox(ph.box.password)
	if j := ph.wait(id); j.Status != "done" {
		t.Fatalf("first run %+v", j)
	}
	ph.box.hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIReinstalled"
	id2, err := ph.prov.Provision(context.Background(), box, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if j := ph.wait(id2); j.Status != "failed" || !strings.Contains(j.Error, "другой ключ хоста") {
		t.Fatalf("job %+v", j)
	}
	// Forgetting the key is the operator's way through.
	if err := ph.svc.ForgetHostKey(context.Background(), box); err != nil {
		t.Fatal(err)
	}
	ph.vantage("clean")
	id3, _ := ph.prov.Provision(context.Background(), box, "", "")
	if j := ph.wait(id3); j.Status != "done" {
		t.Fatalf("after forgetting %+v\n%s", j, ph.logText(id3))
	}
}

func TestOneJobPerBox(t *testing.T) {
	ph := newProvHarness(t)
	// No vantage: the job waits at the probe.
	box, id := ph.newBox(ph.box.password)
	for i := 0; i < 200; i++ {
		j, _ := ph.svc.Job(context.Background(), id)
		if j.Step == "probe" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := ph.prov.Provision(context.Background(), box, "", ""); !errors.Is(err, ErrBoxBusy) {
		t.Fatalf("second job on a busy box: %v, want ErrBoxBusy", err)
	}
	ph.vantage("clean")
	ph.wait(id)
}

type fixedMedia []MediaTarget

func (m fixedMedia) Media(context.Context, string) ([]MediaTarget, error) { return m, nil }

// A voice box (voice plan Ф3) goes through the same job as a stream box, with
// what differs taken from its protocol: UDP in the firewall and the listening
// check, the region's SFU media rendered into the relay's env, Hysteria
// credentials handed to the vantage, and a hysteria2 record for the region.
func TestAVoiceBoxGoesToThePoolInFrontOfItsRegion(t *testing.T) {
	ph := newProvHarness(t)
	ph.prov.media = fixedMedia{{IP: "203.0.113.9", From: 40000, To: 40099}}
	ph.vantage("clean")
	u := uuid.New()
	box := boxName("v", u)
	region := "t-" + u.String()[:8]
	id, err := ph.prov.CreateAndProvision(context.Background(), NewBox{
		BoxSpec: BoxSpec{Name: box, Address: "2001:db8::" + u.String()[:4], Provider: "vultr",
			Location: "amsterdam", Proto: ProtoHysteria2, Region: region},
		Password: ph.box.password,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ph.made = append(ph.made, box+"-1")
	j := ph.wait(id)
	log := ph.logText(id)
	if j.Status != "done" {
		t.Fatalf("job %+v\n%s", j, log)
	}

	var harden, swap, env string
	for i, c := range ph.box.cmds {
		in := ph.box.stdins[i]
		switch {
		case strings.Contains(in, "00-nekko-hardening.conf"):
			harden = c
		case strings.Contains(in, "nekko-edge@$name"):
			swap = c
		case strings.Contains(in, "EDGE_PROTO="):
			env = in
		}
	}
	if !strings.HasSuffix(harden, " 443/udp") {
		t.Fatalf("firewall opened %q, want 443/udp", harden)
	}
	if !strings.HasSuffix(swap, " 443 udp") {
		t.Fatalf("swap checks %q, want a udp listener", swap)
	}
	for _, want := range []string{"EDGE_PROTO=hysteria2", "EDGE_MEDIA=203.0.113.9:40000-40099", "EDGE_HY_FINALMASK="} {
		if !strings.Contains(env, want) {
			t.Fatalf("env lacks %s:\n%s", want, env)
		}
	}
	vid, _ := ph.svc.RelayIdentity(context.Background(), box+"-1")
	for _, secret := range []string{vid.Hysteria.Auth, vid.Hysteria.Masks[0].Password} {
		if strings.Contains(log, secret) {
			t.Fatalf("the job log holds a secret:\n%s", log)
		}
	}

	account := testAccount(t, ph.pool)
	envs, err := ph.svc.BatchFor(context.Background(), account, GrantMeta{})
	if err != nil {
		t.Fatal(err)
	}
	var served bool
	for _, e := range envs {
		r, err := Verify(e, ph.f.keys(t), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		served = served || (r.Name == box+"-1" && r.Proto == ProtoHysteria2)
	}
	if !served {
		t.Fatalf("the admitted voice relay is not in a fresh batch")
	}
}

// A region with no SFU announcing a media address has nowhere to forward
// to: the job stops before the relay is deployed, not after it is admitted.
func TestAVoiceBoxWithNoMediaIsNotDeployed(t *testing.T) {
	ph := newProvHarness(t)
	ph.prov.media = fixedMedia{}
	u := uuid.New()
	box := boxName("w", u)
	id, err := ph.prov.CreateAndProvision(context.Background(), NewBox{
		BoxSpec: BoxSpec{Name: box, Address: "2001:db8::" + u.String()[:4], Provider: "vultr",
			Location: "amsterdam", Proto: ProtoHysteria2, Region: "t-" + u.String()[:8]},
		Password: ph.box.password,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ph.made = append(ph.made, box+"-1")
	j := ph.wait(id)
	if j.Status == "done" || !strings.Contains(ph.logText(id), "некуда пересылать") {
		t.Fatalf("job %+v\n%s", j, ph.logText(id))
	}
	for _, in := range ph.box.stdins {
		if strings.Contains(in, "EDGE_PROTO=") {
			t.Fatal("a relay env reached the box")
		}
	}
}
