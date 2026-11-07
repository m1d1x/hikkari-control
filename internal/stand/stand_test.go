package stand

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func realityOutbound() map[string]any {
	return map[string]any{
		"type": "vless", "tag": "p-amsterdam-1-002", "server": "146.103.121.11", "server_port": float64(8443),
		"uuid": "SECRET-UUID", "flow": "xtls-rprx-vision", "flow_retry_delay": "1s", "domain_resolver": "dns-direct",
		"tls": map[string]any{"enabled": true, "server_name": "www.samsung.com",
			"utls":    map[string]any{"enabled": true, "fingerprint": "chrome"},
			"reality": map[string]any{"enabled": true, "public_key": "SECRET-KEY"}},
	}
}

func TestMatrixConfigSetsOnlyTheKnob(t *testing.T) {
	ob := realityOutbound()
	cfg, _, err := Config(ob, Knobs["fp"], "firefox")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Inbounds  []map[string]any `json:"inbounds"`
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(strings.Replace(cfg, "__PORT__", "1080", 1)), &got); err != nil {
		t.Fatalf("%v: %s", err, cfg)
	}
	out := got.Outbounds[0]
	if fp := out["tls"].(map[string]any)["utls"].(map[string]any)["fingerprint"]; fp != "firefox" {
		t.Fatalf("fp %v", fp)
	}
	for _, k := range []string{"flow_retry_delay", "domain_resolver"} {
		if _, kept := out[k]; kept {
			t.Fatalf("kept %s, which a user's config does not carry", k)
		}
	}
	if out["flow"] != "xtls-rprx-vision" || out["tag"] != "probe" || got.Inbounds[0]["listen_port"] != float64(1080) {
		t.Fatalf("%v", got)
	}
	if ob["tls"].(map[string]any)["utls"].(map[string]any)["fingerprint"] != "chrome" {
		t.Fatal("the profile's own outbound changed")
	}
}

func TestMatrixKnobsRefuseWhatTheyCannotTake(t *testing.T) {
	for knob, v := range map[string]string{"port": "0", "sni": "a b"} {
		if _, _, err := Config(realityOutbound(), Knobs[knob], v); err == nil {
			t.Fatalf("%s=%q taken", knob, v)
		}
	}
	plain := map[string]any{"type": "hysteria2", "server": "1.2.3.4", "server_port": float64(443)}
	if _, _, err := Config(plain, Knobs["fp"], "firefox"); err == nil {
		t.Fatal("fp set on a profile with no TLS")
	}
}

func testPlan(t *testing.T) Plan {
	p := Plan{Profile: "amsterdam-1-002", Knob: "fp", Values: []string{"chrome", "firefox"},
		Server: "146.103.121.11", SNI: "www.samsung.com", Port: 8443, Rounds: 2, Core: "test"}
	for _, v := range p.Values {
		cfg, _, err := Config(realityOutbound(), Knobs["fp"], v)
		if err != nil {
			t.Fatal(err)
		}
		p.Configs = append(p.Configs, cfg)
	}
	return p
}

func TestMatrixScriptIsValidBashAndCarriesNoSecretInClear(t *testing.T) {
	script, err := Script(testPlan(t), "/tmp/hikkari-matrix/sing-box")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "SECRET") {
		t.Fatal("a secret in clear in the script")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v: %s", err, out)
	}
	if strings.Count(script, "  variant ") != 2 || !strings.Contains(script, "baseline $r") {
		t.Fatalf("rounds do not go value by value:\n%s", script)
	}
}

func TestParseMatrixReadsRowsAndNamesValues(t *testing.T) {
	p := testPlan(t)
	rows := Parse(`B 1762211579 1 402705 ok 50 0
R 1762211581 0 1 1048576 ok 0 50 0
R 1762211583 1 1 1048576 ok 49 0 3
R 1762211585 1 2 load_error
garbage
R 1762211587 7 1 1 ok 1 1 1
`, "spb", p)
	if len(rows) != 4 {
		t.Fatalf("%+v", rows)
	}
	if rows[0].Knob != Baseline || rows[0].Burst != 50 || rows[0].Dl != 402705 || rows[0].TS != time.Unix(1762211579, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("baseline %+v", rows[0])
	}
	if rows[1].Value != "chrome" || rows[1].BurstTimeout != 50 || rows[2].Value != "firefox" || rows[2].After != 3 {
		t.Fatalf("%+v", rows[1:3])
	}
	if !rows[3].LoadError || rows[3].Round != 2 {
		t.Fatalf("%+v", rows[3])
	}
	sum := Summary(rows)
	if !strings.Contains(sum, "firefox") || !strings.Contains(sum, "49/50") || !strings.Contains(sum, "0/50") {
		t.Fatalf("summary:\n%s", sum)
	}
}

