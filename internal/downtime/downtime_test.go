package downtime

import (
	"fmt"
	"testing"
	"time"
)

func TestParseLine(t *testing.T) {
	line := `2026-11-10T17:00:00Z [downtime] minutes from=1800000000 states=od-cn`
	got, ok := ParseLine(line, "t-1")
	if !ok {
		t.Fatal("line not read")
	}
	want := []struct {
		at    int64
		state byte
	}{{1800000000, 'o'}, {1800000060, 'd'}, {1800000180, 'c'}, {1800000240, 'n'}}
	if len(got) != len(want) {
		t.Fatalf("got %d minutes, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].At.Unix() != w.at || got[i].State != w.state || got[i].Install != "t-1" {
			t.Fatalf("minute %d: %+v", i, got[i])
		}
	}
}

func TestParseLineRefusesJunk(t *testing.T) {
	for _, line := range []string{
		"[downtime] minutes from=1800000000 states=oxo",
		"[downtime] minutes from=1800000001 states=o",
		"[downtime] minutes from=abc states=o",
		"[downtime] minutes from=1800000000 states=",
		"something else",
	} {
		if _, ok := ParseLine(line, "t-1"); ok {
			t.Errorf("read %q", line)
		}
	}
	if _, ok := ParseLine("[downtime] minutes from=1800000000 states=o", ""); ok {
		t.Error("read a line with no install")
	}
}

func TestChartIsPerPerson(t *testing.T) {
	since := time.Unix(1800000000/3600*3600, 0).UTC()
	until := since.Add(3 * time.Hour)
	counts := []Count{
		{Install: "t-a", Bucket: since, State: Flowing, Minutes: 50},
		{Install: "t-a", Bucket: since, State: Down, Minutes: 7},
		{Install: "t-a", Bucket: since, State: CoreDown, Minutes: 3},
		{Install: "t-b", Bucket: since.Add(time.Hour), State: Idle, Minutes: 60},
		{Install: "t-b", Bucket: since.Add(2 * time.Hour), State: NetDown, Minutes: 4},
	}
	c := assemble("24h", since, until, time.Hour, counts, func(id string) string {
		return map[string]string{"t-a": "tester-001"}[id]
	})
	if len(c.Buckets) != 3 || len(c.People) != 2 {
		t.Fatalf("buckets %d people %d", len(c.Buckets), len(c.People))
	}
	a, b := c.People[0], c.People[1]
	if a.Name != "tester-001" || a.LostTotal != 10 {
		t.Fatalf("first person %s lost %v, want tester-001 with 10", a.Name, a.LostTotal)
	}
	if *a.Lost[0] != 10 || *a.On[0] != 60 || a.Lost[1] != nil {
		t.Fatalf("tester-001 lines: lost %v on %v", a.Lost, a.On)
	}
	if c.AverageTotal == nil || *c.AverageTotal != 5 {
		t.Fatalf("window average %v, want 5 (10 lost over 2 people)", c.AverageTotal)
	}
	if *c.Average[0] != 10 || *c.Average[1] != 0 || *c.Average[2] != 0 {
		t.Fatalf("bucket averages %v %v %v, want 10 0 0 (only those on count)", *c.Average[0], *c.Average[1], *c.Average[2])
	}
	if b.Name != "t-b" || *b.Lost[1] != 0 || *b.Lost[2] != 0 || *b.Net[2] != 4 || b.Lost[0] != nil {
		t.Fatalf("second person: %+v", b)
	}
}

func TestParseRuLine(t *testing.T) {
	got, ok := ParseRuLine(`[rureach] minutes from=1800000000 ms=31,x,-,44`, "t-1")
	if !ok || len(got) != 3 {
		t.Fatalf("read %v %d minutes", ok, len(got))
	}
	if got[0].MS != 31 || got[1].MS != 0 || got[2].MS != 44 || got[2].At.Unix() != 1800000180 {
		t.Fatalf("minutes %+v", got)
	}
	for _, bad := range []string{
		"[rureach] minutes from=1800000000 ms=31,abc",
		"[rureach] minutes from=1800000000 ms=0",
		"[rureach] minutes from=1800000000 ms=",
	} {
		if _, ok := ParseRuLine(bad, "t-1"); ok {
			t.Errorf("read %q", bad)
		}
	}
}

