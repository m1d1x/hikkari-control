package entry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/m1d1x/hikkari-control/internal/sshexec"
	"golang.org/x/crypto/ssh"
)

// The existing SSH harness now models the prober's upload, atomic swap,
// restart and authenticated polls too. Each connection keeps only staged env;
// the machine bytes live in the harness fakeBox and are guarded by its mutex.
type fakeProberRemote struct {
	Remote
	s           *Service
	slot, token string
	staged      []byte
	cancel      context.CancelFunc
}

func newFakeProberRemote(r Remote, s *Service) Remote { return &fakeProberRemote{Remote: r, s: s} }
func (r *fakeProberRemote) Run(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	var in []byte
	if stdin != nil {
		in, _ = io.ReadAll(stdin)
	}
	out, err := r.Remote.Run(ctx, cmd, bytes.NewReader(in))
	if err != nil {
		return out, err
	}
	switch {
	case strings.HasPrefix(cmd, "cat > /usr/local/bin/nekko-entry-prober.new"):
		r.staged = append([]byte(nil), in...)
	case strings.Contains(cmd, "if [ -x /usr/local/bin/xray ]"):
		return "/usr/local/bin/nekko-edge", nil
	case strings.Contains(cmd, "prober.env.new") && strings.Contains(cmd, "cat >"):
		for _, line := range strings.Split(string(in), "\n") {
			if token, ok := strings.CutPrefix(line, "PROBE_INGEST_TOKEN="); ok {
				r.token = token
			}
		}
		r.slot, _ = r.s.VantageByToken(ctx, r.token)
	case string(in) == proberSwapScript:
		sha := sha256.Sum256(r.staged)
		digest := hex.EncodeToString(sha[:])
		pollCtx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel
		go func() {
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-pollCtx.Done():
					return
				case <-tick.C:
					// Exercise token lookup: a process with a revoked token cannot ack.
					slot, e := r.s.VantageByToken(pollCtx, r.token)
					if e == nil && slot == r.slot {
						_, _ = r.s.pool.Exec(pollCtx, `UPDATE entry_vantages SET prober_sha=$2,prober_poll_at=now(),last_admission_poll_at=now() WHERE slot=$1`, slot, digest)
					}
				}
			}
		}()
	}
	return out, nil
}
func (r *fakeProberRemote) Close() error {
	if r.cancel != nil {
		r.cancel()
	}
	return r.Remote.Close()
}

func TestAdoptVantagePasswordThenKeyIdempotent(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	slot := "adopt-" + uuid.NewString()[:8]
	v, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: slot, Title: "adopt", Host: "192.0.2.153"}, "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := ph.prov.AdoptVantage(ctx, slot, "192.0.2.153", "root", "provider-pw-123")
	if err != nil {
		t.Fatal(err)
	}
	if j := ph.wait(id); j.Status != "done" {
		t.Fatalf("%+v\n%s", j, ph.logText(id))
	}
	after, err := ph.svc.VantageSlot(ctx, slot)
	if err != nil {
		t.Fatal(err)
	}
	if after.Box == "" {
		t.Fatal("slot has no box")
	}
	relays, _ := ph.svc.List(ctx)
	for _, r := range relays {
		if r.Box == after.Box {
			t.Fatal("adopt created a relay")
		}
	}
	ph.box.mu.Lock()
	key, passwordOff := ph.box.keyIn, ph.box.passwordOff
	ph.box.mu.Unlock()
	if !key || !passwordOff {
		t.Fatal("hub key/password hardening missing")
	}
	if strings.Contains(ph.logText(id), "provider-pw-123") || strings.Contains(ph.logText(id), v.Token) {
		t.Fatal("secret logged")
	}
	id, err = ph.prov.AdoptVantage(ctx, slot, "192.0.2.153", "root", "")
	if err != nil {
		t.Fatal(err)
	}
	if j := ph.wait(id); j.Status != "done" {
		t.Fatalf("repeat: %+v", j)
	}
	again, _ := ph.svc.VantageSlot(ctx, slot)
	if again.Box != after.Box {
		t.Fatal("repeat made another box")
	}
}

