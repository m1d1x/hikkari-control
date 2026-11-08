package voiceruns

import (
	"testing"
	"time"
)

var at = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func TestParseThePeriodicTunnelLine(t *testing.T) {
	line := "2026/11/08 15:00:01.250 [diag:voice] path=tun profile=p-003 proto=hysteria2 isp=AS12389 isp_at=card mobile=0 target=cloudflare sent=1500 recv=1497 loss=0.20 loss_run=2 rtt_p50=47 rtt_p95=55 rtt_max=180 jitter=2.1 gaps60=1 gaps150=0 gap_max=72 reorder=0 dup=0"
	r, ok := parseLine(line, "t-1", at, "1.0.60")
	if !ok {
		t.Fatal("not read")
	}
	if r.Path != "tun" || r.Profile != "p-003" || r.Proto != "hysteria2" || r.Network != "AS12389" || r.NetworkAt != "card" || r.Mobile || r.OnRequest {
		t.Fatalf("identity: %+v", r)
	}
	if r.Sent != 1500 || r.Recv != 1497 || r.LossRun != 2 || r.Gaps60 != 1 || r.Gaps150 != 0 || *r.GapMax != 72 || *r.RTTP50 != 47 || *r.Jitter != 2.1 {
		t.Fatalf("counts: %+v", r)
	}
	if r.key != "t-1|2026/11/08 15:00:01.250|tun" || r.ClientVersion != "1.0.60" {
		t.Fatalf("key %q version %q", r.key, r.ClientVersion)
	}
}

func TestParseTheAnswerToProbeVoice(t *testing.T) {
	line := "2026/11/08 15:00:01.250 [diag:voice nonce=ab12] path=direct iface=Ethernet isp=AS8359 isp_at=card mobile=1 target=cloudflare sent=1500 recv=0 loss=100.00 loss_run=1500"
	r, ok := parseLine(line, "t-1", at, "")
	if !ok || !r.OnRequest || r.Path != "direct" || r.Iface != "Ethernet" || !r.Mobile {
		t.Fatalf("answer line: %v %+v", ok, r)
	}
	if r.Recv != 0 || r.RTTP50 != nil || r.GapMax != nil {
		t.Fatalf("nothing back still carries times: %+v", r)
	}
}

func TestParseAnErrorLine(t *testing.T) {
	line := `2026/11/08 15:00:01.250 [diag:voice] path=tun profile=p-1 proto=vless-reality isp=unknown isp_at=baseline mobile=0 error="target not resolved"`
	r, ok := parseLine(line, "t-1", at, "")
	if !ok || r.Error != "target not resolved" || r.Sent != 0 {
		t.Fatalf("error line: %v %+v", ok, r)
	}
	// A series that measured and then failed keeps both.
	line = `2026/11/08 15:00:01.250 [diag:voice] path=tun profile=p-1 proto=vless isp=AS1 isp_at=card mobile=0 target=cloudflare sent=700 recv=690 loss=1.43 loss_run=3 rtt_p50=40 rtt_p95=50 rtt_max=90 jitter=1.0 gaps60=0 gaps150=0 gap_max=40 reorder=0 dup=0 error="write: network is down"`
	r, ok = parseLine(line, "t-1", at, "")
	if !ok || r.Sent != 700 || r.Error != "write: network is down" {
		t.Fatalf("partial series: %v %+v", ok, r)
	}
}

func TestParseRefusesWhatIsNotASeries(t *testing.T) {
	for _, line := range []string{
		"2026/11/08 15:00:01 [diag:voice nonce=ab] ===== конец =====",
		"2026/11/08 15:00:01 [diag:stun] stun.cloudflare.com:3478 → x",
		"2026/11/08 15:00:01 [diag:voice] path=elsewhere sent=10 recv=10",
		"2026/11/08 15:00:01 [diag:voice] path=tun profile=p-1",
	} {
		if r, ok := parseLine(line, "t-1", at, ""); ok {
			t.Fatalf("read %q as %+v", line, r)
		}
	}
	// What a client writes is its own business, but a field never lands as
	// free text in the dataset.
	r, ok := parseLine(`2026/11/08 15:00:01 [diag:voice] path=tun profile=<script> proto=vless sent=10 recv=10 loss=0 loss_run=0`, "t-1", at, "")
	if !ok || r.Profile != "" {
		t.Fatalf("profile %q", r.Profile)
	}
}

func intp(v int) *int { return &v }

func TestSummarizeAppliesTheStopRule(t *testing.T) {
	rows := []Row{
		// Through a TCP profile: no loss, but three 60 ms+ silences in each
		// half-minute series — six a minute.
		{InstallID: "t-1", MeasuredAt: at, Path: "tun", Proto: "vless-reality", Network: "AS1", Sent: 1500, Recv: 1500, Gaps60: 3, Gaps150: 1, GapMax: intp(220), RTTP50: intp(50)},
		{InstallID: "t-1", MeasuredAt: at.Add(30 * time.Minute), Path: "tun", Proto: "vless-reality", Network: "AS1", Sent: 1500, Recv: 1499, Gaps60: 3, GapMax: intp(90), RTTP50: intp(60)},
		// The same network bare: clean.
		{InstallID: "t-1", MeasuredAt: at, Path: "direct", Proto: "vless-reality", Network: "AS1", Sent: 1500, Recv: 1500, GapMax: intp(25)},
		// A round that could not run.
		{InstallID: "t-2", MeasuredAt: at, Path: "tun", Proto: "hysteria2", Network: "unknown", Error: "target not resolved"},
	}
	groups := Summarize(rows)
	if len(groups) != 3 {
		t.Fatalf("groups: %+v", groups)
	}
	var tun, direct, none *Group
	for i := range groups {
		g := &groups[i]
		switch {
		case g.InstallID == "t-2":
			none = g
		case g.Path == "tun":
			tun = g
		default:
			direct = g
		}
	}
	if tun.Series != 2 || tun.Minutes != 1 || tun.Gaps60PerMin != 6 || tun.Gaps150PerMin != 1 || *tun.GapMaxMs != 220 || tun.Verdict != VerdictAudible {
		t.Fatalf("tunnel group: %+v", tun)
	}
	if tun.LossPct != 0.03 || *tun.RTTP50Ms != 50 {
		t.Fatalf("tunnel loss %v rtt %v", tun.LossPct, *tun.RTTP50Ms)
	}
	if direct.Proto != "" || direct.Verdict != VerdictOK || direct.Gaps60PerMin != 0 {
		t.Fatalf("bare network group: %+v", direct)
	}
	if none.Verdict != VerdictNoData || none.Failed != 1 {
		t.Fatalf("failed group: %+v", none)
	}
	if !groups[0].Last.Equal(at.Add(30 * time.Minute)) {
		t.Fatalf("newest group not first: %+v", groups[0])
	}
}