func TestTesterChartKeepsTunnelAndNetworkApart(t *testing.T) {
	since := time.Unix(1800000000/3600*3600, 0).UTC()
	c := assembleTesters("24h", since, since.Add(2*time.Hour), time.Hour,
		[]Count{
			{Install: "t-a", Bucket: since, State: Flowing, Minutes: 55},
			{Install: "t-a", Bucket: since, State: Down, Minutes: 5},
		},
		[]RuCount{
			{Install: "t-a", Bucket: since, MedianMS: 30, Answered: 58, Missed: 2},
			{Install: "t-b", Bucket: since.Add(time.Hour), Answered: 0, Missed: 60},
		},
		[]StallCount{
			{Install: "t-a", Bucket: since, BoundMS: 300, N: 4, MS: 2100, Minutes: 7},
			{Install: "t-a", Bucket: since, BoundMS: 1000, N: 1, MS: 1200, Minutes: 7},
			{Install: "t-a", Bucket: since, BoundMS: 3000, Minutes: 7},
		},
		[]TrafficCount{
			{Install: "t-a", Bucket: since.Add(time.Hour), Tunnel: 3 << 20},
		})
	a, b := c.Testers["t-a"], c.Testers["t-b"]
	if a.TunnelMB[0] != nil || *a.TunnelMB[1] != 3 || b.TunnelMB[1] != nil {
		t.Fatalf("traffic: t-a %v, t-b %v", a.TunnelMB, b.TunnelMB)
	}
	if *a.Lost[0] != 5 || *a.RuMS[0] != 30 || *a.RuMissed[0] != 2 || a.Lost[1] != nil {
		t.Fatalf("t-a: %+v", a)
	}
	if *a.StallMS[0] != 2100 || *a.StallLongMS[0] != 1200 || a.StallMS[1] != nil {
		t.Fatalf("t-a stalls: %+v", a)
	}
	// 2.1 s of 7 measured minutes is 0.5%, not 2.1 s of the wall-clock hour.
	if *a.StallPct[0] != 0.5 || a.StallLongPct[0] == nil || *a.StallLongPct[0] > 0.29 || *a.StallLongPct[0] < 0.28 || a.StallPct[1] != nil {
		t.Fatalf("t-a stall shares: %v %v", a.StallPct, a.StallLongPct)
	}
	if b.StallMS[1] != nil || b.StallPct[1] != nil {
		t.Fatalf("t-b: no stall line is not measured, not zero: %+v", b)
	}
	if b.Lost[1] != nil || b.RuMS[1] != nil || *b.RuMissed[1] != 60 {
		t.Fatalf("t-b: a dead home network is no answer, not a latency and not downtime: %+v", b)
	}
}

func TestAStallLineIsPerMinuteAndThreshold(t *testing.T) {
	got, ok := ParseStallLine("2026-11-11 [downtime] stalls from=1800000000 bounds=300,1000,3000 v=.;2:1600,1:1200,0:0;-", "t-a")
	if !ok {
		t.Fatal("line not read")
	}
	at := func(k int64) time.Time { return time.Unix(1800000000+k*60, 0).UTC() }
	want := []StallMinute{
		{Install: "t-a", At: at(0), BoundMS: 300},
		{Install: "t-a", At: at(0), BoundMS: 1000},
		{Install: "t-a", At: at(0), BoundMS: 3000},
		{Install: "t-a", At: at(1), BoundMS: 300, N: 2, MS: 1600},
		{Install: "t-a", At: at(1), BoundMS: 1000, N: 1, MS: 1200},
		{Install: "t-a", At: at(1), BoundMS: 3000},
	}
	if len(got) != len(want) {
		t.Fatalf("want %+v, got %+v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("minute %d: want %+v, got %+v", i, want[i], got[i])
		}
	}
	for _, bad := range []string{
		"[downtime] stalls from=1800000000 bounds=300,1000 v=1:400",
		"[downtime] stalls from=1800000001 bounds=300 v=.",
		"[downtime] stalls from=1800000000 bounds=300 v=-1:5",
		"[downtime] minutes from=1800000000 states=oo",
	} {
		if _, ok := ParseStallLine(bad, "t-a"); ok {
			t.Fatalf("read a bad line %q", bad)
		}
	}
}

