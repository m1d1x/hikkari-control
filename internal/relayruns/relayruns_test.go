package relayruns

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A [diag:config] line as tester-002's client wrote it at 01:25 MSK on
// 04.11, shortened and cut mid-route the way the client cuts it at 8 KB.
const configFirefox = `{"dns":{"final":"dns-direct","rules":[{"domain_suffix":["ru"],"server":"dns-direct"}]},` +
	`"endpoints":[{"address":["[REDACTED_IP]/32"],"tag":"p-amnezia-premium","type":"wireguard"}],` +
	`"log":{"level":"warn"},"outbounds":[{"tag":"direct","type":"direct"},` +
	`{"flow_retry_delay":"1s","server":"[REDACTED_IP]","server_port":443,"tag":"p-profile","tls":{"alpn":["h2","http/1.1"],"enabled":true,"server_name":"web.zovfy.ru","utls":{"enabled":true,"fingerprint":"edge"}},"transport":{"type":"xhttp"},"type":"vless","uuid":"c18c…"},` +
	`{"flow":"xtls-rprx-vision","server":"[REDACTED_IP]","server_port":8443,"tag":"p-amsterdam-1-002","tls":{"enabled":true,"reality":{"enabled":true},"server_name":"www.samsung.com","utls":{"enabled":true,"fingerprint":"firefox"}},"type":"vless","uuid":"87ac…"},` +
	`{"default":"p-profile","outbounds":["p-profile"],"tag":"proxy","type":"selector"}],"route":{"auto_detect_interface":true,"rules":[{"action":"sniff"},{"outbound":"direct","process_name":["dota2.exe","start_ …[обрезано]`

func TestConfigParamsReadsCutConfig(t *testing.T) {
	got := configParams(configFirefox)
	ams := got["p-amsterdam-1-002"]
	want := map[string]string{"fp": "firefox", "sni": "www.samsung.com", "port": "8443", "flow": "xtls-rprx-vision"}
	for k, v := range want {
		if ams[k] != v {
			t.Errorf("amsterdam %s = %q, want %q (all: %v)", k, ams[k], v, ams)
		}
	}
	if _, ok := ams["alpn"]; ok {
		t.Errorf("amsterdam has no alpn, got %q", ams["alpn"])
	}
	prof := got["p-profile"]
	if prof["fp"] != "edge" || prof["alpn"] != "h2/http/1.1" || prof["flow"] != "none" {
		t.Errorf("p-profile = %v", prof)
	}
	if _, ok := got["direct"]; ok {
		t.Error("non-profile outbound kept")
	}
}

func TestConfigParamsCutBeforeOutbounds(t *testing.T) {
	if got := configParams(`{"dns":{"final":"dns-direct","rules":[{"domain_suffix":["ru" …[обрезано]`); len(got) != 0 {
		t.Errorf("got %v from a config cut before its outbounds", got)
	}
	cut := configFirefox[:strings.Index(configFirefox, `"tag":"p-amsterdam-1-002"`)]
	got := configParams(cut)
	if _, ok := got["p-amsterdam-1-002"]; ok {
		t.Error("a profile cut in half was read")
	}
	if got["p-profile"]["fp"] != "edge" {
		t.Errorf("the whole profile before the cut was lost: %v", got)
	}
}

