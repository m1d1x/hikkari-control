package main

// The matrix stand (hikkari-client/plans/2026-11-04-relay-search-method.md):
// one profile of a subscription, one knob of its config, a few values of that
// knob, measured from vantages in and outside Russia on the core users run, the
// way [diag:load] measures: a 1 MB download, 50 requests at once, 3 after.
//
// Every round also measures the same address and port with no protocol, so a
// row can tell a burned knob from a cut address. Rounds go variant by variant
// within a round, not variant after variant, so a path that worsens over the
// run does not pass for a worse value.
//
// The profile's outbound holds its secrets. It goes to our own vantages on SSH
// stdin, into a private temporary directory, and never into a row: a row names
// the profile by its label and the knob by its value.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/m1d1x/hikkari-control/internal/relayruns"
)

const (
	matrixBytes    = 1 << 20
	matrixBurst    = 50
	matrixMaxBurst = 200
	// matrixUploadPart is one part of the core's upload: small enough to pass
	// a link that cuts long flows.
	matrixUploadPart = 4 << 20
	// matrixLoadBurst is the load knob: the burst's size, not the config's.
	matrixLoadBurst = "burst"
	matrixAfter     = 3
	matrixRounds    = 3
	matrixMaxRounds = 10
	matrixMaxValues = 8
	// matrixCoreDir is where a vantage keeps the core between runs.
	matrixCoreDir = "/tmp/hikkari-matrix"
	// matrixBaseline is the knob of the rows with no protocol.
	matrixBaseline = "baseline"
)

// matrixVantages are the vantages by name: one in Russia, one outside as the
// control that tells the censor from the server or the config.
var matrixVantages = map[string]string{"spb": "nekko-spb", "lv": "hikkari-main"}

