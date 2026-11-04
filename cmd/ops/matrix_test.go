package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func realityOutbound() map[string]interface{} {
	return map[string]interface{}{
		"type": "vless", "tag": "p-amsterdam-1-002", "server": "146.103.121.11", "server_port": float64(8443),
		"uuid": "SECRET-UUID", "flow": "xtls-rprx-vision", "flow_retry_delay": "1s", "domain_resolver": "dns-direct",
		"tls": map[string]interface{}{"enabled": true, "server_name": "www.samsung.com",
			"utls":    map[string]interface{}{"enabled": true, "fingerprint": "chrome"},
			"reality": map[string]interface{}{"enabled": true, "public_key": "SECRET-KEY"}},
	}
}

func TestMatrixConfigSetsOnlyTheKnob(t *testing.T) {
	ob := realityOutbound()
	cfg, _, err := matrixConfig(ob, matrixKnobs["fp"], "firefox")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Inbounds  []map[string]interface{} `json:"inbounds"`
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(strings.Replace(cfg, "__PORT__", "1080", 1)), &got); err != nil {
		t.Fatalf("%v: %s", err, cfg)
	}
	out := got.Outbounds[0]
	if fp := out["tls"].(map[string]interface{})["utls"].(map[string]interface{})["fingerprint"]; fp != "firefox" {
		t.Fatalf("fp %v", fp)
	}
	for _, k := range []string{"flow_retry_delay", "domain_resolver"} {
		if _, kept := out[k]; kept {
			t.Fatalf("kept %s, which a user's config does not carry", k)
		}
	}
	if out["flow"] != "xtls-rprx-vision" || out["tag"] != "probe" || got.Inbounds[0]["listen_port"] != float64(1080) {
		t.Fatalf("%v", got)
	}
	if ob["tls"].(map[string]interface{})["utls"].(map[string]interface{})["fingerprint"] != "chrome" {
		t.Fatal("the profile's own outbound changed")
	}
}

func TestMatrixKnobsRefuseWhatTheyCannotTake(t *testing.T) {
	for knob, v := range map[string]string{"port": "0", "sni": "a b"} {
		if _, _, err := matrixConfig(realityOutbound(), matrixKnobs[knob], v); err == nil {
			t.Fatalf("%s=%q taken", knob, v)
		}
	}
	plain := map[string]interface{}{"type": "hysteria2", "server": "1.2.3.4", "server_port": float64(443)}
	if _, _, err := matrixConfig(plain, matrixKnobs["fp"], "firefox"); err == nil {
		t.Fatal("fp set on a profile with no TLS")
	}
}

func matrixTestPlan(t *testing.T) matrixPlan {
	p := matrixPlan{Profile: "amsterdam-1-002", Knob: "fp", Values: []string{"chrome", "firefox"},
		Server: "146.103.121.11", SNI: "www.samsung.com", Port: 8443, Rounds: 2, Core: "test"}
	for _, v := range p.Values {
		cfg, _, err := matrixConfig(realityOutbound(), matrixKnobs["fp"], v)
		if err != nil {
			t.Fatal(err)
		}
		p.Configs = append(p.Configs, cfg)
	}
	return p
}

func TestMatrixScriptIsValidBashAndCarriesNoSecretInClear(t *testing.T) {
	script, err := matrixScript(matrixTestPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "SECRET") {
		t.Fatal("a secret in clear in the script")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v: %s", err, out)
	}
	if strings.Count(script, "  variant ") != 2 || !strings.Contains(script, "baseline $r") {
		t.Fatalf("rounds do not go value by value:\n%s", script)
	}
}

func TestParseMatrixReadsRowsAndNamesValues(t *testing.T) {
	p := matrixTestPlan(t)
	rows := parseMatrix(`B 1762211579 1 402705 ok 50 0
R 1762211581 0 1 1048576 ok 0 50 0
R 1762211583 1 1 1048576 ok 49 0 3
R 1762211585 1 2 load_error
garbage
R 1762211587 7 1 1 ok 1 1 1
`, "spb", p)
	if len(rows) != 4 {
		t.Fatalf("%+v", rows)
	}
	if rows[0].Knob != matrixBaseline || rows[0].Burst != 50 || rows[0].Dl != 402705 || rows[0].TS != time.Unix(1762211579, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("baseline %+v", rows[0])
	}
	if rows[1].Value != "chrome" || rows[1].BurstTimeout != 50 || rows[2].Value != "firefox" || rows[2].After != 3 {
		t.Fatalf("%+v", rows[1:3])
	}
	if !rows[3].LoadError || rows[3].Round != 2 {
		t.Fatalf("%+v", rows[3])
	}
	sum := matrixSummary(rows)
	if !strings.Contains(sum, "firefox") || !strings.Contains(sum, "49/50") || !strings.Contains(sum, "0/50") {
		t.Fatalf("summary:\n%s", sum)
	}
}
