package hikkariobs

import "testing"

func TestChartLines(t *testing.T) {
	tot := totals{
		"client||dup.fresh":         {100: 200},
		"client||dup.only_stream":   {100: 10},
		"relay|trunk|tcp.retrans":   {100: 3},
		"relay|trunk|tcp.segs_out":  {100: 1000},
		"relay|direct|tcp.segs_out": {100: 0},
	}
	if v, ok := ratio(SideClient, none, "dup.only_stream", "dup.fresh", 100)(tot, 100); !ok || v != 5 {
		t.Fatalf("saved share %v %v", v, ok)
	}
	if v, ok := ratio(SideRelay, trunk, "tcp.retrans", "tcp.segs_out", 1000)(tot, 100); !ok || v != 3 {
		t.Fatalf("trunk retrans %v %v", v, ok)
	}
	if _, ok := ratio(SideRelay, direct, "tcp.retrans", "tcp.segs_out", 1000)(tot, 100); ok {
		t.Fatal("a share of nothing sent")
	}
	if _, ok := metric(SideClient, none, "calls.n")(tot, 100); ok {
		t.Fatal("an absent metric read")
	}
	keys := map[string]bool{}
	for _, s := range lineSpecs {
		if keys[s.key] {
			t.Fatalf("line %s twice", s.key)
		}
		keys[s.key] = true
	}
}
