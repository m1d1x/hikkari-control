package analytics

import (
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

func TestReachFromTelemetryCountsEachConnect(t *testing.T) {
	at := time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)
	rows := ReachFromTelemetry([]telemetry.EventRow{
		{TS: at, Install: "t-a", Name: "connect", Fields: `{"ok":true,"proto":"vless-reality","secs":2}`},
		{TS: at, Install: "t-a", Name: "connect", Fields: `{"ok":false,"proto":"hysteria2","secs":1,"error":"engine.start"}`},
		{TS: at, Install: "t-b", Name: "launch", Fields: `{}`},
		{TS: at, Install: "t-b", Name: "connect", Fields: `not json`},
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	ok, bad := rows[0], rows[1]
	if ok.Outcome != "relay" || ok.RelayName != ProtoRelayPrefix+"vless-reality-vision" || ok.ConnectMs != 2000 || ok.RTTMs != 0 {
		t.Errorf("connected = %+v", ok)
	}
	if bad.Outcome != "failed" || bad.RelayName != "" {
		t.Errorf("failed = %+v", bad)
	}
	if ok.Account != bad.Account || len(ok.Account) != 32 || ok.Account == ReachAccount("t-b") {
		t.Errorf("account = %q / %q", ok.Account, bad.Account)
	}
}
