package entry

import (
	"context"
	"io"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// standRecorder answers like a box and keeps what was written to it.
type standRecorder struct {
	cmds  []string
	stdin map[string]string
	files map[string]string
}

func (r *standRecorder) Run(_ context.Context, cmd string, stdin io.Reader) (string, error) {
	r.cmds = append(r.cmds, cmd)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		r.stdin[cmd] = string(b)
		if _, path, ok := strings.Cut(cmd, "cat > "); ok {
			r.files[path] = string(b)
		}
	}
	switch {
	case cmd == "uname -m":
		return "x86_64\n", nil
	case strings.HasPrefix(cmd, "cat "+wireStandSecretF):
		return r.files[wireStandSecretF], nil
	}
	return "", nil
}
func (r *standRecorder) HostKey() string { return "" }
func (r *standRecorder) Close() error    { return nil }

func newStandRecorder() *standRecorder {
	return &standRecorder{stdin: map[string]string{}, files: map[string]string{}}
}

// TestWireStandBothEnds: one secret on both boxes and kept across updates,
// the exit's ports and env per wire, the entry pointed at the exit; nothing
// of the trunk's entry, the relay or another unit is touched.
func TestWireStandBothEnds(t *testing.T) {
	builds := testBuild(t)
	ex, en := newStandRecorder(), newStandRecorder()
	addr := netip.MustParseAddr("192.0.2.10")
	nop := func(string, ...any) {}
	if err := standWireStand(context.Background(), ex, en, builds, addr, "stand.example", "1794360000-1794363600", nop, nop); err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(ex.files[wireStandSecretF])
	if !wireStandSecretRe.MatchString(secret) {
		t.Fatalf("secret %q", secret)
	}
	for _, w := range WireStandPorts {
		exEnv, enEnv := ex.files[wireStandDir+"/"+w.Wire+".env.new"], en.files[wireStandDir+"/"+w.Wire+".env.new"]
		for _, want := range []string{"EDGE_PROTO=wirestand", "WIRESTAND_WIRE=" + w.Wire, "WIRESTAND_SECRET=" + secret, "WIRESTAND_SNI=stand.example"} {
			if !strings.Contains(exEnv, want) || !strings.Contains(enEnv, want) {
				t.Fatalf("%s: no %q:\nexit %s\nentry %s", w.Wire, want, exEnv, enEnv)
			}
		}
		if !strings.Contains(exEnv, "WIRESTAND_ROLE=exit") || !strings.Contains(enEnv, "WIRESTAND_ROLE=entry") ||
			!strings.Contains(enEnv, "WIRESTAND_EXIT=192.0.2.10") || strings.Contains(exEnv, "WIRESTAND_EXIT") {
			t.Fatalf("%s: roles:\nexit %s\nentry %s", w.Wire, exEnv, enEnv)
		}
	}
	if !strings.Contains(en.files[wireStandDir+"/quic.env.new"], "WIRESTAND_BENCH=1794360000-1794363600") ||
		strings.Contains(ex.files[wireStandDir+"/quic.env.new"], "WIRESTAND_BENCH") {
		t.Fatal("the bench window is the entry's")
	}
	if _, ok := en.files[wireStandSecretF]; ok {
		t.Fatal("the entry keeps no secret file of its own")
	}
	for _, r := range []*standRecorder{ex, en} {
		joined := strings.Join(r.cmds, "\n")
		if r.stdin["cat > "+wireStandBinary+".new"] != "edge binary" {
			t.Fatalf("binary not staged to its own path:\n%s", joined)
		}
		for _, other := range []string{"nekko-trunk-entry", "nekko-edge", "/usr/local/bin/nekko-"} {
			if strings.Contains(joined, other) {
				t.Fatalf("touches %s:\n%s", other, joined)
			}
		}
	}
	if !strings.Contains(strings.Join(ex.cmds, "\n"), "bash -s -- exit quic=20443 salamander=20444 awg=20445 udp=20446") {
		t.Fatalf("exit script:\n%s", strings.Join(ex.cmds, "\n"))
	}
	// An update keeps the secret: the running entry's keys stay.
	if err := standWireStand(context.Background(), ex, en, builds, addr, "stand.example", "", nop, nop); err != nil {
		t.Fatal(err)
	}
	if again := strings.TrimSpace(ex.files[wireStandSecretF]); again != secret {
		t.Fatalf("secret changed on update")
	}
}

func TestWireBenchWindow(t *testing.T) {
	now := time.Date(2026, 11, 11, 1, 26, 0, 0, time.UTC)
	from, until := wireBenchWindow(now, time.Hour)
	if !from.Equal(time.Date(2026, 11, 11, 1, 40, 0, 0, time.UTC)) || !until.Equal(from.Add(time.Hour)) {
		t.Fatalf("%v %v", from, until)
	}
	from, _ = wireBenchWindow(time.Date(2026, 11, 11, 1, 24, 0, 0, time.UTC), time.Hour)
	if !from.Equal(time.Date(2026, 11, 11, 1, 30, 0, 0, time.UTC)) {
		t.Fatal(from)
	}
}
