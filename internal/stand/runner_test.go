package stand

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeExe writes an executable shell script.
func fakeExe(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunnerMeasuresEveryProfileTheKnobAppliesTo(t *testing.T) {
	entries, _ := json.Marshal([]Profile{
		{Label: "amsterdam-1-002", Config: realityOutbound()},
		{Label: "hy2-eu", Config: map[string]any{"type": "hysteria2", "server": "1.2.3.4", "server_port": float64(443)}},
	})
	// The parser must get the link on stdin and say nothing of it.
	parser := fakeExe(t, "census-parse", `read url; [ "$url" = "https://sub/secret-token" ] || { echo "bad stdin $url" >&2; exit 1; }
[ "$1" = "--with-config" ] || exit 1
cat <<'EOF'
`+string(entries)+`
EOF
`)
	bash := fakeExe(t, "bash", `cat > /dev/null
echo "B 1762211579 1 402705 ok 50 0"
echo "R 1762211581 0 1 1048576 ok 31 19 3"
echo "R 1762211583 1 1 1048576 ok 50 0 3"
`)
	var lines []string
	r := Runner{Kit: Local{Core: "/opt/kit/core", Parser: parser, Label: "sing-box-lx v1"}, Bash: bash,
		Log: func(f string, a ...any) { lines = append(lines, f) }}
	res := r.Run(context.Background(), Task{Knob: "fp", Values: []string{"chrome", "firefox"}, Rounds: 1, BaselinePath: "/", SubURL: "https://sub/secret-token"})
	if res.Error != "" {
		t.Fatalf("error %q", res.Error)
	}
	if res.Core != "sing-box-lx v1" || len(res.Rows) != 3 {
		t.Fatalf("result %+v", res)
	}
	for _, row := range res.Rows {
		if row.Profile != "amsterdam-1-002" {
			t.Fatalf("hysteria2 measured for fp: %+v", row)
		}
	}
	if res.Rows[1].Value != "chrome" || res.Rows[1].BurstTimeout != 19 || res.Rows[2].Value != "firefox" {
		t.Fatalf("rows %+v", res.Rows)
	}
	if len(lines) != 1 {
		t.Fatalf("log %v", lines)
	}
}

func TestRunnerSaysWhyAndKeepsTheLinkOut(t *testing.T) {
	parser := fakeExe(t, "census-parse", `read url; echo "fetch $url: 404" >&2; exit 1`)
	r := Runner{Kit: Local{Core: "/opt/kit/core", Parser: parser, Label: "x"}}
	res := r.Run(context.Background(), Task{Knob: "fp", Values: []string{"chrome"}, SubURL: "https://sub/secret-token"})
	if res.Error == "" || strings.Contains(res.Error, "secret-token") || !strings.Contains(res.Error, "[subscription]") {
		t.Fatalf("error %q", res.Error)
	}
	empty := fakeExe(t, "census-parse", `cat >/dev/null; echo '[]'`)
	res = Runner{Kit: Local{Core: "/opt/kit/core", Parser: empty, Label: "x"}}.Run(context.Background(), Task{Knob: "fp", Values: []string{"chrome"}, SubURL: "u"})
	if !strings.Contains(res.Error, "нет профиля") {
		t.Fatalf("an empty subscription passed silently: %q", res.Error)
	}
}

func TestAppliesKnowsWhichProfileHasTheKnob(t *testing.T) {
	hy2 := map[string]any{"type": "hysteria2"}
	if Applies("fp", hy2) || Applies("flow", hy2) || !Applies("port", hy2) || !Applies("burst", hy2) {
		t.Fatal("hysteria2")
	}
	if !Applies("fp", realityOutbound()) || !Applies("flow", realityOutbound()) {
		t.Fatal("reality")
	}
}
