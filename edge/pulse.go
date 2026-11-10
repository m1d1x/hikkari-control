package main

// pulse.go is the relay's half-minute self-report to the hub (roadmap 42
// П9.3): the only one of the console's three vantages that can say a relay is
// up and idle rather than up and busy.
//
// # What goes up, and what never does
//
// Counters, and nothing that identifies whoever made them. The edge reads
// Xray's per-inbound byte totals, and per access key (keys.go): a key is a
// random string only the hub can tie to an account, so the counts let the hub
// add up a subscription's traffic across relays while the box still holds
// nothing that names a person (plan 42 §0.1). The shared credential is counted
// in the inbound totals only.
//
// # The answer
//
// The hub answers with every access key granted to this relay, and the edge
// makes its users exactly that list. No body (204) leaves the list as it is.
//
// # Totals, not deltas
//
// Every counter is a total since this process started. A delta is lost for
// good when a pulse is lost; a total survives it, because the next pulse still
// carries the whole. The hub differences consecutive rows and skips the pair
// where uptime went down — that is a restart, not negative traffic.
//
// # Failure is quiet on purpose
//
// A pulse that cannot be delivered is logged and forgotten. The relay's job is
// carrying people's bytes; it must not spend a goroutine retrying telemetry,
// and a hub that is down must not make a relay noisy. The gap in the console
// is itself the signal.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-protocol/observe"
	"github.com/xtls/xray-core/features/stats"
)

// pulseEvery is how often a relay reports. Matches entry.PulseInterval on the
// hub; the two are a contract, not a coincidence.
const pulseEvery = 30 * time.Second

// pulseTimeout bounds one delivery. Short: a pulse that has not landed in a
// few seconds is stale anyway, and the next one is half a minute away.
const pulseTimeout = 10 * time.Second

// pulseConfig is what the hub plants on the box for this. All three are
// required together — an edge with none of them simply does not report, which
// is how a box provisioned before this phase keeps running untouched.
//
// The build is NOT among them. The hub already knows what it pushed; what the
// console needs is what is actually running, and the only honest source for
// that is the binary itself. So the edge hashes its own executable and reports
// that — a rollout is then seen to have landed from the relay's own mouth
// rather than from the hub's record of its intent.
type pulseConfig struct {
	url     string
	token   string
	relay   string
	version string
}

// pulseFromEnv reads EDGE_PULSE_*. Returns false when the box was not given a
// pulse to send, which is not an error.
func pulseFromEnv(getenv func(string) string) (pulseConfig, bool) {
	c := pulseConfig{
		url:     strings.TrimSpace(getenv("EDGE_PULSE_URL")),
		token:   strings.TrimSpace(getenv("EDGE_PULSE_TOKEN")),
		relay:   strings.TrimSpace(getenv("EDGE_RELAY_NAME")),
		version: ownBuild(),
	}
	if c.url == "" || c.token == "" || c.relay == "" {
		return pulseConfig{}, false
	}
	return c, true
}

// ownBuild is the sha of the binary this process is running, short. Empty
// when the executable cannot be read — a figure we cannot stand behind is
// left out rather than guessed.
func ownBuild() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// pulseBody is the wire shape, shared by contract with
// internal/entry/pulse_api.go. The hub is a module of its own and cannot
// import this type, so the contract is the JSON and the two comments.
type pulseBody struct {
	Relay     string  `json:"relay"`
	UptimeS   int64   `json:"uptimeS"`
	BytesUp   int64   `json:"bytesUp"`
	BytesDown int64   `json:"bytesDown"`
	Conns     int64   `json:"conns"`
	CPUPct    float64 `json:"cpuPct"`
	MemMB     int64   `json:"memMb"`
	Version   string  `json:"version"`
	// Boot names this run of the process: per-key totals count from it.
	Boot string              `json:"boot,omitempty"`
	Keys map[string]pulseKey `json:"keys,omitempty"`
	// Hellos are the hello tap's counts since the start: cover → hk →
	// connections (hellotap.go); absent without a tap.
	Hellos map[string]map[string]int64 `json:"hellos,omitempty"`
	// Hikkari is v1.04 observability (hikkari-protocol spec/v1.md): what the
	// sessions of each "<path>/<key>" saw in the HikkariS seconds since the
	// last delivered pulse; absent on other protocols.
	Hikkari  map[string]*observe.Period `json:"hikkari,omitempty"`
	HikkariS int64                      `json:"hikkariS,omitempty"`
	// Drain is set by a generation a newer one took over (handoff.go): its
	// keys and periods count, but the relay's own row is the newest
	// generation's, or two processes' totals would interleave.
	Drain bool `json:"drain,omitempty"`
}

