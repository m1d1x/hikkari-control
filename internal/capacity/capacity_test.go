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
