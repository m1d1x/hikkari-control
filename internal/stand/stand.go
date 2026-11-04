// Package stand is the matrix stand (hikkari-client
// plans/2026-11-04-relay-search-method.md): one profile of a subscription, one
// knob of its config, a few values of that knob, measured on the core users run
// the way [diag:load] measures: a 1 MB download, 50 requests at once, 3 after.
//
// Every round also measures the same address and port with no protocol, so a
// row can tell a burned knob from a cut address. Rounds go variant by variant
// within a round, not variant after variant, so a path that worsens over the
// run does not pass for a worse value.
//
// A round starts by asking whether the address answers at all: AddrFlows
// connections, a ClientHello on each. A banned address opens TCP and then loses
// the first packet of data, whatever the protocol (NTC Party 26076), so every
// value of every knob would read red there and say nothing about the knob. Such
// a round writes one row, addr_dead, and measures no value.
//
// The run itself is a bash script over curl: the same script runs on a vantage
// over SSH (`ops matrix`) and locally under the vantage's prober (the stand on
// the hub's schedule, schedule.go), so rows from either are the same
// measurement. The profile's outbound holds its secrets: it reaches the script
// base64-encoded, the script keeps it in a private temporary directory, and a
// row names the profile by its label and the knob by its value.
package stand

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/m1d1x/hikkari-control/internal/relayruns"
)

const (
	Bytes    = 1 << 20
	Burst    = 50
	MaxBurst = 200
	// LoadBurst is the load knob: the burst's size, not the config's.
	LoadBurst = relayruns.KnobBurst
	After     = 3
	Rounds    = 3
	MaxRounds = 10
	MaxValues = 8
	// Baseline is the knob of the rows with no protocol.
	Baseline = relayruns.KnobBaseline
	// AddrFlows connections ask whether the address answers, each given
	// addrWait seconds. A censor drops some flows by source port and leaves
	// others (plans/2026-11-02-tcp-flow-loss.md), so one answer is enough.
	AddrFlows = 12
	addrWait  = 8
)

// Values of a baseline row that end a round before any value is measured.
const (
	// ValueNoProtocol is the ordinary baseline: the cover site with no protocol.
	ValueNoProtocol = "no-protocol"
	// ValueAddrDead: TCP opened and not one flow got a byte back for its
	// ClientHello.
	ValueAddrDead = "addr_dead"
	// ValueNoTCP: no flow opened at all.
	ValueNoTCP = "no_tcp"
	// ValueVantageDown: the address is silent, and so is the point's control.
	// It says nothing about the address.
	ValueVantageDown = "vantage_down"
)

// addrControl is a site the point reaches when its own network is up.
const addrControl = "https://1.1.1.1/"

// udpOutbounds carry no TCP, so neither the baseline's TLS nor the address
// check says anything about them.
var udpOutbounds = map[string]bool{"hysteria": true, "hysteria2": true, "tuic": true, "wireguard": true}

var (
	reHost  = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)
	rePath  = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,200}$`)
	reValue = regexp.MustCompile(`^[A-Za-z0-9._,/-]{0,64}$`)
	reCore  = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,200}$`)
)

// ValidValue reports whether v can be a knob's value.
func ValidValue(v string) bool { return reValue.MatchString(v) }

// ValidPath reports whether p can be fetched as the baseline page.
func ValidPath(p string) bool { return rePath.MatchString(p) }

