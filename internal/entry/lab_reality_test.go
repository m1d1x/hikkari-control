package entry

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"
)

// labRecorder answers like a box: the env file the last stand wrote, uname, sha.
type labRecorder struct {
	env   string
	cmds  []string
	stdin map[string]string
}

func (r *labRecorder) Run(_ context.Context, cmd string, stdin io.Reader) (string, error) {
	r.cmds = append(r.cmds, cmd)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		r.stdin[cmd] = string(b)
	}
	switch {
	case cmd == "uname -m":
		return "x86_64\n", nil
	case strings.HasPrefix(cmd, "cat "+labRealityEnvFile):
		return r.env, nil
	case strings.HasPrefix(cmd, "umask 077; cat > "+labRealityEnvFile):
		r.env = r.stdin[cmd]
	}
	return "", nil
}
func (r *labRecorder) HostKey() string { return "" }
func (r *labRecorder) Close() error    { return nil }

func TestStandLabRealityKeepsItsIdentityAndItsOwnBinary(t *testing.T) {
	builds := testBuild(t)
	r := &labRecorder{stdin: map[string]string{}}
	first, err := standLabReality(context.Background(), r, builds, 443, "www.samsung.com", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.cmds, "\n")
	if r.stdin["cat > "+labRealityBinary+".new"] != "edge binary" {
		t.Fatalf("binary not staged to its own path:\n%s", joined)
	}
	if strings.Contains(joined, "/usr/local/bin/nekko-edge") || strings.Contains(joined, provokeBinary) {
		t.Fatalf("touches another unit's binary:\n%s", joined)
	}
	for _, want := range []string{"EDGE_PROTO=vless-reality-vision", "EDGE_LISTEN=0.0.0.0:443", "EDGE_COVERS=www.samsung.com",
		"EDGE_LOCAL_PORT_BASE=21001", "EDGE_ECHO_PORT=21901", "EDGE_CLIENTS=" + first.Client} {
		if !strings.Contains(r.env, want) {
			t.Fatalf("env has no %q:\n%s", want, r.env)
		}
	}
	if strings.Contains(r.env, "EDGE_PULSE") || strings.Contains(r.env, "HUB") {
		t.Fatalf("lab relay reports to the hub:\n%s", r.env)
	}
	second, err := standLabReality(context.Background(), r, builds, 443, "www.samsung.com", func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("identity changed on update: %+v -> %+v", first, second)
	}
}

func TestLabRealityLinkIsWhatTheClientParses(t *testing.T) {
	id, err := newLabRealityIdentity()
	if err != nil {
		t.Fatal(err)
	}
	link, err := labRealityLink(id, "130.49.175.145", 443, "www.samsung.com", "lab-reality-box")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	pub, _ := id.publicKey()
	if u.Scheme != "vless" || u.User.Username() != id.Client || u.Host != "130.49.175.145:443" || u.Fragment != "lab-reality-box" {
		t.Fatalf("link %s", link)
	}
	if q.Get("security") != "reality" || q.Get("flow") != "xtls-rprx-vision" || q.Get("pbk") != pub || q.Get("sid") != id.ShortID ||
		q.Get("sni") != "www.samsung.com" || q.Get("fp") != "chrome" {
		t.Fatalf("query %v", q)
	}
	back, ok := labRealityIdentityOf(labRealityEnv(id, 443, "www.samsung.com"))
	if !ok || back != id {
		t.Fatalf("env does not read back: %+v", back)
	}
}
