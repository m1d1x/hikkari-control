package support

import (
	"context"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What each relay did for each tester, read from the [diag:reach] round the
// client ships in its log every 10 minutes (hikkari-client
// engine/singbox/diagnostics/reach.go). Only testers: everyone else reports
// anonymous aggregates to the downdetector and cannot be told apart.

// Why a relay did not answer. The ping alone cannot tell: the core's delay
// endpoint answers one generic error, so the TCP flows counted beside it decide.
const (
	RelayTCPBlocked = "tcp_blocked" // no flow opens while the control does
	RelayTCPThinned = "tcp_thinned" // some flows open
	RelayProtocol   = "protocol"    // flows open, the protocol does not pass
	RelayTimeout    = "timeout"     // no flow count (UDP or no card to count from)
	RelayError      = "error"
)

// TesterRelay is one profile as the install's latest round saw it.
type TesterRelay struct {
	Tag        string     `json:"tag"`
	Proto      string     `json:"proto"`
	Up         bool       `json:"up"`
	PingMs     int        `json:"pingMs,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	FlowsOpen  *int       `json:"flowsOpen,omitempty"`
	FlowsTotal int        `json:"flowsTotal,omitempty"`
	LastOK     *time.Time `json:"lastOk,omitempty"`
}

// TesterRelays is one install's relays.
type TesterRelays struct {
	InstallID string        `json:"install_id"`
	Platform  string        `json:"platform"`
	Version   string        `json:"version"`
	Network   string        `json:"network,omitempty"`
	At        time.Time     `json:"at"`
	Relays    []TesterRelay `json:"relays"`
}

var (
	reReachControl = regexp.MustCompile(`\[diag:reach\] control .* flows=(\d+)/(\d+)`)
	reReachSelf    = regexp.MustCompile(`\[diag:reach\] self .* isp=(\S+)`)
	reReachProfile = regexp.MustCompile(`\[diag:reach\] (p-\S+) type=(\S+)(?: proto=(\S+))?.* ping=(?:(\d+)ms|fail\((.*?)\))(?: flows\[|$)`)
	reReachFlows   = regexp.MustCompile(`flows\[[^\]]*\]=(\d+)/(\d+)`)
)

// reachLine is one profile's result in a round.
type reachLine struct {
	tag, proto string
	up         bool
	pingMs     int
	pingErr    string
	flowsOpen  *int
	flowsTotal int
}

func parseReachLine(line string) (reachLine, bool) {
	m := reReachProfile.FindStringSubmatch(line)
	if m == nil {
		return reachLine{}, false
	}
	out := reachLine{tag: m[1], proto: m[3], pingErr: m[5]}
	if out.proto == "" {
		out.proto = m[2]
	}
	if m[4] != "" {
		out.up = true
		out.pingMs, _ = strconv.Atoi(m[4])
	}
	if f := reReachFlows.FindStringSubmatch(line); f != nil {
		open, _ := strconv.Atoi(f[1])
		out.flowsOpen = &open
		out.flowsTotal, _ = strconv.Atoi(f[2])
	}
	return out, true
}

// eachReachResult walks an install's chunks, oldest first, giving every profile
// line with the reason it failed. The control line comes before the profiles of
// its round, so the reason is judged against the same round's control.
func eachReachResult(chunks []TesterChunk, network *string, visit func(at time.Time, line reachLine, reason string)) {
	controlOpen := false
	for _, c := range chunks {
		for _, text := range strings.Split(c.Body, "\n") {
			if !strings.Contains(text, "[diag:reach] ") {
				continue
			}
			if m := reReachSelf.FindStringSubmatch(text); m != nil {
				*network = m[1]
				continue
			}
			if m := reReachControl.FindStringSubmatch(text); m != nil {
				open, _ := strconv.Atoi(m[1])
				controlOpen = open > 0
				continue
			}
			line, ok := parseReachLine(text)
			if !ok {
				continue
			}
			reason := ""
			if !line.up {
				reason = relayFailure(line.pingErr, line.flowsOpen, line.flowsTotal, controlOpen)
			}
			visit(c.ReceivedAt, line, reason)
		}
	}
}

// readTesterRelays folds an install's chunks into its relays. The newest line of
// a profile is its state.
func readTesterRelays(install TesterInstall, chunks []TesterChunk) (TesterRelays, bool) {
	out := TesterRelays{InstallID: install.InstallID, Platform: install.Platform, Version: install.Version}
	relays := map[string]*TesterRelay{}
	eachReachResult(chunks, &out.Network, func(at time.Time, line reachLine, reason string) {
		relay := relays[line.tag]
		if relay == nil {
			relay = &TesterRelay{Tag: line.tag}
			relays[line.tag] = relay
		}
		relay.Proto, relay.Up, relay.PingMs, relay.Reason = line.proto, line.up, line.pingMs, reason
		relay.FlowsOpen, relay.FlowsTotal = line.flowsOpen, line.flowsTotal
		if line.up {
			seen := at
			relay.LastOK = &seen
		}
		out.At = at
	})
	if len(relays) == 0 {
		return out, false
	}
	out.Relays = make([]TesterRelay, 0, len(relays))
	for _, relay := range relays {
		out.Relays = append(out.Relays, *relay)
	}
	sort.Slice(out.Relays, func(i, j int) bool { return out.Relays[i].Tag < out.Relays[j].Tag })
	return out, true
}

func relayFailure(ping string, flowsOpen *int, flowsTotal int, controlOpen bool) string {
	if flowsOpen != nil && controlOpen {
		switch {
		case *flowsOpen == 0:
			return RelayTCPBlocked
		case *flowsOpen < flowsTotal:
			return RelayTCPThinned
		default:
			return RelayProtocol
		}
	}
	lower := strings.ToLower(ping)
	if strings.Contains(lower, "504") || strings.Contains(lower, "timeout") {
		return RelayTimeout
	}
	return RelayError
}

// TesterProtocol is one protocol over the window, as testers measured it.
type TesterProtocol struct {
	Proto    string     `json:"proto"`
	Checks   int        `json:"checks"`
	SharePct float64    `json:"sharePct"` // of all passed checks
	OkPct    float64    `json:"okPct"`
	P50Ms    *int       `json:"p50Ms"`
	P95Ms    *int       `json:"p95Ms"`
	OkSeries []*float64 `json:"okSeries"`
	Ping     []*int     `json:"pingSeries"`
}

// TesterSeries is one line of a chart: a value per bucket, null where nothing
// was measured.
type TesterSeries struct {
	Key    string     `json:"key"`
	Values []*float64 `json:"values"`
}

// TesterRelayScreen is the protocols screen in one response.
type TesterRelayScreen struct {
	Window        string           `json:"window"`
	BucketSeconds int              `json:"bucketSeconds"`
	Buckets       []time.Time      `json:"buckets"`
	Protocols     []TesterProtocol `json:"protocols"`
	// ClientsByUp is the share of testers per bucket by how many relays answered.
	ClientsByUp []TesterSeries `json:"clientsByUp"`
	// Failures counts failed checks per bucket by reason.
	Failures []TesterSeries `json:"failures"`
	Installs []TesterRelays `json:"installs"`
}

// testerRelayWindows are the screen's windows. The log is kept 7 days, so there
// is no longer one.
var testerRelayWindows = map[string]struct{ span, step time.Duration }{
	"24h": {24 * time.Hour, time.Hour},
	"7d":  {7 * 24 * time.Hour, 6 * time.Hour},
}

func percentile(sorted []int, p float64) *int {
	if len(sorted) == 0 {
		return nil
	}
	v := sorted[int(math.Ceil(float64(len(sorted))*p))-1]
	return &v
}

// buildTesterRelayScreen reads the window's rounds of every install.
func buildTesterRelayScreen(ctx context.Context, store TesterStore, window string, now time.Time) (*TesterRelayScreen, error) {
	win := testerRelayWindows[window]
	end := now.Truncate(win.step)
	n := int(win.span / win.step)
	first := end.Add(-time.Duration(n-1) * win.step)
	screen := &TesterRelayScreen{
		Window: window, BucketSeconds: int(win.step.Seconds()), Protocols: []TesterProtocol{},
		ClientsByUp: []TesterSeries{}, Failures: []TesterSeries{}, Installs: []TesterRelays{},
	}
	for i := 0; i < n; i++ {
		screen.Buckets = append(screen.Buckets, first.Add(time.Duration(i)*win.step))
	}

	type protoAcc struct {
		ok, all  int
		pings    []int
		okBucket []int
		allBkt   []int
		pingBkt  [][]int
	}
	protos := map[string]*protoAcc{}
	failures := map[string][]float64{}
	upGroups := map[string][]float64{"none": make([]float64, n), "one": make([]float64, n), "some": make([]float64, n), "all": make([]float64, n)}
	testers := make([]float64, n)

	installs, err := store.ListInstalls(ctx)
	if err != nil {
		return nil, err
	}
	for _, install := range installs {
		if install.LastSeen.Before(first) {
			continue
		}
		chunks, err := store.ReadReachLines(ctx, install.InstallID, first, now)
		if err != nil {
			return nil, err
		}
		if relays, ok := readTesterRelays(install, chunks); ok {
			screen.Installs = append(screen.Installs, relays)
		}
		// A tester's state in a bucket is the last result of each of its relays.
		last := make([]map[string]bool, n)
		var network string
		eachReachResult(chunks, &network, func(at time.Time, line reachLine, reason string) {
			i := int(at.Sub(first) / win.step)
			if i < 0 || i >= n {
				return
			}
			acc := protos[line.proto]
			if acc == nil {
				acc = &protoAcc{okBucket: make([]int, n), allBkt: make([]int, n), pingBkt: make([][]int, n)}
				protos[line.proto] = acc
			}
			acc.all++
			acc.allBkt[i]++
			if line.up {
				acc.ok++
				acc.okBucket[i]++
				acc.pings = append(acc.pings, line.pingMs)
				acc.pingBkt[i] = append(acc.pingBkt[i], line.pingMs)
			} else {
				if failures[reason] == nil {
					failures[reason] = make([]float64, n)
				}
				failures[reason][i]++
			}
			if last[i] == nil {
				last[i] = map[string]bool{}
			}
			last[i][line.tag] = line.up
		})
		for i, state := range last {
			if state == nil {
				continue
			}
			up := 0
			for _, alive := range state {
				if alive {
					up++
				}
			}
			testers[i]++
			switch {
			case up == 0:
				upGroups["none"][i]++
			case up == len(state):
				upGroups["all"][i]++
			case up == 1:
				upGroups["one"][i]++
			default:
				upGroups["some"][i]++
			}
		}
	}

	totalOK := 0
	for _, acc := range protos {
		totalOK += acc.ok
	}
	for proto, acc := range protos {
		sort.Ints(acc.pings)
		p := TesterProtocol{
			Proto: proto, Checks: acc.all, OkPct: float64(acc.ok) / float64(acc.all) * 100,
			P50Ms: percentile(acc.pings, 0.5), P95Ms: percentile(acc.pings, 0.95),
			OkSeries: make([]*float64, n), Ping: make([]*int, n),
		}
		if totalOK > 0 {
			p.SharePct = float64(acc.ok) / float64(totalOK) * 100
		}
		for i := 0; i < n; i++ {
			if acc.allBkt[i] > 0 {
				v := float64(acc.okBucket[i]) / float64(acc.allBkt[i]) * 100
				p.OkSeries[i] = &v
			}
			sort.Ints(acc.pingBkt[i])
			p.Ping[i] = percentile(acc.pingBkt[i], 0.5)
		}
		screen.Protocols = append(screen.Protocols, p)
	}
	sort.Slice(screen.Protocols, func(i, j int) bool { return screen.Protocols[i].SharePct > screen.Protocols[j].SharePct })

	for _, key := range []string{"none", "one", "some", "all"} {
		line := TesterSeries{Key: key, Values: make([]*float64, n)}
		for i := 0; i < n; i++ {
			if testers[i] > 0 {
				v := upGroups[key][i] / testers[i] * 100
				line.Values[i] = &v
			}
		}
		screen.ClientsByUp = append(screen.ClientsByUp, line)
	}
	for _, reason := range []string{RelayTCPBlocked, RelayTCPThinned, RelayProtocol, RelayTimeout, RelayError} {
		counts := failures[reason]
		if counts == nil {
			continue
		}
		line := TesterSeries{Key: reason, Values: make([]*float64, n)}
		for i := 0; i < n; i++ {
			v := counts[i]
			line.Values[i] = &v
		}
		screen.Failures = append(screen.Failures, line)
	}

	up := func(r TesterRelays) int {
		count := 0
		for _, relay := range r.Relays {
			if relay.Up {
				count++
			}
		}
		return count
	}
	sort.SliceStable(screen.Installs, func(i, j int) bool { return up(screen.Installs[i]) < up(screen.Installs[j]) })
	return screen, nil
}

// TesterRelaysHandler answers the protocols screen. Query: window (24h or 7d).
func TesterRelaysHandler(store TesterStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		window := r.URL.Query().Get("window")
		if window == "" {
			window = "24h"
		}
		if _, ok := testerRelayWindows[window]; !ok {
			writeErr(w, http.StatusBadRequest, "window is 24h or 7d")
			return
		}
		screen, err := buildTesterRelayScreen(r.Context(), store, window, time.Now())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to read tester relays")
			return
		}
		writeJSON(w, http.StatusOK, screen)
	}
}