var (
	matrixHost  = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)
	matrixPath  = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,200}$`)
	matrixValue = regexp.MustCompile(`^[A-Za-z0-9._,/-]{0,64}$`)
)

// matrixKnobs set one knob on a sing-box outbound. A value the knob cannot take
// is an error, not a silently different experiment.
var matrixKnobs = map[string]func(ob map[string]interface{}, v string) error{
	"fp": func(ob map[string]interface{}, v string) error {
		tls, err := outboundTLS(ob)
		if err != nil {
			return err
		}
		tls["utls"] = map[string]interface{}{"enabled": true, "fingerprint": v}
		return nil
	},
	"sni": func(ob map[string]interface{}, v string) error {
		if !matrixHost.MatchString(v) {
			return fmt.Errorf("sni %q is not a host name", v)
		}
		tls, err := outboundTLS(ob)
		if err != nil {
			return err
		}
		tls["server_name"] = v
		return nil
	},
	"port": func(ob map[string]interface{}, v string) error {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return fmt.Errorf("port %q", v)
		}
		ob["server_port"] = p
		return nil
	},
	"flow": func(ob map[string]interface{}, v string) error {
		if v == "" || v == "none" {
			delete(ob, "flow")
			return nil
		}
		ob["flow"] = v
		return nil
	},
	"alpn": func(ob map[string]interface{}, v string) error {
		tls, err := outboundTLS(ob)
		if err != nil {
			return err
		}
		// Values are split by "/", so http/1.1 is written http1.1.
		var list []interface{}
		for _, p := range strings.Split(v, "/") {
			switch p {
			case "h2", "h3":
				list = append(list, p)
			case "http1.1":
				list = append(list, "http/1.1")
			default:
				return fmt.Errorf("alpn %q: h2, h3 or http1.1, joined by /", p)
			}
		}
		tls["alpn"] = list
		return nil
	},
}

func outboundTLS(ob map[string]interface{}) (map[string]interface{}, error) {
	tls, ok := ob["tls"].(map[string]interface{})
	if !ok {
		return nil, errors.New("the profile has no TLS")
	}
	return tls, nil
}

// matrixRow is one round of one variant from one vantage: a line of the dataset.
type matrixRow struct {
	TS           string `json:"ts"`
	Vantage      string `json:"vantage"`
	Profile      string `json:"profile"`
	Core         string `json:"core"`
	Knob         string `json:"knob"`
	Value        string `json:"value"`
	Round        int    `json:"round"`
	Dl           int64  `json:"dl"`
	DlEnd        string `json:"dl_end"`
	Burst        int    `json:"burst"`
	BurstTotal   int    `json:"burst_total"`
	BurstTimeout int    `json:"burst_timeout"`
	After        int    `json:"after"`
	LoadError    bool   `json:"load_error,omitempty"`
	// Params are every knob the round ran with, the varied one included; none
	// on a baseline row, which runs no protocol.
	Params map[string]string `json:"params,omitempty"`
}

// matrixPlan is what one run measures.
type matrixPlan struct {
	Profile string
	Knob    string
	Values  []string
	Configs []string            // whole core configs, one per value, listen port left as __PORT__
	Params  []map[string]string // the knobs each config runs with, one per value
	Bursts  []int               // how many requests each value's burst opens at once
	// Base is the knobs set on every value before the varied one, so a knob
	// is tried on a base that shows a difference (alpn on fp=chrome).
	Base   string
	Server string
	SNI    string
	Port   int
	// BasePath is fetched with no protocol: a page of the cover site big
	// enough to pass a cut by volume, "/" when none is named.
	BasePath string
	Rounds   int
	Core     string
}

// matrix runs the stand and prints a summary, then the rows as NDJSON.
func (c *client) matrix(args map[string]string) (string, error) {
	plan, err := matrixPlanOf(args)
	if err != nil {
		return "", err
	}
	vantages, err := matrixVantageList(args["vantage"])
	if err != nil {
		return "", err
	}
	core, coreLabel, err := matrixCore()
	if err != nil {
		return "", err
	}
	plan.Core = coreLabel
	script, err := matrixScript(plan)
	if err != nil {
		return "", err
	}
	var (
		mu   sync.Mutex
		rows []matrixRow
		errs []string
		wg   sync.WaitGroup
	)
	for _, v := range vantages {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			got, err := matrixRun(name, matrixVantages[name], core, script, plan)
			mu.Lock()
			defer mu.Unlock()
			rows = append(rows, got...)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			}
		}(v)
	}
	wg.Wait()
	out := matrixSummary(rows)
	if plan.Base != "" {
		out = "база: " + plan.Base + "\n" + out
	}
	if len(rows) > 0 {
		// Every run is history: the rows go to the hub's dataset, so the next
		// question about this knob starts from them, not from scrollback.
		// A dataset the hub refused does not undo the run: its rows are below
		// and can be sent again; the failure is said, not raised.
		if n, err := c.recordMatrix(rows); err != nil {
			out += "\nдатасет на hub: не записан: " + err.Error()
		} else {
			out += fmt.Sprintf("\nдатасет на hub: %d новых строк из %d", n, len(rows))
		}
	}
	for _, r := range rows {
		line, _ := json.Marshal(r)
		out += "\n" + string(line)
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return out, errors.New(strings.Join(errs, "; "))
	}
	return out, nil
}

// recordMatrix sends the run's rows to the hub's relay dataset.
func (c *client) recordMatrix(rows []matrixRow) (int, error) {
	out, err := matrixDatasetRows(rows)
	if err != nil {
		return 0, err
	}
	data, err := c.request(http.MethodPost, "/api/v1/ops/relay-runs", nil, out)
	if err != nil {
		return 0, err
	}
	var ans struct {
		Added int `json:"added"`
	}
	if err := json.Unmarshal(data, &ans); err != nil {
		return 0, errors.New("unreadable answer")
	}
	return ans.Added, nil
}

// runsRecord sends the rows of a saved matrix output (--file) to the dataset:
// for a run whose rows the hub refused at the time.
func (c *client) runsRecord(args map[string]string) (string, error) {
	data, err := os.ReadFile(args["file"])
	if err != nil {
		return "", err
	}
	var rows []matrixRow
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r matrixRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return "", fmt.Errorf("row %q: %w", line, err)
		}
		if r.BurstTotal == 0 && !r.LoadError {
			r.BurstTotal = matrixBurst // rows printed before burst_total was written
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return "", errors.New("no rows in the file")
	}
	n, err := c.recordMatrix(rows)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("датасет на hub: %d новых строк из %d", n, len(rows)), nil
}

// matrixDatasetRows are the run's rows as the dataset keeps them.
func matrixDatasetRows(rows []matrixRow) ([]relayruns.Row, error) {
	out := make([]relayruns.Row, 0, len(rows))
	for _, r := range rows {
		at, err := time.Parse(time.RFC3339, r.TS)
		if err != nil {
			return nil, fmt.Errorf("row time %q", r.TS)
		}
		d := relayruns.Row{
			Source: relayruns.SourceStand, MeasuredAt: at, Vantage: r.Vantage, Core: r.Core, Profile: r.Profile,
			Knob: r.Knob, Value: r.Value, Params: r.Params, Round: r.Round, Dl: r.Dl, DlEnd: r.DlEnd,
			Burst: r.Burst, BurstTotal: r.BurstTotal, BurstTimeout: r.BurstTimeout, LoadError: r.LoadError,
		}
		if r.Knob != matrixBaseline {
			d.After, d.AfterTotal = r.After, matrixAfter
		}
		if r.LoadError {
			d.BurstTotal = 0
		}
		out = append(out, d)
	}
	return out, nil
}

// matrixPlanOf reads the arguments and the profile. The subscription is parsed
// by the client's own parser (census-parse), as census add does.
func matrixPlanOf(args map[string]string) (matrixPlan, error) {
	sub, label, knob := args["sub"], args["profile"], args["knob"]
	if sub == "" || label == "" || knob == "" {
		return matrixPlan{}, errors.New("matrix needs --sub, --profile and --knob name=v1,v2")
	}
	name, list, ok := strings.Cut(knob, "=")
	set, known := matrixKnobs[name]
	if name == matrixLoadBurst {
		// Not a knob of the config: how many requests the burst opens at once,
		// on the profile as it is. It finds the count a cut starts at.
		set, known = func(map[string]interface{}, string) error { return nil }, true
	}
	if !ok || !known {
		return matrixPlan{}, fmt.Errorf("knob %q: want one of %s as name=v1,v2", knob, strings.Join(matrixKnobNames(), ", "))
	}
	values := strings.Split(list, ",")
	if len(values) == 0 || len(values) > matrixMaxValues {
		return matrixPlan{}, fmt.Errorf("want 1 to %d values", matrixMaxValues)
	}
	for _, v := range values {
		if !matrixValue.MatchString(v) {
			return matrixPlan{}, fmt.Errorf("value %q", v)
		}
	}
	rounds := matrixRounds
	if v := args["rounds"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > matrixMaxRounds {
			return matrixPlan{}, fmt.Errorf("rounds: 1 to %d", matrixMaxRounds)
		}
		rounds = n
	}
	outbound, err := matrixOutbound(sub, label)
	if err != nil {
		return matrixPlan{}, err
	}
	base, err := matrixApplyBase(outbound, args["base"], name)
	if err != nil {
		return matrixPlan{}, err
	}
	plan := matrixPlan{Profile: label, Knob: name, Values: values, Rounds: rounds, BasePath: "/", Base: base}
	if v := args["baseline_path"]; v != "" {
		if !matrixPath.MatchString(v) {
			return matrixPlan{}, fmt.Errorf("baseline_path %q", v)
		}
		plan.BasePath = v
	}
	plan.Server, _ = outbound["server"].(string)
	plan.Port = intOf(outbound["server_port"])
	if tls, ok := outbound["tls"].(map[string]interface{}); ok {
		plan.SNI, _ = tls["server_name"].(string)
	}
	if !matrixHost.MatchString(plan.Server) || plan.Port <= 0 {
		return matrixPlan{}, errors.New("the profile has no server address")
	}
	if plan.SNI != "" && !matrixHost.MatchString(plan.SNI) {
		return matrixPlan{}, errors.New("the profile's server name is not a host name")
	}
	for _, v := range values {
		cfg, params, err := matrixConfig(outbound, set, v)
		if err != nil {
			return matrixPlan{}, fmt.Errorf("%s=%s: %w", name, v, err)
		}
		plan.Configs = append(plan.Configs, cfg)
		plan.Params = append(plan.Params, params)
		n := matrixBurst
		if name == matrixLoadBurst {
			if n, err = strconv.Atoi(v); err != nil || n < 1 || n > matrixMaxBurst {
				return matrixPlan{}, fmt.Errorf("burst %q: 1 to %d", v, matrixMaxBurst)
			}
		}
		plan.Bursts = append(plan.Bursts, n)
	}
	return plan, nil
}

// matrixApplyBase sets the --base knobs, "fp=chrome;alpn=h2", on the outbound
// every value starts from. The varied knob cannot be among them, nor can the
// burst, which is not a knob of the config. It returns the base as given, for
// the summary.
func matrixApplyBase(ob map[string]interface{}, base, varied string) (string, error) {
	if base == "" {
		return "", nil
	}
	var names []string
	for _, pair := range strings.Split(base, ";") {
		k, v, ok := strings.Cut(pair, "=")
		set, known := matrixKnobs[k]
		if !ok || !known {
			return "", fmt.Errorf("base %q: want knob=value;knob=value", pair)
		}
		if k == varied {
			return "", fmt.Errorf("base sets %s, the knob this run varies", k)
		}
		if !matrixValue.MatchString(v) {
			return "", fmt.Errorf("base value %q", v)
		}
		if err := set(ob, v); err != nil {
			return "", fmt.Errorf("base %s=%s: %w", k, v, err)
		}
		names = append(names, k+"="+v)
	}
	return strings.Join(names, ";"), nil
}

func matrixKnobNames() []string {
	out := make([]string, 0, len(matrixKnobs)+1)
	for k := range matrixKnobs {
		out = append(out, k)
	}
	out = append(out, matrixLoadBurst)
	sort.Strings(out)
	return out
}

func intOf(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// matrixOutbound is the profile's sing-box outbound as the client builds it.
func matrixOutbound(sub, label string) (map[string]interface{}, error) {
	dir, err := clientEngineDir()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/census-parse", "--with-config")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(sub + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("census-parse: %s", strings.ReplaceAll(strings.TrimSpace(stderr.String()), sub, "[subscription]"))
	}
	var entries []struct {
		Label  string                 `json:"label"`
		Config map[string]interface{} `json:"config"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		return nil, errors.New("census-parse: unreadable answer")
	}
	var labels []string
	for _, e := range entries {
		if e.Label == label && e.Config != nil {
			return e.Config, nil
		}
		labels = append(labels, e.Label)
	}
	return nil, fmt.Errorf("no profile %q in the subscription, it has: %s", label, strings.Join(labels, ", "))
}