func TestParseLoadLine(t *testing.T) {
	l, ok := parseLoadLine("2026/11/04 00:31:42.139000 [diag:load] p-amsterdam-1-002 proto=vless-reality dl=1048576/1048576 took=656ms end=ok burst=0/50 timeout=50 med=- after=0/3 med=-")
	if !ok {
		t.Fatal("not read")
	}
	r := l.row("t-1", time.Unix(1000, 0), "AS12389", map[string]string{"fp": "chrome"})
	if r.Profile != "amsterdam-1-002" || r.Proto != "vless-reality" || r.Dl != 1048576 || r.DlEnd != "ok" ||
		r.Burst != 0 || r.BurstTotal != 50 || r.BurstTimeout != 50 || r.After != 0 || r.AfterTotal != 3 || r.Params["fp"] != "chrome" {
		t.Errorf("row = %+v", r)
	}
	if r.Key != "t-1|2026/11/04 00:31:42.139000|p-amsterdam-1-002" {
		t.Errorf("key = %q", r.Key)
	}
	if err := r.Validate(); err != nil {
		t.Error(err)
	}

	// A newer client writes the knobs on the line: they win over the config.
	l, _ = parseLoadLine("2026/11/04 05:00:00 [diag:load] p-a proto=vless-reality fp=safari sni=www.samsung.com port=8443 dl=10/1048576 took=1s end=timeout burst=3/50 timeout=40 med=1s after=1/3 med=1s")
	r = l.row("t-1", time.Unix(1000, 0), "", map[string]string{"fp": "chrome"})
	if r.Params["fp"] != "safari" || r.Params["port"] != "8443" || r.DlEnd != "timeout" || r.Burst != 3 {
		t.Errorf("row = %+v", r)
	}

	l, ok = parseLoadLine(`2026/11/04 05:00:00 [diag:load] p-a proto=hysteria2 load_error="core: bind: address in use"`)
	if !ok {
		t.Fatal("load_error not read")
	}
	if r = l.row("t-1", time.Unix(1000, 0), "", nil); !r.LoadError || r.Validate() != nil {
		t.Errorf("row = %+v err %v", r, r.Validate())
	}

	for _, line := range []string{
		"2026/11/04 01:27:35 [diag:load nonce=b9] started profiles=2",
		"2026/11/04 01:27:40 [diag:load] on request: done",
	} {
		if _, ok := parseLoadLine(line); ok {
			t.Errorf("read a service line: %q", line)
		}
	}
}

type fakeChunks struct{ before map[string]string }

func (f fakeChunks) loadChunks(context.Context, int64, int) ([]chunk, error) { return nil, nil }
func (f fakeChunks) lastBefore(_ context.Context, _ string, _ int64, marker string) (string, error) {
	return f.before[marker], nil
}