type pulseKey struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// pulseAnswer is the hub's reply, shared by contract with
// internal/entry/pulse_api.go.
type pulseAnswer struct {
	Keys *[]string `json:"keys"`
}

// maxPulseAnswer bounds the reply: a key list of some thousands of keys.
const maxPulseAnswer = 1 << 20

// pulseBootRetry is how soon a pulse is sent again while the hub's key list
// has not arrived since the start.
const pulseBootRetry = 2 * time.Second

// spawnPulse starts the reporting loop, if the box was given one. It returns
// at once; the loop stops with ctx.
//
// The first pulse goes at once, and again every pulseBootRetry until the hub
// answers with the key list: the key list comes back on a pulse, and an edge
// that waited a whole pulseEvery after its start refused every key added
// since the box was provisioned for that long. Rollout 09.11 22:11 UTC: ams3
// restarted at :58, its holders got 707 «invalid request user id» until the
// first pulse at +30 s, and the watch rolled the box back.
func spawnPulse(ctx context.Context, source pulseSource, cfg pulseConfig, keys keySetter) {
	started := time.Now()
	boot := newBoot()
	client := &http.Client{Timeout: pulseTimeout}

	go func() {
		keyed := false
		for {
			body := readPulse(source, cfg, started)
			body.Boot = boot
			answer, err := sendPulse(ctx, client, cfg, body)
			periodsSent(source, body.Hikkari, err == nil)
			switch {
			case err != nil:
				if ctx.Err() == nil {
					log.Printf("nekko-edge: pulse not delivered: %v", err)
				}
			case answer.Keys != nil:
				if err := keys.Set(ctx, *answer.Keys); err != nil {
					log.Printf("nekko-edge: %v", err)
				} else {
					keyed = true
				}
			default:
				keyed = true // a hub that sends no list leaves the keys as they are
			}
			wait := pulseEvery
			if !keyed {
				wait = pulseBootRetry
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}

// pulseSource is who counts a relay's traffic. Xray does for what passes
// through its inbounds; an AmneziaWG device counts its own peers.
type pulseSource interface {
	read() (up, down, conns int64, keys map[string]pulseKey)
}

// keySetter takes the hub's list of keys.
type keySetter interface {
	Set(ctx context.Context, keys []string) error
}

type xraySource struct{ counters stats.Manager }

func (s xraySource) read() (up, down, conns int64, keys map[string]pulseKey) {
	up, down, conns = inboundTotals(s.counters)
	return up, down, conns, keyTotals(s.counters)
}

// tappedSource adds the hello tap's counts to a source's report.
type tappedSource struct {
	pulseSource
	tap *helloTap
}

func (s tappedSource) hellos() map[string]map[string]int64 { return s.tap.totals() }

type awgSource struct{ server *awgServer }

func (s awgSource) read() (up, down, conns int64, keys map[string]pulseKey) {
	return s.server.totals(time.Now())
}

// readPulse assembles one report from Xray's counters and the box's own load.
func readPulse(source pulseSource, cfg pulseConfig, started time.Time) pulseBody {
	up, down, conns, keys := source.read()
	cpu, mem := boxLoad()
	body := pulseBody{
		Relay:     cfg.relay,
		UptimeS:   int64(time.Since(started).Seconds()),
		BytesUp:   up,
		BytesDown: down,
		Conns:     conns,
		CPUPct:    cpu,
		MemMB:     mem,
		Version:   cfg.version,
		Keys:      keys,
		Hellos:    helloTotals(source),
	}
	if s, ok := source.(observedSource); ok {
		body.Hikkari, body.HikkariS = s.periods()
	}
	if s, ok := source.(interface{ drained() bool }); ok {
		body.Drain = s.drained()
	}
	return body
}

// observedSource is a source that keeps a v1.04 book (naiveRuntime on a
// Hikkari relay).
type observedSource interface {
	periods() (map[string]*observe.Period, int64)
	periodsSent(map[string]*observe.Period, bool)
}

func periodsSent(source pulseSource, periods map[string]*observe.Period, ok bool) {
	if s, isObserved := source.(observedSource); isObserved {
		s.periodsSent(periods, ok)
	}
}

// helloTotals is the hello tap's counts, when the source has a tap.
func helloTotals(source pulseSource) map[string]map[string]int64 {
	if s, ok := source.(interface {
		hellos() map[string]map[string]int64
	}); ok {
		return s.hellos()
	}
	return nil
}

// newBoot names one run of the process.
func newBoot() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		// Without a random name the per-key totals of two runs could share a
		// row; the time of the start still keeps them apart.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

// inboundTotals sums every inbound's byte counters. Summed rather than
// reported per inbound because one relay is one row in the console: a Reality
// edge has an inbound per cover, and which cover carried the bytes is the
// prober's question, not the load's.
func inboundTotals(counters stats.Manager) (up, down, conns int64) {
	// Xray names them inbound>>>tag>>>traffic>>>{uplink,downlink}. Walking the
	// manager by name prefix keeps this independent of how many inbounds a
	// protocol happens to build.
	counters.VisitCounters(func(name string, c stats.Counter) bool {
		if !strings.HasPrefix(name, "inbound>>>") {
			return true
		}
		switch {
		case strings.HasSuffix(name, ">>>traffic>>>uplink"):
			up += c.Value()
		case strings.HasSuffix(name, ">>>traffic>>>downlink"):
			down += c.Value()
		}
		return true
	})
	// Xray keeps no connection counter of its own; the box's does.
	conns = establishedConns()
	return up, down, conns
}

// boxLoad reads the machine's load from /proc. The box's, not this process's:
// one box runs several relays, and what matters is whether the machine is out
// of room. Zeroes on a platform without /proc — the edge runs on Linux, and a
// developer's mac must not fail for want of a file.
func boxLoad() (cpuPct float64, memMB int64) {
	// loadavg's first field over the core count: a coarse "how busy", which is
	// what a half-minute pulse can honestly claim. A true per-cent would need
	// two samples of /proc/stat and the bookkeeping between them, for a figure
	// nobody reads that closely.
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(raw)); len(f) > 0 {
			if load, err := strconv.ParseFloat(f[0], 64); err == nil {
				cpuPct = load * 100 / float64(max(1, runtime.NumCPU()))
				if cpuPct > 100 {
					cpuPct = 100
				}
			}
		}
	}
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, available int64
		for _, line := range strings.Split(string(raw), "\n") {
			switch {
			case strings.HasPrefix(line, "MemTotal:"):
				total = meminfoKB(line)
			case strings.HasPrefix(line, "MemAvailable:"):
				available = meminfoKB(line)
			}
		}
		if total > 0 && available >= 0 {
			memMB = (total - available) / 1024
		}
	}
	return cpuPct, memMB
}

