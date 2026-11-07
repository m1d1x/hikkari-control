package capacity

import (
	"strings"
	"testing"
	"time"
)

func TestKeyIgnoresCredentialsAndLabelsButNotTransport(t *testing.T) {
	a := "vless://alice@EXAMPLE.test:443?security=reality&fp=chrome&hikkari-node=n:one#Name"
	b := "vless://bob@example.test:443?fp=chrome&security=reality#Other"
	if Key(a) != Key(b) {
		t.Fatal("credential or presentation changed identity")
	}
	if Key(a) == Key(strings.Replace(b, "chrome", "firefox", 1)) {
		t.Fatal("different transport settings reused capacity")
	}
}
func TestSampleRejectsInventedSpeedAndClock(t *testing.T) {
	valid := Sample{Key: strings.Repeat("a", 64), MeasuredAt: time.Now().Unix(), Bytes: 1000000, DurationMS: 1000, Mbps: 8}
	if err := valid.Validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	valid.Mbps = 1000
	if err := valid.Validate(time.Now()); err == nil {
		t.Fatal("invented speed accepted")
	}
}

func TestAggregateKeepsLatencyIndependentOfDownloadFailure(t *testing.T) {
	now := time.Now().Unix()
	samples := []Sample{{MeasuredAt: now, RTTMS: 120, Mbps: 100}, {MeasuredAt: now - 10, RTTMS: 80, Error: "control_unavailable"}, {MeasuredAt: now, RTTMS: 140, Mbps: 200, Limited: true}}
	got := aggregate(strings.Repeat("a", 64), samples)
	if got.RTTMS != 120 || got.Mbps != 100 || got.Points != 3 || !got.Limited || got.ExpiresAt != now-10+int64(FreshFor.Seconds()) {
		t.Fatalf("bad aggregate: %+v", got)
	}
	got = aggregate("key", []Sample{{MeasuredAt: now, Error: "unreachable"}})
	if got.RTTMS != 0 || got.Mbps != 0 {
		t.Fatal("failed latest report retained success")
	}
}
func TestLegacyHeaderNeverIncludesHistory(t *testing.T) {
	raw := Encode([]Metric{{Key: "key", History: []Point{{Ts: 123, Mbps: 100}}}})
	if strings.Contains(raw, "history") {
		t.Fatal("history leaked into response header")
	}
}
