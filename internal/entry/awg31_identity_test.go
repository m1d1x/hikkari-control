package entry

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// A 3.1 relay is minted as the 06.11 stand ran: its own header protection key,
// paddings long enough for the nonce, single-value headers (amneziawg-go#186),
// and every 3.1 knob set.
func TestNewAwg31IdentityObeysTheProtocol(t *testing.T) {
	keys := map[string]bool{}
	for i := 0; i < 200; i++ {
		id, err := newAwgIdentity(31000, true)
		if err != nil {
			t.Fatal(err)
		}
		a := id.Awg
		v := a.AwgV3
		if !v.IsSet() || !v.RandomTrailers || !v.DisableCookies || len(v.Ranges()) != 6 {
			t.Fatalf("%+v", v)
		}
		if raw, err := base64.StdEncoding.DecodeString(v.HeaderProtectionKey); err != nil || len(raw) != 32 || keys[v.HeaderProtectionKey] {
			t.Fatalf("header protection key %q", v.HeaderProtectionKey)
		}
		keys[v.HeaderProtectionKey] = true
		for _, s := range []int{a.S1, a.S2, a.S3, a.S4} {
			if s < 16 {
				t.Fatalf("s %d %d %d %d", a.S1, a.S2, a.S3, a.S4)
			}
		}
		if a.S1+148 == a.S2+92 || a.S4 > 32 {
			t.Fatalf("s1=%d s2=%d s4=%d", a.S1, a.S2, a.S4)
		}
		if a.H1 != "1" || a.H2 != "2" || a.H3 != "3" || a.H4 != "4" {
			t.Fatalf("headers %s %s %s %s", a.H1, a.H2, a.H3, a.H4)
		}
	}
}

// The signed record and the env carry every 3.1 field; a 2.0 relay's carry
// none, so its record stays byte for byte what released clients read.
func TestAwg31ReachesTheRecordAndTheEdge(t *testing.T) {
	id, err := newAwgIdentity(31000, true)
	if err != nil {
		t.Fatal(err)
	}
	rec := id.record("x1-1", "203.0.113.9", "", 1, 2)
	if err := rec.check(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back RelayRecord
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.AwgParams.AwgV3 != id.Awg.AwgV3 {
		t.Fatalf("record: %+v, want %+v", back.AwgParams.AwgV3, id.Awg.AwgV3)
	}
	env, err := id.EdgeEnv()
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"EDGE_AWG_HEADER_PROTECTION_KEY":    id.Awg.HeaderProtectionKey,
		"EDGE_AWG_CONTENT_PADDING_ADDITION": "10-50",
		"EDGE_AWG_REKEY_AFTER_TIME":         "100-120",
		"EDGE_AWG_REKEY_TIMEOUT":            "5-7",
		"EDGE_AWG_REJECT_AFTER_TIME":        "170-190",
		"EDGE_AWG_KEEPALIVE_TIMEOUT":        "10-15",
		"EDGE_AWG_MAX_HANDSHAKE_ATTEMPTS":   "15-20",
		"EDGE_AWG_RANDOM_TRAILERS":          "1",
		"EDGE_AWG_DISABLE_COOKIES":          "1",
		"EDGE_AWG_H1":                       "1",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	for k, v := range env {
		if strings.ContainsAny(v, " \t\n\"'$`\\") {
			t.Errorf("%s=%q does not survive the swap script", k, v)
		}
	}

	old, err := newAwgIdentity(31000, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(old.record("x1-1", "203.0.113.9", "", 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"header_protection_key", "content_padding_addition", "rekey_after_time", "rekey_timeout", "reject_after_time", "keepalive_timeout", "max_handshake_attempts", "random_trailers", "disable_cookies"} {
		if strings.Contains(string(raw), k) {
			t.Errorf("a 2.0 record carries %s", k)
		}
	}
	oldEnv, _ := old.EdgeEnv()
	if _, ok := oldEnv["EDGE_AWG_HEADER_PROTECTION_KEY"]; ok || len(oldEnv) != len(env)-9 {
		t.Errorf("a 2.0 env: %v", oldEnv)
	}
}

// What amneziawg-go would refuse, or lose traffic over, is not signed.
func TestAwg31RecordCheckRefusesWhatTheDeviceWouldNot(t *testing.T) {
	for name, spoil := range map[string]func(p *AwgParams){
		"short key":             func(p *AwgParams) { p.HeaderProtectionKey = "c2hvcnQ=" },
		"short padding":         func(p *AwgParams) { p.S3 = 8 },
		"reversed range":        func(p *AwgParams) { p.RekeyAfterTime = "120-100" },
		"not a range":           func(p *AwgParams) { p.ContentPaddingAddition = "lots" },
		"trailers over a range": func(p *AwgParams) { p.H2 = "100-200" },
	} {
		id, err := newAwgIdentity(31000, true)
		if err != nil {
			t.Fatal(err)
		}
		rec := id.record("x1-1", "203.0.113.9", "", 1, 2)
		spoil(rec.AwgParams)
		if err := rec.check(); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
}

func TestAwgGenerationFromEnv(t *testing.T) {
	for v, want := range map[string]bool{"": false, "2.0": false, "3.1": true, " 3.1 ": true} {
		if got, err := awgGenerationFromEnv(v); err != nil || got != want {
			t.Errorf("%q: %v %v", v, got, err)
		}
	}
	for _, v := range []string{"3", "31", "v3.1", "2"} {
		if _, err := awgGenerationFromEnv(v); err == nil || !strings.Contains(err.Error(), awgGenerationEnv) {
			t.Errorf("%q: %v", v, err)
		}
	}
}