func TestRowsOfTakesConfigInForce(t *testing.T) {
	at := time.Date(2026, 11, 3, 22, 30, 0, 0, time.UTC)
	chrome := strings.ReplaceAll(configFirefox, `"fingerprint":"firefox"`, `"fingerprint":"chrome"`)
	in := &Ingester{chunks: fakeChunks{before: map[string]string{
		"[diag:config] ":     "2026/11/04 01:00:03 [diag:config] " + chrome,
		"[diag:reach] self ": "2026/11/04 01:20:00 [diag:reach] self iface=eth isp=AS12389",
	}}}
	body := strings.Join([]string{
		"2026/11/04 01:01:00 [diag:load] p-amsterdam-1-002 proto=vless-reality dl=0/1048576 took=219ms end=closed burst=0/50 timeout=0 med=- after=0/3 med=-",
		"2026/11/04 01:25:43 [diag:config] " + configFirefox,
		"2026/11/04 01:27:40 [diag:load] p-amsterdam-1-002 proto=vless-reality dl=1048576/1048576 took=534ms end=ok burst=49/50 timeout=0 med=266ms after=3/3 med=165ms",
	}, "\n")
	rows, err := in.rowsOf(context.Background(), chunk{id: 7, install: "t-cbfed", body: body, receivedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].Params["fp"] != "chrome" || rows[1].Params["fp"] != "firefox" {
		t.Errorf("fp = %q then %q, want chrome then firefox", rows[0].Params["fp"], rows[1].Params["fp"])
	}
	if rows[0].Network != "AS12389" || rows[0].Vantage != "t-cbfed" || rows[0].Source != SourceTester {
		t.Errorf("row = %+v", rows[0])
	}
}

func TestRowsOfWithoutConfigKeepsNoParams(t *testing.T) {
	in := &Ingester{chunks: fakeChunks{}}
	rows, err := in.rowsOf(context.Background(), chunk{id: 1, install: "t-x", receivedAt: time.Now().Add(-time.Hour),
		body: "2026/11/04 01:01:00 [diag:load] p-a proto=vless-reality dl=1/1048576 took=1s end=closed burst=0/50 timeout=0 med=- after=0/3 med=-"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v err %v", rows, err)
	}
	if len(rows[0].Params) != 0 {
		t.Errorf("params guessed: %v", rows[0].Params)
	}
	if v := valueOf(rows[0], "fp"); v != ValueUnknown {
		t.Errorf("value = %q", v)
	}
}

func TestBuildChart(t *testing.T) {
	now := time.Date(2026, 11, 4, 3, 40, 0, 0, time.UTC)
	mk := func(src, vantage, fp string, at time.Time, burst int) Row {
		return Row{Source: src, Vantage: vantage, Profile: "amsterdam-1-002", MeasuredAt: at, Knob: "fp", Value: fp,
			Params: map[string]string{"fp": fp}, Dl: 1 << 20, DlEnd: "ok", Burst: burst, BurstTotal: 50, After: 3, AfterTotal: 3}
	}
	h := now.Truncate(time.Hour)
	rows := []Row{
		mk(SourceStand, "spb", "chrome", h.Add(5*time.Minute), 30),
		mk(SourceStand, "spb", "chrome", h.Add(6*time.Minute), 40),
		mk(SourceStand, "spb", "firefox", h.Add(7*time.Minute), 49),
		mk(SourceTester, "t-cbfed", "firefox", h.Add(-2*time.Hour), 50),
		{Source: SourceStand, Vantage: "spb", Profile: "amsterdam-1-002", MeasuredAt: h.Add(5 * time.Minute), Knob: KnobBaseline,
			Value: "no-protocol", Dl: 1, DlEnd: "ok", Burst: 50, BurstTotal: 50},
		mk(SourceStand, "spb", "chrome", now.Add(-48*time.Hour), 0), // outside the window
		{Source: SourceStand, Vantage: "lv", Profile: "other", MeasuredAt: h, Burst: 1, BurstTotal: 50},
	}
	c := BuildChart(rows, "24h", "fp", "amsterdam-1-002", now, func(id string) string { return "tester-002" })
	if len(c.Buckets) != 24 || c.BucketSeconds != 3600 {
		t.Fatalf("buckets %d step %d", len(c.Buckets), c.BucketSeconds)
	}
	got := map[string]Series{}
	for _, s := range c.Series {
		got[s.Key] = s
	}
	if len(got) != 4 {
		t.Fatalf("series %v", c.Series)
	}
	last := len(c.Buckets) - 1
	if s := got["chrome · spb"]; s.BurstPct[last] == nil || *s.BurstPct[last] != 70 || s.Rounds[last] != 2 || s.TotalRounds != 2 {
		t.Errorf("chrome spb = %+v", s)
	}
	if s := got["firefox · tester-002"]; s.BurstPct[last-2] == nil || *s.BurstPct[last-2] != 100 || s.BurstPct[last] != nil {
		t.Errorf("firefox tester = %+v", s)
	}
	if _, ok := got["no-protocol · spb"]; !ok {
		t.Error("baseline series missing")
	}
	if s := got["chrome · spb"]; s.BurstPct[0] != nil {
		t.Error("an empty bucket drew a value")
	}
}

func TestValidate(t *testing.T) {
	ok := Row{Source: SourceStand, MeasuredAt: time.Now(), Vantage: "spb", Profile: "amsterdam-1-002", Knob: "fp", Value: "chrome",
		Core:   "sing-box-lx v1.14.3-lx.21+489ac0f3",
		Params: map[string]string{"fp": "chrome"}, Burst: 3, BurstTotal: 50, AfterTotal: 3, DlEnd: "ok"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(r *Row){
		func(r *Row) { r.Source = "x" },
		func(r *Row) { r.Knob = "uuid" },
		func(r *Row) { r.Params = map[string]string{"uuid": "x"} },
		func(r *Row) { r.Value = "a b" },
		func(r *Row) { r.Burst = 51 },
		func(r *Row) { r.MeasuredAt = time.Now().Add(time.Hour) },
		func(r *Row) { r.Profile = "" },
		func(r *Row) { r.DlEnd = "weird" },
		func(r *Row) { r.Core = "a\nb" },
	}
	for i, f := range bad {
		r := ok
		r.Params = map[string]string{"fp": "chrome"}
		f(&r)
		if r.Validate() == nil {
			t.Errorf("case %d passed", i)
		}
	}
}