func TestUserChartAveragesOverThePeopleWhoseTunnelWasOn(t *testing.T) {
	since := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	step := time.Hour
	until := since.Add(3 * step)
	c := assembleUsers("7d", since, until, step, []UserCount{
		// Hour 0: two people, one lost 6 minutes; only one core timed stalls.
		{Install: "t-a", Bucket: since, On: 60, Lost: 6, StallMinutes: 58, MS300: 4000, MS1000: 1500},
		{Install: "t-b", Bucket: since, On: 30, Lost: 0},
		// Hour 2: one person, nothing lost, stalls timed and none seen.
		{Install: "t-a", Bucket: since.Add(2 * step), On: 10, StallMinutes: 9},
		// Outside the window, and a row without a minute: neither is a person.
		{Install: "t-c", Bucket: until, On: 60, Lost: 60},
		{Install: "t-d", Bucket: since.Add(step), On: 0},
	})
	line := func(v []*float64) string {
		out := ""
		for _, x := range v {
			if x == nil {
				out += " -"
			} else {
				out += fmt.Sprintf(" %g", *x)
			}
		}
		return out
	}
	if len(c.Buckets) != 3 {
		t.Fatalf("buckets = %v", c.Buckets)
	}
	for name, got := range map[string][2]string{
		"people":      {line(c.People), " 2 - 1"},
		"lost":        {line(c.Lost), " 3 - 0"},
		"stallMs":     {line(c.StallMS), " 4000 - 0"},
		"stallLongMs": {line(c.StallLongMS), " 1500 - 0"},
	} {
		if got[0] != got[1] {
			t.Errorf("%s =%s, want%s", name, got[0], got[1])
		}
	}
}

// A tester's bytes per minute through the tunnel (client newer than 1.0.93,
// owner 18.11). Client 1.0.93 wrote the bytes past the tunnel too: they are
// dropped, what went past the VPN is the tester's own business.
func TestABytesLineIsPerMinuteThroughTheTunnelOnly(t *testing.T) {
	at := func(k int64) time.Time { return time.Unix(1800000000+k*60, 0).UTC() }
	want := []TrafficMinute{
		{Install: "t-a", At: at(0), TunnelUp: 1200, TunnelDown: 56000},
		{Install: "t-a", At: at(1)},
	}
	for _, line := range []string{
		"2026-11-18 [downtime] bytes from=1800000000 v=1200,56000;.;-",
		"2026-11-18 [downtime] bytes from=1800000000 v=1200,56000,300,900;.;-",
	} {
		got, ok := ParseBytesLine(line, "t-a")
		if !ok {
			t.Fatalf("%q not read", line)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%q: got %v, want %v", line, got, want)
		}
	}
	for _, bad := range []string{
		"[downtime] bytes from=1800000001 v=1,2",
		"[downtime] bytes from=1800000000 v=1,2,3",
		"[downtime] bytes from=1800000000 v=1",
		"[downtime] bytes from=1800000000 v=1,-2",
		"[downtime] bytes from=1800000000 v=1,-2,3,4",
		"[downtime] bytes from=1800000000 v=",
	} {
		if _, ok := ParseBytesLine(bad, "t-a"); ok {
			t.Errorf("read %q", bad)
		}
	}
}

func TestAStateLineSaysModeProtocolAndProfile(t *testing.T) {
	st, ok := ParseStateLine(`19:15 [downtime] state at=1800000042 mode=proxy proto=hikkari profile="амстердам 006"`, "t-a")
	if !ok {
		t.Fatal("line not read")
	}
	want := TesterState{Install: "t-a", At: time.Unix(1800000042, 0).UTC(), Mode: "proxy", Proto: "hikkari", Profile: "амстердам 006"}
	if st != want {
		t.Fatalf("got %+v, want %+v", st, want)
	}
	for _, bad := range []string{
		`[downtime] state at=x mode=proxy proto=hikkari profile="a"`,
		`[downtime] state at=1800000042 mode=proxy profile="a"`,
		`[downtime] state at=1800000042 mode=proxy proto=hikkari profile=a`,
	} {
		if _, ok := ParseStateLine(bad, "t-a"); ok {
			t.Errorf("read %q", bad)
		}
	}
}
