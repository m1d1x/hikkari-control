package entry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// namedBuild is a build of the given content, like testBuild.
func namedBuild(t *testing.T, name, body string) map[string]EdgeBuild {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return map[string]EdgeBuild{"amd64": {Arch: "amd64", SHA: hex.EncodeToString(sum[:]), Size: int64(len(body)), path: path}}
}

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
	builds := wireStandKit{edge: testBuild(t), h3: namedBuild(t, WireStandH3Build, "h3 edge binary"), cronet: namedBuild(t, "libcronet.so", "cronet library")}
	ex, en := newStandRecorder(), newStandRecorder()
	addr := netip.MustParseAddr("192.0.2.10")
	nop := func(string, ...any) {}
	if err := standWireStand(context.Background(), ex, en, builds, addr, "stand.example", "1794360000-1794363600", map[string]string{"h3": "1794360060:u1x4,0x1"}, "bbr", nop, nop); err != nil {
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
	// The exit runs the relay's edge; the entry the one built with cronet,
	// libcronet.so beside the stand, and only h3 loads it.
	if ex.stdin["cat > "+wireStandBinary+".new"] != "edge binary" || en.stdin["cat > "+wireStandBinary+".new"] != "h3 edge binary" ||
		en.stdin["cat > "+wireStandCronet+".new"] != "cronet library" || ex.stdin["cat > "+wireStandCronet+".new"] != "" {
		t.Fatalf("builds:\nexit %s\nentry %s", strings.Join(ex.cmds, "\n"), strings.Join(en.cmds, "\n"))
	}
	h3 := en.files[wireStandDir+"/h3.env.new"]
	for _, want := range []string{"WIRESTAND_CRONET=" + wireStandCronet, "WIRESTAND_LOAD=1794360060:u1x4,0x1", "WIRESTAND_H3_CC=bbr", "WIRESTAND_PORT=20450"} {
		if !strings.Contains(h3, want) {
			t.Fatalf("h3 entry env lacks %q:\n%s", want, h3)
		}
	}
	if strings.Contains(en.files[wireStandDir+"/quic.env.new"], "WIRESTAND_LOAD") || strings.Contains(ex.files[wireStandDir+"/h3.env.new"], "WIRESTAND_LOAD") {
		t.Fatal("a load only where the plan says, on the entry")
	}
	if !strings.Contains(en.files["/etc/systemd/system/"+wireStandUnit+"@.service"], "IPAccounting=yes") {
		t.Fatal("the unit counts the wire's packets")
	}
	for _, r := range []*standRecorder{ex, en} {
		joined := strings.Join(r.cmds, "\n")
		for _, other := range []string{"nekko-trunk-entry", "nekko-edge", "/usr/local/bin/nekko-"} {
			if strings.Contains(joined, other) {
				t.Fatalf("touches %s:\n%s", other, joined)
			}
		}
	}
	if !strings.Contains(strings.Join(ex.cmds, "\n"), "bash -s -- exit quic=20443 salamander=20444 awg=20445 udp=20446 h3=20450") {
		t.Fatalf("exit script:\n%s", strings.Join(ex.cmds, "\n"))
	}
	// An update keeps the secret: the running entry's keys stay.
	if err := standWireStand(context.Background(), ex, en, builds, addr, "stand.example", "", nil, "", nop, nop); err != nil {
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

// TestWireLoadPlans: steps interleaved over the wires in five minutes each,
// from the first :x1 at least 5 minutes away, load in the four quiet minutes.
func TestWireLoadPlans(t *testing.T) {
	l := WireLoad{Wires: []string{"h3", "quic"}, Steps: []string{"u1", "g4"}}
	if err := l.Check(); err != nil {
		t.Fatal(err)
	}
	plans, from, until := l.plans(time.Date(2026, 11, 12, 10, 3, 0, 0, time.UTC))
	if !from.Equal(time.Date(2026, 11, 12, 10, 11, 0, 0, time.UTC)) || !until.Equal(from.Add(20*time.Minute)) {
		t.Fatalf("%v %v", from, until)
	}
	f := strconv.FormatInt(from.Unix(), 10) + ":"
	if plans["h3"] != f+"u1x4,0x1,0x5,g4x4,0x1" || plans["quic"] != f+"0x5,u1x4,0x1,0x5,g4x4,0x1" {
		t.Fatalf("%v", plans)
	}
	for _, bad := range []WireLoad{{Wires: []string{"awg"}, Steps: []string{"u1"}}, {Wires: []string{"h3"}, Steps: []string{"x1"}},
		{Wires: []string{"h3"}, Steps: []string{"u17"}}, {Wires: []string{"h3", "h3"}, Steps: []string{"u1"}}, {Wires: []string{"h3"}, Steps: []string{"u1"}, CC: "vegas"}} {
		if bad.Check() == nil {
			t.Fatalf("%+v taken", bad)
		}
	}
}