func TestAdoptRefusesRelayAndReportsLoginStep(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	if _, err := ph.svc.AddRelay(ctx, box, KindPersonal); err != nil {
		t.Fatal(err)
	}
	acc, _ := ph.svc.boxAccess(ctx, box)
	slot := "adopt-" + uuid.NewString()[:8]
	_, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: slot, Host: acc.address}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ph.prov.AdoptVantage(ctx, slot, acc.address, "root", "pw"); !errors.Is(err, ErrRejected) {
		t.Fatalf("relay adoption: %v", err)
	}
	slot = "adopt-" + uuid.NewString()[:8]
	v, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: slot, Host: "192.0.2.154"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ph.prov.AdoptVantage(ctx, slot, "192.0.2.154", "root", ""); err == nil || !strings.Contains(err.Error(), "adopt login") {
		t.Fatalf("no password: %v", err)
	}
	if got, err := ph.svc.VantageByToken(ctx, v.Token); err != nil || got != slot {
		t.Fatal("failed adoption revoked old token")
	}
}

// Refusing the swap must restore the old bearer and report the failure;
// the error path is also used by the no-poll timeout below.
type failingProberRemote struct {
	Remote
	fail bool
}

func (r failingProberRemote) Run(ctx context.Context, cmd string, in io.Reader) (string, error) {
	var data []byte
	if in != nil {
		data, _ = io.ReadAll(in)
	}
	if r.fail && string(data) == proberSwapScript {
		return "", errors.New("simulated swap failure")
	}
	return r.Remote.Run(ctx, cmd, bytes.NewReader(data))
}
func TestProberDeployFailureRestoresTokenAndIsVisible(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	ph.box.keyIn = true
	v, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: "fail-" + uuid.NewString()[:8], Box: box}, "")
	if err != nil {
		t.Fatal(err)
	}
	dial := ph.prov.dial
	ph.prov.dial = func(ctx context.Context, target sshexec.Target, a ...ssh.AuthMethod) (Remote, error) {
		r, e := dial(ctx, target, a...)
		return failingProberRemote{r, true}, e
	}
	id, err := ph.prov.DeployProber(ctx, box, v.Slot, "")
	if err != nil {
		t.Fatal(err)
	}
	if j := ph.wait(id); j.Status != "failed" {
		t.Fatalf("%+v", j)
	}
	if got, e := ph.svc.VantageByToken(ctx, v.Token); e != nil || got != v.Slot {
		t.Fatalf("old token not restored: %q %v", got, e)
	}
	ph.prov.proberUpdateError(ctx, v.Slot, "", errors.New("outer rollout error"))
	var failedSHA string
	if err := ph.svc.pool.QueryRow(ctx, `SELECT prober_failed_sha FROM entry_vantages WHERE slot=$1`, v.Slot).Scan(&failedSHA); err != nil || failedSHA != ph.prov.proberBuilds["amd64"].SHA {
		t.Fatalf("failed build forgotten: %q %v", failedSHA, err)
	}
	status, e := ph.svc.readProberStatus(ctx, v)
	if e != nil || !strings.Contains(status.ProberUpdateError, "outer rollout error") {
		t.Fatalf("status: %+v %v", status, e)
	}
	ph.box.mu.Lock()
	defer ph.box.mu.Unlock()
	found := false
	for _, in := range ph.box.stdins {
		found = found || in == proberRollbackScript
	}
	if !found {
		t.Fatal("rollback script not run")
	}
}

func TestProberUpdateAPIRequiresSlotTokenAndPinsBuild(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	v, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: "update-" + uuid.NewString()[:8]}, "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	RegisterProberUpdates(mux, ph.svc, ph.prov)
	sha := ph.prov.proberBuilds["amd64"].SHA
	for _, tt := range []struct {
		path, token string
		code        int
	}{
		{"/update?arch=amd64&sha=" + sha, "", 401},
		{"/update?arch=amd64&sha=" + sha, v.Token, 200},
		{"/binary?arch=amd64&sha=" + sha, v.Token, 200},
		{"/binary?arch=../../etc&sha=" + sha, v.Token, 404},
		{"/binary?arch=amd64&sha=" + strings.Repeat("a", 64), v.Token, 404},
	} {
		req := httptest.NewRequest("GET", "/api/v1/internal/entry-probes"+tt.path, nil)
		req.Header.Set("Authorization", "Bearer "+tt.token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != tt.code {
			t.Fatalf("%s: %d %s", tt.path, w.Code, w.Body.String())
		}
	}
}