// matrixConfig is the whole core config of one value: the profile's outbound
// with the knob set, behind a local mixed inbound. Fields that name the
// client's own config or are set for testers only are dropped, so every value
// runs what a user runs but for the knob.
func matrixConfig(outbound map[string]interface{}, set func(map[string]interface{}, string) error, value string) (string, map[string]string, error) {
	raw, err := json.Marshal(outbound)
	if err != nil {
		return "", nil, err
	}
	var ob map[string]interface{}
	if err := json.Unmarshal(raw, &ob); err != nil {
		return "", nil, err
	}
	for _, k := range []string{"domain_resolver", "detour", "bind_interface", "routing_mark", "flow_retry_delay"} {
		delete(ob, k)
	}
	ob["tag"] = "probe"
	if err := set(ob, value); err != nil {
		return "", nil, err
	}
	cfg, err := json.Marshal(map[string]interface{}{
		"log":       map[string]interface{}{"level": "error"},
		"inbounds":  []interface{}{map[string]interface{}{"type": "mixed", "listen": "127.0.0.1", "listen_port": "__PORT__"}},
		"outbounds": []interface{}{ob},
		"route":     map[string]interface{}{"final": "probe"},
	})
	if err != nil {
		return "", nil, err
	}
	// The port is chosen on the vantage, among its free ones.
	return strings.Replace(string(cfg), `"__PORT__"`, "__PORT__", 1), relayruns.ParamsOf(ob), nil
}