// Knobs set one knob on a sing-box outbound. A value the knob cannot take is
// an error, not a silently different experiment.
var Knobs = map[string]func(ob map[string]any, v string) error{
	"fp": func(ob map[string]any, v string) error {
		tls, err := outboundTLS(ob)
		if err != nil {
			return err
		}
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": v}
		return nil
	},
	"sni": func(ob map[string]any, v string) error {
		if !reHost.MatchString(v) {
			return fmt.Errorf("sni %q is not a host name", v)
		}
		tls, err := outboundTLS(ob)
		if err != nil {
			return err
		}
		tls["server_name"] = v
		return nil
	},
	"port": func(ob map[string]any, v string) error {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return fmt.Errorf("port %q", v)
		}
		ob["server_port"] = p
		return nil
	},
	"flow": func(ob map[string]any, v string) error {
		if v == "" || v == "none" {
			delete(ob, "flow")
			return nil
		}
		ob["flow"] = v
		return nil
	},
	"alpn": func(ob map[string]any, v string) error {
		tls, err := outboundTLS(ob)
		if err != nil {
			return err
		}
		// Values are split by "/", so http/1.1 is written http1.1.
		var list []any
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

func outboundTLS(ob map[string]any) (map[string]any, error) {
	tls, ok := ob["tls"].(map[string]any)
	if !ok {
		return nil, errors.New("the profile has no TLS")
	}
	return tls, nil
}

// KnobNames are the knobs a run can vary, the burst among them.
func KnobNames() []string {
	out := make([]string, 0, len(Knobs)+1)
	for k := range Knobs {
		out = append(out, k)
	}
	out = append(out, LoadBurst)
	sort.Strings(out)
	return out
}

// Row is one round of one variant from one vantage: a line of the dataset.
type Row struct {
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

// Spec is what to vary on one profile.
type Spec struct {
	Profile string
	Knob    string
	Values  []string
	// Base is the knobs set on every value before the varied one,
	// "fp=chrome;alpn=h2", so a knob is tried on a base that shows a
	// difference (alpn on fp=chrome).
	Base string
	// BasePath is fetched with no protocol: a page of the cover site big
	// enough to pass a cut by volume, "/" when empty.
	BasePath string
	Rounds   int
}

// Plan is what one run measures.
type Plan struct {
	Profile string
	Knob    string
	Values  []string
	Configs []string            // whole core configs, one per value, listen port left as __PORT__
	Params  []map[string]string // the knobs each config runs with, one per value
	Bursts  []int               // how many requests each value's burst opens at once
	Base    string
	Server  string
	SNI     string
	Port    int
	// BasePath is fetched with no protocol.
	BasePath string
	Rounds   int
	Core     string
}

// NewPlan builds the run of spec on the profile's outbound, as the client's
// parser built it. The outbound itself is not changed.
func NewPlan(outbound map[string]any, spec Spec) (Plan, error) {
	set, known := Knobs[spec.Knob]
	if spec.Knob == LoadBurst {
		// Not a knob of the config: how many requests the burst opens at once,
		// on the profile as it is. It finds the count a cut starts at.
		set, known = func(map[string]any, string) error { return nil }, true
	}
	if !known {
		return Plan{}, fmt.Errorf("knob %q: want one of %s", spec.Knob, strings.Join(KnobNames(), ", "))
	}
	if len(spec.Values) == 0 || len(spec.Values) > MaxValues {
		return Plan{}, fmt.Errorf("want 1 to %d values", MaxValues)
	}
	for _, v := range spec.Values {
		if !reValue.MatchString(v) {
			return Plan{}, fmt.Errorf("value %q", v)
		}
	}
	rounds := spec.Rounds
	if rounds == 0 {
		rounds = Rounds
	}
	if rounds < 1 || rounds > MaxRounds {
		return Plan{}, fmt.Errorf("rounds: 1 to %d", MaxRounds)
	}
	ob, err := clone(outbound)
	if err != nil {
		return Plan{}, err
	}
	base, err := ApplyBase(ob, spec.Base, spec.Knob)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Profile: spec.Profile, Knob: spec.Knob, Values: spec.Values, Rounds: rounds, BasePath: "/", Base: base}
	if spec.BasePath != "" {
		if !rePath.MatchString(spec.BasePath) {
			return Plan{}, fmt.Errorf("baseline path %q", spec.BasePath)
		}
		plan.BasePath = spec.BasePath
	}
	plan.Server, _ = ob["server"].(string)
	plan.Port = intOf(ob["server_port"])
	if tls, ok := ob["tls"].(map[string]any); ok {
		plan.SNI, _ = tls["server_name"].(string)
	}
	if !reHost.MatchString(plan.Server) || plan.Port <= 0 {
		return Plan{}, errors.New("the profile has no server address")
	}
	if plan.SNI != "" && !reHost.MatchString(plan.SNI) {
		return Plan{}, errors.New("the profile's server name is not a host name")
	}
	if typ, _ := ob["type"].(string); udpOutbounds[typ] {
		plan.SNI = "" // no TCP to ask: no baseline and no address check
	}
	for _, v := range spec.Values {
		cfg, params, err := Config(ob, set, v)
		if err != nil {
			return Plan{}, fmt.Errorf("%s=%s: %w", spec.Knob, v, err)
		}
		plan.Configs = append(plan.Configs, cfg)
		plan.Params = append(plan.Params, params)
		n := Burst
		if spec.Knob == LoadBurst {
			if n, err = strconv.Atoi(v); err != nil || n < 1 || n > MaxBurst {
				return Plan{}, fmt.Errorf("burst %q: 1 to %d", v, MaxBurst)
			}
		}
		plan.Bursts = append(plan.Bursts, n)
	}
	return plan, nil
}

// ApplyBase sets the base knobs, "fp=chrome;alpn=h2", on the outbound every
// value starts from. The varied knob cannot be among them, nor can the burst,
// which is not a knob of the config. It returns the base as given.
func ApplyBase(ob map[string]any, base, varied string) (string, error) {
	if base == "" {
		return "", nil
	}
	var names []string
	for _, pair := range strings.Split(base, ";") {
		k, v, ok := strings.Cut(pair, "=")
		set, known := Knobs[k]
		if !ok || !known {
			return "", fmt.Errorf("base %q: want knob=value;knob=value", pair)
		}
		if k == varied {
			return "", fmt.Errorf("base sets %s, the knob this run varies", k)
		}
		if !reValue.MatchString(v) {
			return "", fmt.Errorf("base value %q", v)
		}
		if err := set(ob, v); err != nil {
			return "", fmt.Errorf("base %s=%s: %w", k, v, err)
		}
		names = append(names, k+"="+v)
	}
	return strings.Join(names, ";"), nil
}

// Config is the whole core config of one value: the profile's outbound with
// the knob set, behind a local mixed inbound. Fields that name the client's
// own config or are set for testers only are dropped, so every value runs what
// a user runs but for the knob.
func Config(outbound map[string]any, set func(map[string]any, string) error, value string) (string, map[string]string, error) {
	ob, err := clone(outbound)
	if err != nil {
		return "", nil, err
	}
	for _, k := range []string{"domain_resolver", "detour", "bind_interface", "routing_mark", "flow_retry_delay"} {
		delete(ob, k)
	}
	ob["tag"] = "probe"
	if err := set(ob, value); err != nil {
		return "", nil, err
	}
	cfg, err := json.Marshal(map[string]any{
		"log":       map[string]any{"level": "error"},
		"inbounds":  []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": "__PORT__"}},
		"outbounds": []any{ob},
		"route":     map[string]any{"final": "probe"},
	})
	if err != nil {
		return "", nil, err
	}
	// The port is chosen on the vantage, among its free ones.
	return strings.Replace(string(cfg), `"__PORT__"`, "__PORT__", 1), relayruns.ParamsOf(ob), nil
}

func clone(ob map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(ob)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// BurstOf is how many requests value i opens at once.
func (p Plan) BurstOf(i int) int {
	if i < len(p.Bursts) {
		return p.Bursts[i]
	}
	return Burst
}

// Wait is the longest a run of the plan takes: about 25 s a value a round at
// worst, plus the baseline and the address check.
func (p Plan) Wait() time.Duration {
	return time.Duration(p.Rounds*((len(p.Configs)+1)*45+addrWait+7)+60) * time.Second
}

// Script is the whole run, with the core at core. The address check counts TCP
// by itself: on a timeout curl reports time_connect 0 whether or not TCP opened.
// A connection refused (7) or timed out (28) before TLS is no answer; anything
// else is, a reset among them: it is not the silence of a banned address. Every value that reaches it
// was checked above or is base64; it prints one line a round:
//
//	D <unix> <round> <addr_dead|no_tcp|vantage_down> <silent flows>     the round stops here
//	B <unix> <round> <bytes> <end> <ok> <timeouts>                      no protocol
//	R <unix> <index> <round> <bytes> <end> <ok> <timeouts> <after>      a value
//	R <unix> <index> <round> load_error                                 the core did not start
func Script(p Plan, core string) (string, error) {
	if !reCore.MatchString(core) {
		return "", fmt.Errorf("core path %q", core)
	}
	var b strings.Builder
	b.WriteString(`set -u
C=` + core + `
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
tcpopen() {
  (exec 3<>/dev/tcp/$ip/%d) 2>/dev/null & local p=$! k rc
  (sleep %d; kill $p 2>/dev/null) >/dev/null 2>&1 & k=$!
  wait $p 2>/dev/null; rc=$?
  kill $k 2>/dev/null
  return $rc
}
addrcheck() {
  local i bp="" tcp=0 said=0 t o rc a v
  t=$(date +%%s)
  for i in $(seq %d); do (o=0; tcpopen && o=1; a=$(curl -skI -o /dev/null -m %d --resolve %s:%d:$ip -w "%%{time_appconnect}" https://%s:%d/ 2>/dev/null); rc=$?; echo "$o $rc $a" > $d/a.$i) & bp="$bp $!"; done
  wait $bp
  for i in $(seq %d); do
    read o rc a < $d/a.$i
    [ "$o" = 1 ] && tcp=$((tcp+1))
    case "$rc" in 28|7) [ "${a:-0.000000}" != 0.000000 ] && said=$((said+1));; *) said=$((said+1));; esac
  done
  [ $said -gt 0 ] && return 0
  v=%s; [ $tcp = 0 ] && v=%s
  curl -skI -o /dev/null -m 5 %s 2>/dev/null || v=%s
  echo "D $t $1 $v $tcp"
  return 1
}
`, p.Server, p.Server, p.SNI, p.Port, p.SNI, p.Port, p.BasePath, Burst, p.SNI, p.Port, p.SNI, p.Port,
			p.Port, addrWait/2, AddrFlows, addrWait, p.SNI, p.Port, p.SNI, p.Port, AddrFlows, ValueAddrDead, ValueNoTCP, addrControl, ValueVantageDown)
	} else {
		b.WriteString("baseline() { :; }\naddrcheck() { :; }\n")
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
`, Bytes, After)
	for i, cfg := range p.Configs {
		fmt.Fprintf(&b, "CFG%d=%s\n", i, base64.StdEncoding.EncodeToString([]byte(cfg)))
	}
	fmt.Fprintf(&b, "for r in $(seq %d); do\n  addrcheck $r || { sleep 2; continue; }\n  baseline $r; sleep 2\n", p.Rounds)
	for i := range p.Configs {
		fmt.Fprintf(&b, "  variant %d $r \"$CFG%d\" %d; sleep 2\n", i, i, p.BurstOf(i))
	}
	b.WriteString("done\n")
	return b.String(), nil
}

// Parse reads the script's output as rows of vantage.
func Parse(text, vantage string, p Plan) []Row {
	var rows []Row
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
		row := Row{TS: time.Unix(unix, 0).UTC().Format(time.RFC3339), Vantage: vantage, Profile: p.Profile, Core: p.Core}
		switch {
		case len(f) == 5 && f[0] == "D":
			if f[3] != ValueAddrDead && f[3] != ValueNoTCP && f[3] != ValueVantageDown {
				continue
			}
			row.Knob, row.Value = Baseline, f[3]
			row.BurstTotal = AddrFlows
			row.Round, _ = strconv.Atoi(f[2])
			row.DlEnd = "timeout"
			row.BurstTimeout, _ = strconv.Atoi(f[4])
		case len(f) == 7 && f[0] == "B":
			row.Knob, row.Value = Baseline, ValueNoProtocol
			row.BurstTotal = Burst
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
			row.BurstTotal = p.BurstOf(i)
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

// DatasetRows are the run's rows as the dataset keeps them.
func DatasetRows(rows []Row) ([]relayruns.Row, error) {
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
		if r.Knob != Baseline {
			d.After, d.AfterTotal = r.After, After
		}
		if r.LoadError {
			d.BurstTotal = 0
		}
		out = append(out, d)
	}
	return out, nil
}

// Summary sums the rounds of each vantage, profile and value.
func Summary(rows []Row) string {
	type key struct{ vantage, profile, knob, value string }
	type sum struct {
		rounds, dlFull, burst, asked, timeouts, after, loadErrors int
	}
	sums := map[key]*sum{}
	var order []key
	for _, r := range rows {
		k := key{r.Vantage, r.Profile, r.Knob, r.Value}
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
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].vantage != order[j].vantage {
			return order[i].vantage < order[j].vantage
		}
		return order[i].profile < order[j].profile
	})
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "vantage\tprofile\tknob\tvalue\trounds\tdl ok\tburst\ttimeouts\tafter\tload_error")
	for _, k := range order {
		s := sums[k]
		after := fmt.Sprintf("%d/%d", s.after, (s.rounds-s.loadErrors)*After)
		if k.knob == Baseline {
			after = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d/%d\t%d/%d\t%d\t%s\t%d\n", k.vantage, k.profile, k.knob, k.value, s.rounds,
			s.dlFull, s.rounds-s.loadErrors, s.burst, s.asked, s.timeouts, after, s.loadErrors)
	}
	w.Flush()
	return strings.TrimRight(b.String(), "\n")
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}
