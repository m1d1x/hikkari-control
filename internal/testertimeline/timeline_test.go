package testertimeline

import (
	"strings"
	"testing"
	"time"
)

// The evening of 17.11 as dima-002's log and the entry's rows had it: the
// tester's clock is three hours ahead, a stall of 6.4 s in the minute the
// tester switched profile, an old generation of the entry that cut two flows
// minutes before, and a hikkari period with the client's pauses.
func TestTheEveningOf1711LinesUpWithOurEvents(t *testing.T) {
	recv := time.Date(2026, 11, 17, 20, 59, 10, 0, time.UTC)
	log := []string{
		"2026/11/17 23:59:09.193 [downtime] minutes from=1794948480 states=oooooooodoo",
		"2026/11/17 23:59:09.193 [downtime] stalls from=1794948480 bounds=300,1000,3000 v=.;.;.;.;.;.;.;.;2:1236,0:0,0:0;4:8172,2:6376,1:3453;5:4159,1:1607,0:0",
		"2026/11/17 23:57:17.000 [connect] switch profile=\"нидерланды-002\" proto=hikkari secs=0.0 ok=true err=<nil>",
		"2026/11/17 23:57:19.000 [route] auto moved \"102\" -> \"002\"",
		"2026/11/17 23:55:41.078 [hikkari] 2026/11/17 23:55:41.078590 [hikkari] +0300 2026-11-17 23:55:41 WARN outbound/hikkari[p-102]: hikkari period s=599 {\"dup\":{\"fresh\":3},\"pauses\":{\"out\":{\"voice\":{\"packets\":100,\"over60\":2,\"over150\":1,\"stream\":0},\"video\":{\"packets\":4000,\"over60\":7,\"over150\":3,\"stream\":2}}}}",
		"2026/11/17 23:56:20.000 [diag:voice] path=tun profile=p-102 proto=hikkari isp=AS8402 isp_at=card mobile=0 target=cloudflare sent=1500 recv=1462 loss=2.53 loss_run=2 rtt_p50=67 rtt_p95=138 rtt_max=267 jitter=2.5 gaps60=1 gaps150=0 gap_max=62 reorder=0 dup=0",
		"2026/11/17 23:58:00.000  health: ok -> connecting (ip=\"\", нет ответа)",
	}
	var chunk []Line
	for _, l := range log {
		chunk = append(chunk, Line{Text: l, ReceivedAt: recv})
	}
	offset := Offset([][]Line{chunk})
	if offset != 3*time.Hour {
		t.Fatalf("offset %v, want 3h", offset)
	}
	from := time.Date(2026, 11, 17, 20, 30, 0, 0, time.UTC)
	b := NewBuilder(from, from.Add(time.Hour))
	for _, l := range chunk {
		b.AddTesterLine(l.Text, offset)
	}
	deaths := map[time.Time]int{}
	var ours []Our
	for _, row := range []struct {
		ts     time.Time
		layer  string
		target string
		data   map[string]any
	}{
		{time.Date(2026, 11, 17, 20, 40, 52, 0, time.UTC), "event", "", map[string]any{"kind": "entry_generation_left", "gen": 8.0, "cut_flows": 2.0, "cut_streams": 1.0}},
		{time.Date(2026, 11, 17, 20, 55, 30, 0, time.UTC), "event", "", map[string]any{"kind": "receivers"}},
		{time.Date(2026, 11, 17, 20, 56, 10, 0, time.UTC), "event", "", map[string]any{"kind": "member_down", "member": 3.0}},
		{time.Date(2026, 11, 17, 20, 56, 11, 0, time.UTC), "event", "", map[string]any{"kind": "member_down", "member": 4.0}},
		{time.Date(2026, 11, 17, 20, 56, 0, 0, time.UTC), "event", "", map[string]any{"kind": "route", "path": "pipe", "class": "video"}},
		{time.Date(2026, 11, 17, 20, 58, 0, 0, time.UTC), "call", "copies1", map[string]any{"gaps150": 1.0, "gap_max_ms": 355.0}},
		{time.Date(2026, 11, 17, 20, 58, 0, 0, time.UTC), "call", "pipe-copies1", map[string]any{"gaps150": 1.0, "gap_max_ms": 900.0}},
	} {
		if o, ok := OursFromTrunk("aezamsk1", row.ts, row.layer, row.target, row.data, deaths); ok {
			ours = append(ours, o)
		}
	}
	ours = append(ours, DeathsAsOurs("aezamsk1", deaths)...)
	b.AddOurs(ours)
	got := map[string]Minute{}
	for _, m := range b.Minutes(ours) {
		got[m.At.Format("15:04")] = m
	}

	if m := got["20:40"]; len(m.Ours) != 1 || !strings.Contains(m.Ours[0], "entry_generation_left cut_flows=2 cut_streams=1 gen=8") {
		t.Fatalf("20:40: %+v", m)
	}
	if m := got["20:55"]; m.Tester == nil || len(m.Tester.Pauses) != 1 || m.Tester.Pauses[0] != "p-102 voice packets=100 over60=2 over150=1 stream=0 video packets=4000 over60=7 over150=3 stream=2" || len(m.Ours) != 0 {
		t.Fatalf("20:55: %+v (the heartbeat must stay out)", m)
	}
	if m := got["20:56"]; m.Tester == nil || len(m.Tester.Voice) != 1 || !strings.Contains(m.Tester.Voice[0], "gaps150=0 gap_max=62") || m.Tester.State != "d" || len(m.Ours) != 1 || m.Ours[0] != "aezamsk1 member_down x2" {
		t.Fatalf("20:56: %+v (the stand's route must stay out)", m)
	}
	m := got["20:57"]
	if m.Tester == nil || m.Tester.Stalls1s != 2 || m.Tester.Stalls3s != 1 || m.Tester.StallMS != 6376 || len(m.Tester.Events) != 2 {
		t.Fatalf("20:57 tester: %+v", m.Tester)
	}
	if len(m.Near) != 2 || !strings.Contains(m.Near[0]+m.Near[1], "member_down x2") || !strings.Contains(m.Near[0]+m.Near[1], "trunk call red: gaps150=1 gap_max=355") {
		t.Fatalf("20:57 near: %q", m.Near)
	}
	if m := got["20:58"]; m.Tester == nil || len(m.Tester.Events) != 1 || !strings.HasPrefix(m.Tester.Events[0], "health ok -> connecting") || len(m.Ours) != 1 {
		t.Fatalf("20:58: %+v (the stand's call must stay out)", m)
	}
}

func TestTheWindowIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 18, 6, 0, 0, 0, time.UTC)
	if from, to, err := window("", "", "", now); err != nil || to.Sub(from) != 6*time.Hour {
		t.Fatalf("default: %v %v %v", from, to, err)
	}
	if from, _, err := window("2d", "", "", now); err != nil || !from.Equal(now.Add(-48*time.Hour)) {
		t.Fatalf("2d: %v %v", from, err)
	}
	if _, _, err := window("3d", "", "", now); err == nil {
		t.Fatal("3d passed")
	}
	if from, to, err := window("", "2026-11-17T20:30:00Z", "2026-11-17T21:30:00Z", now); err != nil || to.Sub(from) != time.Hour {
		t.Fatalf("from-to: %v %v %v", from, to, err)
	}
}