func matrixVantageList(s string) ([]string, error) {
	if s == "" {
		s = "spb,lv"
	}
	var out []string
	for _, v := range strings.Split(s, ",") {
		if _, ok := matrixVantages[v]; !ok {
			return nil, fmt.Errorf("vantage %q: want spb or lv", v)
		}
		out = append(out, v)
	}
	return out, nil
}

// matrixScript is the whole run on a vantage. Every value that reaches it was
// checked above or is base64; it prints one line a round:
//
//	B <unix> <round> <bytes> <end> <ok> <timeouts>                      no protocol
//	R <unix> <index> <round> <bytes> <end> <ok> <timeouts> <after>      a value
//	R <unix> <index> <round> load_error                                 the core did not start
func matrixScript(p matrixPlan) (string, error) {
	var b strings.Builder
	b.WriteString(`set -u
C=` + matrixCoreDir + `/sing-box
d=$(mktemp -d); chmod 700 $d
trap 'rm -rf $d' EXIT
freeport() { local p; while :; do p=$((20000 + RANDOM % 40000)); ss -ltn | grep -q ":$p " || { echo $p; return; }; done; }
end() { case $1 in 0) case $2 in 2??) echo ok;; *) echo "http$2";; esac;; 28) echo timeout;; *) echo closed;; esac; }
burst() {
  local n=$1 tag=$2 bp="" ok=0 to=0 i c rc; shift 2
  for i in $(seq $n); do (c=$(curl -s -o /dev/null -m 15 -w "%{http_code}" "$@" 2>/dev/null); echo "$? $c" > $d/$tag.$i) & bp="$bp $!"; done
  wait $bp
  for i in $(seq $n); do read rc c < $d/$tag.$i; [ "$rc" = 0 ] && [ "$c" != 000 ] && ok=$((ok+1)); [ "$rc" = 28 ] && to=$((to+1)); done
  echo "$ok $to"
}
`)
	if p.SNI != "" {
		fmt.Fprintf(&b, `ip=%s
case $ip in *[!0-9.]*) ip=$(getent ahostsv4 %s | awk '{print $1; exit}');; esac
baseline() {
  local n c rc t
  t=$(date +%%s)
  set -- $1 $(curl -s -o /dev/null -m 20 --resolve %s:%d:$ip -w "%%{size_download} %%{http_code}" https://%s:%d%s 2>/dev/null); rc=$?
  echo "B $t $1 ${2:-0} $(end $rc ${3:-000}) $(burst %d b$1 --resolve %s:%d:$ip https://%s:%d/)"
}
`, p.Server, p.Server, p.SNI, p.Port, p.SNI, p.Port, p.BasePath, matrixBurst, p.SNI, p.Port, p.SNI, p.Port)
	} else {
		b.WriteString("baseline() { :; }\n")
	}
	fmt.Fprintf(&b, `variant() {
  local i=$1 r=$2 cfg=$3 bn=$4 port pid k n c rc a=0 t
  t=$(date +%%s)
  port=$(freeport)
  printf '%%s' "$cfg" | base64 -d | sed "s/__PORT__/$port/" > $d/c$i.json
  $C run -c $d/c$i.json > $d/log$i 2>&1 & pid=$!
  for k in $(seq 50); do ss -ltn | grep -q "127.0.0.1:$port " && break; sleep 0.1; done
  if ! kill -0 $pid 2>/dev/null || ! ss -ltn | grep -q "127.0.0.1:$port "; then echo "R $t $i $r load_error"; kill $pid 2>/dev/null; return; fi
  P="--socks5-hostname 127.0.0.1:$port"
  read n c < <(curl -s -o /dev/null -m 20 $P -w "%%{size_download} %%{http_code}" "https://speed.cloudflare.com/__down?bytes=%d" 2>/dev/null; echo " $?" > $d/rc$i)
  rc=$(tr -d ' ' < $d/rc$i)
  set -- $(burst $bn v$i.$r $P http://cp.cloudflare.com/generate_204)
  for k in $(seq %d); do [ "$(curl -s -o /dev/null -m 5 $P -w "%%{http_code}" http://cp.cloudflare.com/generate_204 2>/dev/null)" = 204 ] && a=$((a+1)); done
  kill $pid 2>/dev/null; wait $pid 2>/dev/null
  echo "R $t $i $r ${n:-0} $(end $rc ${c:-000}) $1 $2 $a"
}
`, matrixBytes, matrixAfter)
	for i, cfg := range p.Configs {
		fmt.Fprintf(&b, "CFG%d=%s\n", i, base64.StdEncoding.EncodeToString([]byte(cfg)))
	}
	fmt.Fprintf(&b, "for r in $(seq %d); do\n  baseline $r; sleep 2\n", p.Rounds)
	for i := range p.Configs {
		fmt.Fprintf(&b, "  variant %d $r \"$CFG%d\" %d; sleep 2\n", i, i, p.burstOf(i))
	}
	b.WriteString("done\n")
	return b.String(), nil
}

