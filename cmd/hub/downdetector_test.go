package main

import (
	"testing"
	"time"

	dd "github.com/m1d1x/hikkari-control/internal/downdetector"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// A report under the client's id for a node's link shows up against that
// node's slug, and the totals count the install once.
func TestIntakeLineShowsReportsOnOurNodes(t *testing.T) {
	now := time.Now().UTC()
	window := now.Truncate(dd.WindowSec*time.Second).Add(-dd.WindowSec * time.Second).Unix()
	node := entry.FleetNode{Slug: "ams-1", Host: "1.2.3.4", Port: 8443, Proto: entry.ProtoVlessRealityVision}
	quiet := entry.FleetNode{Slug: "ams-2", Host: "1.2.3.4", Port: 9443, Proto: entry.ProtoVlessRealityVision}
	store := dd.NewStore()
	if got := intakeLine(store, []entry.FleetNode{node, quiet}, now); got != "installs=0 servers=0 ams-1=0/0/0 ams-2=0/0/0" {
		t.Fatalf("empty store: %s", got)
	}
	err := store.Add(dd.Report{Install: "r-a", Country: "RU", ASN: 1, Window: [2]int64{window, window + dd.WindowSec},
		Servers: []dd.ServerStat{{ID: dd.ServerID("1.2.3.4", 8443, "vless-reality"), Proto: "vless-reality", Src: dd.SrcTunnel, OK: 2, Fail: 8}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := intakeLine(store, []entry.FleetNode{node, quiet}, now); got != "installs=1 servers=1 ams-1=1/2/8 ams-2=0/0/0" {
		t.Fatalf("one report: %s", got)
	}
}