func TestProberDeployMissingPollRollsBack(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	ph.box.keyIn = true
	v, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: "silent-" + uuid.NewString()[:8], Box: box}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Raw Remote never reports a poll, despite systemd claiming an active unit.
	ph.prov.dial = ph.box.dial
	ph.prov.probeTimeout = 100 * time.Millisecond
	ph.prov.probePoll = 10 * time.Millisecond
	id, err := ph.prov.DeployProber(ctx, box, v.Slot, "")
	if err != nil {
		t.Fatal(err)
	}
	j := ph.wait(id)
	if j.Status != "failed" || !strings.Contains(j.Error, "poll confirmation") {
		t.Fatalf("%+v", j)
	}
	if _, err = ph.svc.VantageByToken(ctx, v.Token); err != nil {
		t.Fatal("silent deploy revoked original token")
	}
	ph.box.mu.Lock()
	defer ph.box.mu.Unlock()
	found := false
	for _, in := range ph.box.stdins {
		found = found || in == proberRollbackScript
	}
	if !found {
		t.Fatal("silent deploy did not roll back")
	}
}

func TestProberWaitRejectsWrongBuildAndStaleAdmission(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	v, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: "wrong-" + uuid.NewString()[:8]}, "")
	if err != nil {
		t.Fatal(err)
	}
	ph.prov.probeTimeout = 40 * time.Millisecond
	ph.prov.probePoll = 10 * time.Millisecond
	build := ph.prov.proberBuilds["amd64"]
	for _, sha := range []string{strings.Repeat("a", 64), build.SHA} {
		if _, err = ph.svc.pool.Exec(ctx, `UPDATE entry_vantages SET prober_sha=$2,prober_poll_at=now(),last_admission_poll_at=now()-interval '1 hour' WHERE slot=$1`, v.Slot, sha); err != nil {
			t.Fatal(err)
		}
		j := &jobRun{p: ph.prov, ctx: ctx}
		if err = j.waitProber(v.Slot, build.SHA, time.Now().Add(-time.Second)); err == nil {
			t.Fatal("wrong/stale prober confirmed")
		}
	}
}

type inspectProberRemote struct {
	Remote
	run func(string, []byte) (string, bool)
}

func (r inspectProberRemote) Run(ctx context.Context, cmd string, in io.Reader) (string, error) {
	var data []byte
	if in != nil {
		data, _ = io.ReadAll(in)
	}
	if out, handled := r.run(cmd, data); handled {
		return out, nil
	}
	return r.Remote.Run(ctx, cmd, bytes.NewReader(data))
}
func TestProberRolloutSerialAndMissingKeyVisible(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	ph.box.keyIn = true
	if _, err := ph.svc.pool.Exec(ctx, `UPDATE entry_vantages SET status='retired',retired_at=now(),token_hash=NULL`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		box, _ := ph.harness.box()
		if err := ph.svc.setHostKey(ctx, box, ph.box.hostKey); err != nil {
			t.Fatal(err)
		}
		if _, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: "roll-" + uuid.NewString()[:8], Box: box}, ""); err != nil {
			t.Fatal(err)
		}
	}
	box, _ := ph.harness.box()
	missing, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: "missing-" + uuid.NewString()[:8], Box: box}, "")
	if err != nil {
		t.Fatal(err)
	}
	dial := ph.prov.dial
	swaps := 0
	ph.prov.dial = func(ctx context.Context, target sshexec.Target, a ...ssh.AuthMethod) (Remote, error) {
		r, e := dial(ctx, target, a...)
		if e != nil {
			return nil, e
		}
		// Observe without consuming the script's string-reader type.
		return &observedProberRemote{Remote: r, svc: ph.svc, onSwap: func() {
			swaps++
			var running int
			if err := ph.svc.pool.QueryRow(ctx, `SELECT count(*) FROM entry_jobs WHERE kind='prober' AND status='running'`).Scan(&running); err != nil || running != 1 {
				t.Errorf("concurrent prober jobs=%d err=%v", running, err)
			}
		}}, nil
	}
	ph.prov.RollProbers(ctx)
	if swaps != 2 {
		t.Fatalf("swaps=%d want 2", swaps)
	}
	status, err := ph.svc.readProberStatus(ctx, missing)
	if err != nil || !strings.Contains(status.ProberUpdateError, "ключ хоста") {
		t.Fatalf("missing key status %+v %v", status, err)
	}
}

type observedProberRemote struct {
	Remote
	svc    *Service
	onSwap func()
}

func (r *observedProberRemote) Run(ctx context.Context, cmd string, in io.Reader) (string, error) {
	if text, ok := in.(*strings.Reader); ok {
		data, _ := io.ReadAll(text)
		if string(data) == proberSwapScript {
			r.onSwap()
		}
		in = strings.NewReader(string(data))
	}
	return r.Remote.Run(ctx, cmd, in)
}

