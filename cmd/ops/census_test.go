package main

import (
	"strings"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/census"
)

func TestParseProbeCounts(t *testing.T) {
	text := `{"type":"log","src":"probe nonce=a","msg":"sub12-443 net=203.0.113.0/24 flows[Ethernet]=3/12 med=40ms"}
{"type":"log","src":"probe nonce=a","msg":"control Ethernet flows=12/12 med=9ms"}`
	ms := parseProbeCounts(text, "tester:002")
	if len(ms) != 1 || ms[0].Target != "sub12-443" || ms[0].Opened != 3 || ms[0].Total != 12 || ms[0].Vantage != "tester:002" {
		t.Fatalf("%+v", ms)
	}
}

func TestSPBScriptRefusesWhatIsNotAnAddress(t *testing.T) {
	if _, err := spbScript([]census.Target{{Label: "x-443", Addr: "1.2.3.4:443; rm -rf /"}}); err == nil {
		t.Fatal("want a refusal")
	}
	if _, err := spbScript([]census.Target{{Label: "$(id)", Addr: "1.2.3.4:443"}}); err == nil {
		t.Fatal("want a refusal")
	}
	s, err := spbScript([]census.Target{{Label: "x-443", Addr: "1.2.3.4:443"}})
	if err != nil || !strings.Contains(s, "probe x-443 1.2.3.4 443") {
		t.Fatalf("%q %v", s, err)
	}
}

func TestParseSPB(t *testing.T) {
	ms := parseSPB("x-443 7\nnoise line here\n")
	if len(ms) != 1 || ms[0].Opened != 7 || ms[0].Vantage != censusVantage {
		t.Fatalf("%+v", ms)
	}
}

func TestParseProtoCounts(t *testing.T) {
	ms := parseProtoCounts(`{"msg":"sub3-443 proto=2/3 med=210ms"}`, "tester:002")
	if len(ms) != 1 || ms[0].Target != "sub3-443" || ms[0].Opened != 2 || ms[0].Kind != "proto" {
		t.Fatalf("%+v", ms)
	}
}