func TestParseMatrixReadsADeadRound(t *testing.T) {
	p := testPlan(t)
	rows := Parse(`D 1762211579 1 addr_dead 12
D 1762211589 2 no_tcp 0
D 1762211599 3 vantage_down 0
D 1762211609 4 something_else 0
`, "spb", p)
	if len(rows) != 3 {
		t.Fatalf("%+v", rows)
	}
	if rows[0].Knob != Baseline || rows[0].Value != ValueAddrDead || rows[0].Burst != 0 || rows[0].BurstTotal != AddrFlows || rows[0].BurstTimeout != 12 || rows[0].Round != 1 {
		t.Fatalf("%+v", rows[0])
	}
	if rows[1].Value != ValueNoTCP || rows[2].Value != ValueVantageDown {
		t.Fatalf("%+v", rows[1:])
	}
	data, err := DatasetRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		if err := data[i].Validate(); err != nil {
			t.Fatalf("%+v: %v", data[i], err)
		}
	}
}

// controlCurl puts a curl on PATH that answers for the point's control as the
// test says and is the real curl for everything else: what curl reports about a
// silent connection is the very thing under test (on a timeout it says
// time_connect 0 even with TCP open).
func controlCurl(t *testing.T, control string) string {
	t.Helper()
	real, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("no curl")
	}
	dir := t.TempDir()
	script := "#!/bin/bash\ncase \"$*\" in\n*1.1.1.1*) " + control + ";;\n*) exec " + real + " \"$@\";;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// addrListener is a real listener on loopback doing what serve says with each
// connection; its port is free again once the test closes it.
func addrListener(t *testing.T, serve func(net.Conn)) (int, func()) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, func() { _ = l.Close() }
}

// TestScriptStopsARoundOnADeadAddress runs the script's own address check with
// real curl against real sockets.
func TestScriptStopsARoundOnADeadAddress(t *testing.T) {
	silent := func(c net.Conn) { time.Sleep(time.Minute); _ = c.Close() }
	var answers atomic.Int64
	oneAnswers := func(c net.Conn) {
		if answers.Add(1)%2 == 0 { // the check opens each flow twice: TCP, then TLS
			_ = c.Close()
			return
		}
		silent(c)
	}
	closing := func(c net.Conn) { _ = c.Close() }
	for _, c := range []struct {
		name     string
		serve    func(net.Conn) // nil: nobody listens
		control  string
		want     string
		variants bool
	}{
		{name: "TCP opens, nothing comes back", serve: silent, control: "exit 0", want: "addr_dead 12"},
		{name: "no TCP at all", control: "exit 0", want: "no_tcp 0"},
		{name: "the point itself is down", control: "exit 7", want: "vantage_down 0"},
		{name: "some flows answer: not dead", serve: oneAnswers, control: "exit 0", variants: true},
		{name: "a closed connection is an answer", serve: closing, control: "exit 0", variants: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var port int
			if c.serve != nil {
				p, stop := addrListener(t, c.serve)
				defer stop()
				port = p
			} else {
				p, stop := addrListener(t, closing)
				stop()
				port = p
			}
			plan, err := NewPlan(map[string]any{"type": "vless", "server": "127.0.0.1", "server_port": port, "tag": "p",
				"tls": map[string]any{"enabled": true, "server_name": "www.example.com"}},
				Spec{Profile: "p", Knob: "fp", Values: []string{"firefox"}, Rounds: 1})
			if err != nil {
				t.Fatal(err)
			}
			script, err := Script(plan, "/nonexistent/core")
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-s")
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "PATH="+controlCurl(t, c.control)+":"+os.Getenv("PATH"))
			out, _ := cmd.CombinedOutput()
			text := string(out)
			if c.variants {
				if strings.Contains(text, "D ") {
					t.Fatalf("an answering address read as dead:\n%s", text)
				}
				return
			}
			if !strings.Contains(text, " 1 "+c.want+"\n") {
				t.Fatalf("want %q:\n%s", c.want, text)
			}
			if strings.Contains(text, "R ") || strings.Contains(text, "B ") {
				t.Fatalf("a dead round measured a value:\n%s", text)
			}
		})
	}
}

