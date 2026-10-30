package downdetector

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

const (
	srvA = "s:7a19192edd834db17881c0ad69c22fa9"
	srvB = "n:fra-1"
)

func install(i int) string { return fmt.Sprintf("r-%024x", i) }

func report(inst int, asn uint32, country, region string, start int64, servers ...ServerStat) Report {
	return Report{Install: install(inst), ASN: asn, Country: country, Region: region,
		Window: [2]int64{start, start + WindowSec}, Servers: servers}
}

func tunnel(id string, ok, fail int) ServerStat {
	return ServerStat{ID: id, Proto: "vless-reality", Src: SrcTunnel, OK: ok, Fail: fail, RttP50: 80}
}

func mustAdd(t *testing.T, s *Store, r Report) {
	t.Helper()
	if err := s.Add(r); err != nil {
		t.Fatal(err)
	}
}

func TestGroupsSplitByASNAndCountry(t *testing.T) {
	s := NewStore()
	w := testNow.Unix() - WindowSec
	mustAdd(t, s, report(1, 100, "ru", "moscow", w, tunnel(srvA, 0, 10)))
	mustAdd(t, s, report(2, 100, "ru", "moscow", w-WindowSec, tunnel(srvA, 1, 9)))
	mustAdd(t, s, report(3, 200, "ru", "kazan", w, tunnel(srvA, 10, 0)))
	mustAdd(t, s, report(4, 300, "de", "", w, tunnel(srvA, 10, 0)))
	mustAdd(t, s, report(5, 0, "", "", w, tunnel(srvA, 10, 0))) // unknown country: no group
	mustAdd(t, s, report(6, 100, "ru", "moscow", w,             // other src / server ignored
		ServerStat{ID: srvA, Proto: "vless-reality", Src: SrcTCP, OK: 10, RttP50: 1}, tunnel(srvB, 5, 5)))
	mustAdd(t, s, report(7, 100, "ru", "moscow", w-3*WindowSec, tunnel(srvA, 10, 0))) // older than 15 min

	same, country, abroad := s.Groups(srvA, 100, "ru", testNow)
	if same != (Group{2, 1, 19}) {
		t.Errorf("same_asn = %+v", same)
	}
	if country != (Group{1, 10, 0}) {
		t.Errorf("same_country_other_asn = %+v", country)
	}
	if abroad != (Group{1, 10, 0}) {
		t.Errorf("abroad = %+v", abroad)
	}
}

func TestRepeatedReportReplacesAndIsCapped(t *testing.T) {
	s := NewStore()
	w := testNow.Unix() - WindowSec
	mustAdd(t, s, report(1, 100, "ru", "", w, tunnel(srvA, 0, 10)))
	mustAdd(t, s, report(1, 100, "ru", "", w, tunnel(srvA, 10, 0)))
	same, _, _ := s.Groups(srvA, 100, "ru", testNow)
	if same != (Group{1, 10, 0}) {
		t.Fatalf("after replace same_asn = %+v", same)
	}
	mustAdd(t, s, report(1, 100, "ru", "", w, tunnel(srvA, 10, 0)))
	if err := s.Add(report(1, 100, "ru", "", w, tunnel(srvA, 10, 0))); err != ErrTooManySubmits {
		t.Fatalf("4th submit err = %v", err)
	}
	// A different window is a separate report.
	mustAdd(t, s, report(1, 100, "ru", "", w-WindowSec, tunnel(srvA, 10, 0)))
}

func hourOf(t *testing.T, s *Store, now time.Time) (int64, HourCells) {
	t.Helper()
	hours := s.TakeHours(now, true)
	if len(hours) != 1 {
		t.Fatalf("hours = %d", len(hours))
	}
	for h, c := range hours {
		return h, c
	}
	return 0, nil
}

func TestRollupDropsServerSeenByOneInstall(t *testing.T) {
	s := NewStore()
	w := testNow.Unix() - WindowSec
	// srvA: one install, many windows and sources — still dropped.
	mustAdd(t, s, report(1, 100, "ru", "moscow", w, tunnel(srvA, 10, 0),
		ServerStat{ID: srvA, Proto: "vless-reality", Src: SrcTCP, OK: 10}))
	mustAdd(t, s, report(1, 100, "ru", "moscow", w-WindowSec, tunnel(srvA, 10, 0)))
	// srvB: two installs.
	mustAdd(t, s, report(1, 100, "ru", "moscow", w, tunnel(srvB, 10, 0)))
	mustAdd(t, s, report(2, 100, "ru", "moscow", w, tunnel(srvB, 5, 5)))

	for _, k := range []int{1, 2} {
		s2 := NewStore()
		*s2 = Store{buckets: cloneBuckets(s)}
		hour, cells := hourOf(t, s2, testNow)
		for _, r := range Rollup(hour, cells, k) {
			if r.Server == srvA {
				t.Fatalf("k=%d: single-install server written: %+v", k, r)
			}
		}
	}
}

