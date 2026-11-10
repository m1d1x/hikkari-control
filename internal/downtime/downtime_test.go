package downtime

import (
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
	if b.Name != "t-b" || *b.Lost[1] != 0 || *b.Lost[2] != 0 || *b.Net[2] != 4 || b.Lost[0] != nil {
		t.Fatalf("second person: %+v", b)
	}
}
