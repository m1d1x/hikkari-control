package support

import "testing"

func TestValidateProbeTargets(t *testing.T) {
	good := []ProbeTarget{{"ams7-443", "89.124.99.31:443"}}
	if err := validateProbeTargets(TesterCommandProbe, good, MaxProbeTargets); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string][]ProbeTarget{
		"empty":    nil,
		"private":  {{"a", "192.168.1.1:443"}},
		"loopback": {{"a", "127.0.0.1:443"}},
		"cgnat":    {{"a", "100.64.0.1:443"}},
		"fakeip":   {{"a", "198.18.0.1:443"}},
		"hostname": {{"a", "example.org:443"}},
		"ipv6":     {{"a", "[2001:db8::1]:443"}},
		"no port":  {{"a", "1.2.3.4"}},
		"zero":     {{"a", "1.2.3.4:0"}},
		"label":    {{"A B", "1.2.3.4:443"}},
		"too many": make([]ProbeTarget, MaxProbeTargets+1),
	} {
		if err := validateProbeTargets(TesterCommandProbe, bad, MaxProbeTargets); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestProvokeCarriesAtMostThreeTargets(t *testing.T) {
	three := []ProbeTarget{{"ru", "1.2.3.4:443"}, {"ams3", "1.2.3.5:443"}, {"eu", "1.2.3.6:443"}}
	p, err := commandParams(TesterCommandProvoke, three)
	if err != nil || len(p) == 0 {
		t.Fatalf("three boxes: %s, %v", p, err)
	}
	if _, err := commandParams(TesterCommandProvoke, append(three, ProbeTarget{"x", "1.2.3.7:443"})); err == nil {
		t.Fatal("four boxes accepted")
	}
	if _, err := commandParams(TesterCommandProvoke, nil); err == nil {
		t.Fatal("no boxes accepted")
	}
	if _, err := commandParams(TesterCommandProvoke, []ProbeTarget{{"ru", "10.0.0.1:443"}}); err == nil {
		t.Fatal("a private box accepted")
	}
	if !testerCommandKinds[TesterCommandProvoke] {
		t.Fatal("provoke_targets is not on the whitelist")
	}
}

func TestOnlyProbeCarriesTargets(t *testing.T) {
	if _, err := commandParams(TesterCommandRunDoctor, []ProbeTarget{{"a", "1.2.3.4:443"}}); err == nil {
		t.Fatal("run_doctor took targets")
	}
	p, err := commandParams(TesterCommandSnapshot, nil)
	if err != nil || p != nil {
		t.Fatalf("snapshot params: %s, %v", p, err)
	}
}