func TestRollupFoldsSmallASNAndRegion(t *testing.T) {
	s := NewStore()
	w := testNow.Unix() - WindowSec
	// Big provider in moscow: 3 installs. Big provider in a small region: 1.
	for i := 1; i <= 3; i++ {
		mustAdd(t, s, report(i, 100, "ru", "moscow", w, tunnel(srvA, 10, 0)))
	}
	mustAdd(t, s, report(4, 100, "ru", "tver-oblast", w, tunnel(srvA, 10, 0)))
	// Two small providers, 1 install each, in moscow.
	mustAdd(t, s, report(5, 200, "ru", "moscow", w, tunnel(srvA, 0, 10)))
	mustAdd(t, s, report(6, 300, "ru", "moscow", w, tunnel(srvA, 0, 10)))
	// A lone install abroad: folded and still below k, so dropped.
	mustAdd(t, s, report(7, 400, "de", "berlin", w, tunnel(srvA, 10, 0)))

	hour, cells := hourOf(t, s, testNow)
	rows := Rollup(hour, cells, 2)
	type key struct {
		asn     uint32
		country string
		region  string
	}
	got := map[key]RollupRow{}
	for _, r := range rows {
		if r.Installs < 2 {
			t.Fatalf("row below k written: %+v", r)
		}
		got[key{r.ASN, r.Country, r.Region}] = r
	}
	want := map[key]int{
		{100, "ru", "moscow"}: 3, // big enough on both axes
		{0, "ru", "moscow"}:   2, // asn 200 + 300 folded into 0
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for k, n := range want {
		if got[k].Installs != n {
			t.Errorf("%+v installs = %d, want %d (rows %+v)", k, got[k].Installs, n, rows)
		}
	}
	// asn 100 / tver-oblast (1 install) folds into region other, alone → dropped.
	if _, ok := got[key{100, "ru", "other"}]; ok {
		t.Error("lonely region written")
	}
}

func TestRollupRegionFoldsIntoOther(t *testing.T) {
	s := NewStore()
	w := testNow.Unix() - WindowSec
	mustAdd(t, s, report(1, 100, "ru", "tver-oblast", w, tunnel(srvA, 10, 0)))
	mustAdd(t, s, report(2, 100, "ru", "tula-oblast", w, tunnel(srvA, 8, 2)))
	hour, cells := hourOf(t, s, testNow)
	rows := Rollup(hour, cells, 2)
	if len(rows) != 1 || rows[0].Region != "other" || rows[0].ASN != 100 || rows[0].Installs != 2 ||
		rows[0].OK != 18 || rows[0].Fail != 2 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestTakeHoursWaitsForLateReports(t *testing.T) {
	s := NewStore()
	w := testNow.Unix() - WindowSec
	mustAdd(t, s, report(1, 100, "ru", "", w, tunnel(srvA, 10, 0)))
	hour := w - w%3600
	if got := s.TakeHours(time.Unix(hour+3600, 0).Add(MaxReportAge).Add(-time.Second), false); len(got) != 0 {
		t.Fatal("hour taken while reports for it may still come")
	}
	if got := s.TakeHours(time.Unix(hour+3600, 0).Add(MaxReportAge), false); len(got) != 1 {
		t.Fatal("mature hour not taken")
	}
}

func TestDBWriteHourOnceAndOverview(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "dd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	hour := int64(1761796800)
	rows := []RollupRow{
		{hour, srvA, "vless-reality", SrcTunnel, 100, "ru", "moscow", 20, 2, 80, 3},
		{hour, srvB, "vless-reality", SrcTunnel, 100, "ru", "moscow", 10, 0, 60, 2},
	}
	if err := db.WriteHour(ctx, hour, rows); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteHour(ctx, hour, rows); err != nil { // already done: no-op
		t.Fatal(err)
	}
	ov, err := db.Overview(ctx, hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov) != 1 || ov[0].Servers != 2 || ov[0].OK != 30 || ov[0].Fail != 2 || ov[0].Installs != 3 {
		t.Fatalf("overview = %+v", ov)
	}
}

func cloneBuckets(s *Store) map[int64]*bucket {
	out := map[int64]*bucket{}
	for start, b := range s.buckets {
		nb := newBucket()
		for k, c := range b.cells {
			m := map[string]contrib{}
			for i, v := range c {
				m[i] = v
			}
			nb.cells[k] = m
		}
		out[start] = nb
	}
	return out
}