func TestPlanOfAUDPProfileAsksNoTCP(t *testing.T) {
	plan, err := NewPlan(map[string]any{"type": "hysteria2", "server": "1.2.3.4", "server_port": 443, "tag": "p",
		"tls": map[string]any{"enabled": true, "server_name": "x.example.com"}}, Spec{Profile: "p", Knob: "alpn", Values: []string{"h3"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SNI != "" {
		t.Fatalf("SNI %q", plan.SNI)
	}
	script, err := Script(plan, "/tmp/core")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "addrcheck() { :; }") {
		t.Fatal("a UDP profile would ask TCP whether its address is alive")
	}
}

func TestMatrixALPNNamesHTTP11(t *testing.T) {
	ob := realityOutbound()
	if err := Knobs["alpn"](ob, "h2/http1.1"); err != nil {
		t.Fatal(err)
	}
	tls := ob["tls"].(map[string]any)
	got := fmt.Sprint(tls["alpn"])
	if got != "[h2 http/1.1]" {
		t.Fatalf("alpn %s", got)
	}
	if err := Knobs["alpn"](realityOutbound(), "http/1.1"); err == nil {
		t.Fatal("http/1.1 split into two names passed")
	}
}

func TestMatrixModeSetsTheXHTTPMode(t *testing.T) {
	ob := realityOutbound()
	ob["transport"] = map[string]any{"type": "xhttp", "mode": "stream-up", "path": "/x"}
	cfg, params, err := Config(ob, Knobs["mode"], "packet-up")
	if err != nil {
		t.Fatal(err)
	}
	if params["mode"] != "packet-up" || !strings.Contains(cfg, `"mode":"packet-up"`) || !strings.Contains(cfg, `"path":"/x"`) {
		t.Fatalf("params %v config %s", params, cfg)
	}
	if ob["transport"].(map[string]any)["mode"] != "stream-up" {
		t.Fatal("the profile's own outbound changed")
	}
	if _, _, err := Config(ob, Knobs["mode"], "stream_up"); err == nil {
		t.Fatal("an unknown mode passed")
	}
	if _, _, err := Config(realityOutbound(), Knobs["mode"], "packet-up"); err == nil {
		t.Fatal("mode set on a profile with no XHTTP")
	}
}

func TestMatrixBaseSetsEveryValue(t *testing.T) {
	ob := realityOutbound()
	base, err := ApplyBase(ob, "fp=chrome;alpn=http1.1", "sni")
	if err != nil || base != "fp=chrome;alpn=http1.1" {
		t.Fatalf("base %q err %v", base, err)
	}
	cfg, params, err := Config(ob, Knobs["sni"], "www.nvidia.com")
	if err != nil {
		t.Fatal(err)
	}
	if params["fp"] != "chrome" || params["alpn"] != "http/1.1" || params["sni"] != "www.nvidia.com" {
		t.Fatalf("params %v", params)
	}
	if !strings.Contains(cfg, `"server_name":"www.nvidia.com"`) {
		t.Fatal(cfg)
	}
	if _, err := ApplyBase(realityOutbound(), "sni=www.samsung.com", "sni"); err == nil {
		t.Fatal("base set the varied knob")
	}
	if _, err := ApplyBase(realityOutbound(), "burst=3", "fp"); err == nil {
		t.Fatal("base set the burst")
	}
}

func TestMatrixBurstKnobSizesTheBurst(t *testing.T) {
	p := Plan{Profile: "x", Knob: "burst", Values: []string{"3", "50"}, Bursts: []int{3, 50}, Rounds: 1, Configs: []string{"{}", "{}"}}
	script, err := Script(p, "/tmp/hikkari-matrix/sing-box")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `variant 0 $r "$CFG0" 3;`) || !strings.Contains(script, `variant 1 $r "$CFG1" 50;`) {
		t.Fatal(script)
	}
	rows := Parse("R 1700000000 0 1 1048576 ok 3 0 3\nR 1700000000 1 1 1048576 ok 20 30 0", "spb", p)
	if len(rows) != 2 || rows[0].BurstTotal != 3 || rows[1].BurstTotal != 50 || rows[0].Value != "3" {
		t.Fatalf("rows %+v", rows)
	}
	d, err := DatasetRows(rows)
	if err != nil || d[0].BurstTotal != 3 || d[0].Validate() != nil {
		t.Fatalf("dataset %+v err %v / %v", d, err, d[0].Validate())
	}
}

func TestNewPlanLeavesTheProfileAsItIs(t *testing.T) {
	ob := realityOutbound()
	p, err := NewPlan(ob, Spec{Profile: "amsterdam-1-002", Knob: "alpn", Values: []string{"h2", "http1.1"}, Base: "fp=firefox", BasePath: "/us/"})
	if err != nil {
		t.Fatal(err)
	}
	if ob["tls"].(map[string]any)["utls"].(map[string]any)["fingerprint"] != "chrome" {
		t.Fatal("the base changed the profile's own outbound")
	}
	if p.Rounds != Rounds || p.BasePath != "/us/" || p.SNI != "www.samsung.com" || p.Port != 8443 || len(p.Configs) != 2 {
		t.Fatalf("%+v", p)
	}
	if p.Params[0]["fp"] != "firefox" || p.Params[1]["alpn"] != "http/1.1" {
		t.Fatalf("params %v", p.Params)
	}
	for _, bad := range []Spec{
		{Profile: "x", Knob: "fp", Values: nil},
		{Profile: "x", Knob: "nope", Values: []string{"a"}},
		{Profile: "x", Knob: "fp", Values: []string{"a b"}},
		{Profile: "x", Knob: "fp", Values: []string{"chrome"}, BasePath: "us"},
		{Profile: "x", Knob: "fp", Values: []string{"chrome"}, Rounds: MaxRounds + 1},
	} {
		if _, err := NewPlan(realityOutbound(), bad); err == nil {
			t.Fatalf("%+v taken", bad)
		}
	}
}

func TestScriptRefusesACorePathTheShellWouldRead(t *testing.T) {
	for _, core := range []string{"", "sing-box", "/tmp/a b", "/tmp/$(id)", "/tmp/a;b"} {
		if _, err := Script(testPlan(t), core); err == nil {
			t.Fatalf("core %q taken", core)
		}
	}
}