// burstOf is how many requests value i opens at once.
func (p matrixPlan) burstOf(i int) int {
	if i < len(p.Bursts) {
		return p.Bursts[i]
	}
	return matrixBurst
}

// matrixRun puts the core on the vantage if it is not there yet and runs the
// script over one SSH connection.
func matrixRun(name, host, core, script string, p matrixPlan) ([]matrixRow, error) {
	if err := matrixUpload(host, core); err != nil {
		return nil, err
	}
	// About 25 s a value a round at worst, plus the baseline.
	wait := time.Duration(p.Rounds*(len(p.Configs)+1)*45+60) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", host, "bash -s")
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	rows := parseMatrix(stdout.String(), name, p)
	if err != nil {
		return rows, fmt.Errorf("ssh %s: %v: %s", host, err, strings.TrimSpace(stderr.String()))
	}
	return rows, nil
}

// matrixUpload copies the core unless the vantage already holds the same one.
func matrixUpload(host, core string) error {
	sum, err := fileSHA256(core)
	if err != nil {
		return err
	}
	remote := matrixCoreDir + "/sing-box"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	have, _ := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", host,
		"sha256sum "+remote+" 2>/dev/null | cut -d' ' -f1").Output()
	if strings.TrimSpace(string(have)) == sum {
		return nil
	}
	// The core is 70 MB and the link from here to a vantage can cut a long
	// flow (04.11: 25 MB died at both vantages while 5 MB passed). So it goes
	// gzipped, in parts small enough to pass, each retried on its own; the
	// vantage checks the whole against the hash before it replaces the old
	// core, so a cut upload never leaves a half core for the next run.
	parts, err := gzipParts(core, matrixUploadPart)
	if err != nil {
		return err
	}
	dir := matrixCoreDir + "/up"
	if out, err := sshRun(host, "rm -rf "+dir+" && mkdir -p "+dir, nil, time.Minute); err != nil {
		return fmt.Errorf("upload to %s: %v: %s", host, err, out)
	}
	for i, part := range parts {
		var last error
		for try := 0; try < 3; try++ {
			out, err := sshRun(host, fmt.Sprintf("cat > %s/p%04d", dir, i), part, 3*time.Minute)
			if err == nil {
				last = nil
				break
			}
			last = fmt.Errorf("part %d/%d: %v: %s", i+1, len(parts), err, out)
		}
		if last != nil {
			return fmt.Errorf("upload to %s: %w", host, last)
		}
	}
	join := fmt.Sprintf(`set -e; cat %[1]s/p* | gunzip > %[2]s.up; [ "$(sha256sum %[2]s.up | cut -d' ' -f1)" = %[3]s ] || { rm -f %[2]s.up; echo hash mismatch; exit 1; }; chmod 755 %[2]s.up; mv -f %[2]s.up %[2]s; rm -rf %[1]s`, dir, remote, sum)
	if out, err := sshRun(host, join, nil, 2*time.Minute); err != nil {
		return fmt.Errorf("upload to %s: %v: %s", host, err, out)
	}
	return nil
}