func TestSharedNekkoServerUsesHikkariPathsWithoutChangingBinaryBytes(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	var commands []string
	var uploads [][]byte
	base := &fakeRemote{b: ph.box}
	r := inspectProberRemote{Remote: base, run: func(cmd string, data []byte) (string, bool) {
		commands = append(commands, cmd)
		uploads = append(uploads, append([]byte(nil), data...))
		if strings.Contains(cmd, "then echo hikkari") {
			return "hikkari\n", true
		}
		return "", true
	}}
	selected, err := ph.prov.proberRemote(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("binary contains nekko-entry-prober text")
	if _, err = selected.Run(ctx, "cat > /usr/local/bin/nekko-entry-prober.new", bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	if commands[len(commands)-1] != "cat > /usr/local/bin/hikkari-entry-prober.new" || !bytes.Equal(uploads[len(uploads)-1], payload) {
		t.Fatal("wrong destination or binary was modified")
	}
	if _, err = selected.Run(ctx, "bash -s", strings.NewReader(proberSwapScript)); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(uploads[len(uploads)-1], []byte("nekko-entry-prober")) {
		t.Fatal("script still changes Nekko service")
	}
}

func TestProberRestartPreservesOriginalRollbackIdentity(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	v, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: "restart-" + uuid.NewString()[:8], Box: box}, "")
	if err != nil {
		t.Fatal(err)
	}
	original := vantageTokenHash(v.Token)
	_, prior, resume, err := ph.svc.beginProberToken(ctx, v.Slot, box)
	if err != nil || resume || !bytes.Equal(prior, original) {
		t.Fatalf("first deploy %v %v", resume, err)
	}
	token, prior, resume, err := ph.svc.beginProberToken(ctx, v.Slot, box)
	if err != nil || !resume || !bytes.Equal(prior, original) {
		t.Fatalf("resumed deploy lost original: %v %v", resume, err)
	}
	if err = ph.prov.restoreProberToken(ctx, v.Slot, token, prior); err != nil {
		t.Fatal(err)
	}
	if got, e := ph.svc.VantageByToken(ctx, v.Token); e != nil || got != v.Slot {
		t.Fatal("restart rollback lost original identity")
	}
	token, prior, resume, err = ph.svc.beginProberToken(ctx, v.Slot, box)
	if err != nil || !resume || !bytes.Equal(prior, original) {
		t.Fatal("retry lost original identity")
	}
	if err = ph.prov.finishProberToken(ctx, v.Slot, token); err != nil {
		t.Fatal(err)
	}
	_, prior, resume, err = ph.svc.beginProberToken(ctx, v.Slot, box)
	if err != nil || resume || !bytes.Equal(prior, vantageTokenHash(token)) {
		t.Fatal("confirmed build was not the next rollback baseline")
	}
	// A separate operator reissue must not resurrect an older revoked bearer.
	replacement, err := ph.svc.issueBoxVantageToken(ctx, v.Slot, box)
	if err != nil {
		t.Fatal(err)
	}
	_, prior, resume, err = ph.svc.beginProberToken(ctx, v.Slot, box)
	if err != nil || resume || !bytes.Equal(prior, vantageTokenHash(replacement)) {
		t.Fatal("external reissue was undone by resume state")
	}
}

func TestProberBackupResumePreservesOldFilesAndDropsStaleUpload(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	envDir := filepath.Join(root, "etc", "nekko-entry-prober")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(envDir, 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "nekko-entry-prober")
	env := filepath.Join(envDir, "prober.env")
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	backup := func(resume string) {
		t.Helper()
		script := strings.ReplaceAll(proberBackupScript, "/usr/local/bin", binDir)
		script = strings.ReplaceAll(script, "/etc/nekko-entry-prober", envDir)
		cmd := exec.Command("bash", "-s", "--", resume)
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("backup: %v %s", err, out)
		}
	}
	write(bin, "good")
	write(env, "original-token")
	backup("0")
	write(bin, "unconfirmed")
	write(env, "unconfirmed-token")
	write(bin+".new", "stale-upload")
	backup("1")
	data, _ := os.ReadFile(bin + ".old")
	if string(data) != "good" {
		t.Fatal("resume overwrote stable binary")
	}
	data, _ = os.ReadFile(env + ".old")
	if string(data) != "original-token" {
		t.Fatal("resume overwrote stable env")
	}
	if _, err := os.Stat(bin + ".new"); !os.IsNotExist(err) {
		t.Fatal("resume retained stale staged binary")
	}
	backup("0")
	data, _ = os.ReadFile(bin + ".old")
	if string(data) != "unconfirmed" {
		t.Fatal("new lifecycle did not replace stale pending backup")
	}
}