// meminfoKB pulls the kB figure out of a /proc/meminfo line.
func meminfoKB(line string) int64 {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	v, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// establishedConns counts established TCP sockets on the box, from /proc/net.
// A count, never the addresses in those lines: the count says how loaded the
// box is, the addresses would say who is on it.
func establishedConns() int64 {
	var n int64
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			// Column 4 is the state; 01 is ESTABLISHED.
			if cols := strings.Fields(line); len(cols) > 3 && cols[3] == "01" {
				n++
			}
		}
	}
	return n
}

// sendPulse delivers one report and reads the hub's answer to it.
func sendPulse(ctx context.Context, client *http.Client, cfg pulseConfig, body pulseBody) (pulseAnswer, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return pulseAnswer{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, pulseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.url, bytes.NewReader(raw))
	if err != nil {
		return pulseAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.token)
	resp, err := client.Do(req)
	if err != nil {
		return pulseAnswer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return pulseAnswer{}, fmt.Errorf("hub answered %s", resp.Status)
	}
	var answer pulseAnswer
	if resp.StatusCode == http.StatusNoContent {
		return answer, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPulseAnswer)).Decode(&answer); err != nil {
		return pulseAnswer{}, fmt.Errorf("hub answer: %w", err)
	}
	return answer, nil
}