// gzipParts is a file gzipped and cut into parts of at most size bytes.
func gzipParts(path string, size int) ([][]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	all := buf.Bytes()
	var parts [][]byte
	for len(all) > 0 {
		n := min(size, len(all))
		parts = append(parts, all[:n])
		all = all[n:]
	}
	return parts, nil
}

// sshRun runs a command on host with stdin, within wait.
func sshRun(host, command string, stdin []byte, wait time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=10", host, command)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func parseMatrix(text, vantage string, p matrixPlan) []matrixRow {
	var rows []matrixRow
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		unix, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		row := matrixRow{TS: time.Unix(unix, 0).UTC().Format(time.RFC3339), Vantage: vantage, Profile: p.Profile, Core: p.Core}
		switch {
		case len(f) == 7 && f[0] == "B":
			row.Knob, row.Value = matrixBaseline, "no-protocol"
			row.BurstTotal = matrixBurst
			row.Round, _ = strconv.Atoi(f[2])
			row.Dl, _ = strconv.ParseInt(f[3], 10, 64)
			row.DlEnd = f[4]
			row.Burst, _ = strconv.Atoi(f[5])
			row.BurstTimeout, _ = strconv.Atoi(f[6])
		case len(f) >= 5 && f[0] == "R":
			i, err := strconv.Atoi(f[2])
			if err != nil || i < 0 || i >= len(p.Values) {
				continue
			}
			row.Knob, row.Value = p.Knob, p.Values[i]
			if i < len(p.Params) {
				row.Params = p.Params[i]
			}
			row.BurstTotal = p.burstOf(i)
			row.Round, _ = strconv.Atoi(f[3])
			if f[4] == "load_error" {
				row.LoadError = true
				break
			}
			if len(f) != 9 {
				continue
			}
			row.Dl, _ = strconv.ParseInt(f[4], 10, 64)
			row.DlEnd = f[5]
			row.Burst, _ = strconv.Atoi(f[6])
			row.BurstTimeout, _ = strconv.Atoi(f[7])
			row.After, _ = strconv.Atoi(f[8])
		default:
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

// matrixSummary sums the rounds of each vantage and value.
func matrixSummary(rows []matrixRow) string {
	type key struct{ vantage, knob, value string }
	type sum struct {
		rounds, dlFull, burst, asked, timeouts, after, loadErrors int
	}
	sums := map[key]*sum{}
	var order []key
	for _, r := range rows {
		k := key{r.Vantage, r.Knob, r.Value}
		s, ok := sums[k]
		if !ok {
			s = &sum{}
			sums[k] = s
			order = append(order, k)
		}
		s.rounds++
		if r.LoadError {
			s.loadErrors++
			continue
		}
		if r.DlEnd == "ok" {
			s.dlFull++
		}
		s.burst += r.Burst
		s.asked += r.BurstTotal
		s.timeouts += r.BurstTimeout
		s.after += r.After
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].vantage < order[j].vantage })
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "vantage\tknob\tvalue\trounds\tdl ok\tburst\ttimeouts\tafter\tload_error")
	for _, k := range order {
		s := sums[k]
		after := fmt.Sprintf("%d/%d", s.after, (s.rounds-s.loadErrors)*matrixAfter)
		if k.knob == matrixBaseline {
			after = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d/%d\t%d/%d\t%d\t%s\t%d\n", k.vantage, k.knob, k.value, s.rounds,
			s.dlFull, s.rounds-s.loadErrors, s.burst, s.asked, s.timeouts, after, s.loadErrors)
	}
	w.Flush()
	return strings.TrimRight(b.String(), "\n")
}

// matrixCore is the core users run, built for the vantages: sing-box-lx at the
// tag the client's CI pins, with the client's patches, cached by both.
func matrixCore() (path, label string, err error) {
	engine, err := clientEngineDir()
	if err != nil {
		return "", "", err
	}
	repo := filepath.Dir(engine)
	workflow, err := os.ReadFile(filepath.Join(repo, ".github", "workflows", "build-windows.yml"))
	if err != nil {
		return "", "", err
	}
	m := regexp.MustCompile(`(?m)^\s*SING_BOX_LX_TAG:\s*(\S+)`).FindSubmatch(workflow)
	if m == nil {
		return "", "", errors.New("no SING_BOX_LX_TAG in the client's build-windows.yml")
	}
	tag := string(m[1])
	patches, err := filepath.Glob(filepath.Join(repo, "core-patches", "sing-box-lx", "*.patch"))
	if err != nil {
		return "", "", err
	}
	sort.Strings(patches)
	h := sha256.New()
	for _, p := range patches {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", "", err
		}
		h.Write(data)
	}
	patchSum := hex.EncodeToString(h.Sum(nil))[:8]
	label = "sing-box-lx " + tag + "+" + patchSum
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(cache, "hikkari", "matrix", "sing-box-"+tag+"-"+patchSum+"-linux-amd64")
	if _, err := os.Stat(path); err == nil {
		return path, label, nil
	}
	if err := buildMatrixCore(tag, patches, path); err != nil {
		return "", "", err
	}
	return path, label, nil
}

// buildMatrixCore follows the client's CI (build-windows.yml, job sing-box).
func buildMatrixCore(tag string, patches []string, out string) error {
	work, err := os.MkdirTemp("", "hikkari-matrix-core-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	src := filepath.Join(work, "sbx")
	run := func(dir string, env []string, name string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	if _, err := run(work, nil, "git", "clone", "-q", "--depth", "1", "--branch", tag, "https://github.com/Leadaxe/sing-box-lx", src); err != nil {
		return err
	}
	if _, err := run(src, nil, "git", "submodule", "update", "-q", "--init", "--depth", "1",
		"submodules/gvisor", "submodules/sing-tun", "submodules/utls", "submodules/wireguard-go"); err != nil {
		return err
	}
	for _, p := range patches {
		if _, err := run(src, nil, "git", "apply", p); err != nil {
			return err
		}
	}
	tags, err := run(src, nil, "make", "-s", "-f", "Makefile.lx", "lx-print-tags")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	tmp := out + ".tmp"
	if _, err := run(src, []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"}, "go", "build", "-trimpath", "-tags", tags,
		"-ldflags", "-checklinkname=0 -s -w -buildid=", "-o", tmp, "./cmd/sing-box"); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}
