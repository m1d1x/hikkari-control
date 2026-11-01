// Command entry-prober measures the liveness of the plan-42 entry relays from
// an RU vantage and pushes the results, with a cause-diagnosis verdict, to
// nekko-server's /api/v1/internal/entry-probes endpoint, which writes them to
// the `entry_probes` ClickHouse table (plan 42 П6.6).
//
// It is the in-repo, CI-covered generalisation of the hand-written realitymon
// on the stand: instead of one hard-coded relay to a CSV, it asks the hub for
// the pool every round, brings up one Xray SOCKS outbound per relay, and runs
// the three signals of the measurement methodology (§6) against each —
//
//	reality — GET the gateway /healthz THROUGH the relay's Reality tunnel.
//	k1      — a bare TLS handshake straight to the relay IP with the cover SNI.
//	k2      — a bare TLS handshake to a known-allowed control site.
//
// — then classifies (clean | ip_banned | fingerprint | reality_wins |
// network_down). The verdict only RECORDS; retiring a dead relay stays a
// manual admin action until the thresholds are tuned on real relays (plan
// decision, 2026-10-17).
//
// Required env:
//
//	ENTRY_PROBE_TUNNEL_URL     what to fetch through the tunnel, e.g.
//	                           "https://api.nekkochat.com/healthz".
//	PROBE_INGEST_URL           the hub's prober routes, e.g.
//	                           "https://api.nekkochat.com/api/v1/internal/entry-probes":
//	                           batches are POSTed here, the pool is GET …/relays,
//	                           admission probes are …/tasks (internal/entry prober_api.go).
//	PROBE_INGEST_TOKEN         this observation point's own token, issued by the
//	                           hub for its slot (DeployProber plants it; for a
//	                           standalone VPS `cmd/server entry-vantage-token
//	                           <slot>`). The hub learns the slot from it — no
//	                           request names a region. It fetches dialling
//	                           credentials — keep it secret.
//
// Optional env:
//
//	ENTRY_PROBE_INTERVAL_SEC   seconds between rounds (default 60).
//	ENTRY_PROBE_TASK_POLL_SEC  seconds between asks for an admission probe
//	                           (default 5): a new box waits on it.
//	ENTRY_PROBE_REPROBE_POLL_SEC seconds between asks for a reprobe of the
//	                           second chance — burned domains, suspect machines
//	                           (default 600): the hub asks about each suspect at
//	                           most once per its probe interval (hours).
//	ENTRY_PROBE_K2_CONTROL     allowed-site control host:port (default
//	                           "www.samsung.com:443").
//	ENTRY_PROBE_K1_SNI         SNI for the K1 bare-TLS control instead of the
//	                           relay's cover. Unset in normal operation; set it
//	                           to a name the censor is known to cut (§8.2: apple)
//	                           to make the real DPI kill K1 while Reality rides
//	                           its own cover — the live reality_wins check.
//	ENTRY_PROBE_XRAY_BIN       path to the xray binary (default "xray").
//	ENTRY_PROBE_SOCKS_BASE     first local SOCKS port (default 10810); admission
//	                           probes use their own xray at base+100, benches
//	                           at base+200.
//	ENTRY_PROBE_FINGERPRINT    uTLS fingerprint profile (default "chrome").
//	ENTRY_PROBE_DIAL_TIMEOUT_MS per-signal timeout (default 8000).
//
// Admission probes (roadmap 42 П6.15): the hub provisioning a box queues a
// task; this prober claims it, probes those relays on a separate xray, pushes
// the rows marked detail "admission" and reports a verdict per relay. The box
// enters the pool only if every one is clean.
//
// Reprobes of the second chance (roadmap 42 §11, П9.11): the hub queues its
// burned domains and its suspect machines apart from the routine rounds. A
// domain is tried as SNI against a live XHTTP relay's address beside a bare
// handshake to it (reprobe.go, classifyReprobe); a machine's relays are probed
// like an admission, on an engine of their own, and not pushed to the
// dataset. Enough clean reads on separate days return a suspect to the pool;
// enough failures write it off — the hub's policy says how many.
//
// Benches (roadmap 42 П6.14 Ф3): before and after a new relay build goes on a
// box, the hub queues a bench; this prober drives messenger-shaped traffic
// through one relay of the box to the hub's bench routes for the asked
// duration and reports the summary (bench.go).
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/proxy"
)

type probeResult struct {
	TS           time.Time `json:"ts"`
	RelayName    string    `json:"relayName"`
	RawTCP       uint8     `json:"rawTcp"`
	RealityOK    uint8     `json:"realityOk"`
	RealityRTTMs float32   `json:"realityRttMs"`
	RealityHTTP  uint16    `json:"realityHttp"`
	K1BareTLS    uint8     `json:"k1BareTls"`
	K2Control    uint8     `json:"k2Control"`
	Cover        string    `json:"cover"`
	Verdict      string    `json:"verdict"`
	Detail       string    `json:"detail"`
	// The voice door of a stream relay that also carries calls (voice plan
	// Ф5): how many datagrams of the burst came back out of how many went in.
	// Its own pair of numbers and not a word in the verdict — see withXUDP.
	XUDPGot  uint16 `json:"xudpGot"`
	XUDPSent uint16 `json:"xudpSent"`
}

// detailAdmission marks the rows of an admission probe in the dataset, so a
// box's first verdict can be told from the routine rounds.
const detailAdmission = "admission"

// detailUpstreamSilent marks a row where the tunnel carried a connection and
// what is behind it did not answer (K3, П9.4). The relay did its job; it is
// our own upstream that was silent, and the verdict says `unknown` rather
// than blaming the protocol.
const detailUpstreamSilent = "k3: tunnel dialled, upstream silent"

// config is the prober's environment.
type config struct {
	tunnelURL   string
	hub         hubClient
	interval    time.Duration
	taskPoll    time.Duration
	reprobePoll time.Duration
	k2Control   string
	k1SNI       string
	xrayBin     string
	socksBase   int
	fingerprint string
	timeout     time.Duration
}

func main() {
	cfg := config{
		tunnelURL: mustEnv("ENTRY_PROBE_TUNNEL_URL"),
		hub: hubClient{
			base:  strings.TrimRight(mustEnv("PROBE_INGEST_URL"), "/"),
			token: mustEnv("PROBE_INGEST_TOKEN"),
			http:  &http.Client{Timeout: 10 * time.Second},
		},
		interval:    time.Duration(envInt("ENTRY_PROBE_INTERVAL_SEC", 60)) * time.Second,
		taskPoll:    time.Duration(envInt("ENTRY_PROBE_TASK_POLL_SEC", 5)) * time.Second,
		reprobePoll: time.Duration(envInt("ENTRY_PROBE_REPROBE_POLL_SEC", 600)) * time.Second,
		k2Control:   envStr("ENTRY_PROBE_K2_CONTROL", "www.samsung.com:443"),
		k1SNI:       envStr("ENTRY_PROBE_K1_SNI", ""),
		xrayBin:     envStr("ENTRY_PROBE_XRAY_BIN", "xray"),
		socksBase:   envInt("ENTRY_PROBE_SOCKS_BASE", 10810),
		fingerprint: envStr("ENTRY_PROBE_FINGERPRINT", "chrome"),
		timeout:     time.Duration(envInt("ENTRY_PROBE_DIAL_TIMEOUT_MS", 8000)) * time.Millisecond,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("entry-prober: hub=%s interval=%s xray=%s", cfg.hub.base, cfg.interval, cfg.xrayBin)

	// Admission probes run beside the routine rounds, on their own xray and
	// ports: a new box must not wait for the next round, nor share its engine.
	go cfg.serveTasks(ctx)
	// Benches of a new relay build (П6.14) on a third xray: minutes long, so
	// never in the way of the other two.
	go cfg.serveBenches(ctx)
	// The reprobes of the second chance (§11, П9.11): burned domains (no
	// xray, two handshakes per name) and suspect machines (an admission-like
	// round on an engine of their own), polled rarely — a suspect waits its
	// probe interval between asks anyway.
	go cfg.serveReprobes(ctx)
	cfg.watchPool(ctx)
	log.Println("entry-prober: shutdown")
}

// watchPool probes the pool every interval. The list comes from the hub each
// round; the xray engine is rebuilt only when the list changes.
//
// An empty pool is said out loud, once per spell. Until now it was the one
// outcome of a round that wrote nothing at all: the hub drains a box for a
// canary rollout, every relay leaves `alive`, and the vantage went quiet with
// a live process and an empty log — indistinguishable, from the outside, from
// a prober that had hung. The measuring eye has to report that it cannot see;
// silence is the one thing it may never mean.
func (cfg config) watchPool(ctx context.Context) {
	var eng *engine
	defer func() { eng.stop() }()
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
	// blind is the pool-is-empty spell we are in, so the line is printed on
	// the edges and not every interval.
	blind := false
	for {
		edges, err := cfg.hub.relays(ctx)
		if len(edges) > 0 && blind {
			log.Printf("entry-prober: pool has %d relay(s) again, probing resumes", len(edges))
			blind = false
		}
		switch {
		case err != nil:
			log.Printf("entry-prober: relay list: %v", err)
		case len(edges) == 0:
			if !blind {
				log.Printf("entry-prober: pool is empty — nothing to probe, the dataset gets no rows until a relay is alive again")
				blind = true
			}
			eng.stop()
			eng = nil
		default:
			if eng == nil || !eng.serves(edges) || eng.dead() {
				eng.stop()
				eng, err = startEngine(ctx, cfg.xrayBin, edges, cfg.socksBase, cfg.fingerprint)
				if err != nil {
					log.Printf("entry-prober: %v", err)
					eng = nil
					break
				}
			}
			batch := runRound(ctx, edges, eng, cfg.tunnelURL, cfg.k2Control, cfg.k1SNI, cfg.timeout)
			if err := cfg.hub.push(ctx, batch); err != nil {
				log.Printf("entry-prober: push failed: %v (%s)", err, verdictList(batch))
			} else {
				log.Printf("entry-prober: round pushed: %s", verdictList(batch))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// serveTasks claims the hub's admission probes and answers them.
func (cfg config) serveTasks(ctx context.Context) {
	ticker := time.NewTicker(cfg.taskPoll)
	defer ticker.Stop()
	for {
		task, err := cfg.hub.claim(ctx, "admission")
		if err != nil {
			log.Printf("entry-prober: claim task: %v", err)
		} else if task != nil {
			cfg.answer(ctx, task)
			continue // there may be more queued
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (cfg config) answer(ctx context.Context, task *probeTask) {
	verdicts, batch := cfg.probeOnce(ctx, task, cfg.socksBase+100)
	if len(batch) > 0 {
		for i := range batch {
			// The admission marker is what the dataset keys on; a voice
			// row's own detail (its burst count) is the lesser loss.
			batch[i].Detail = detailAdmission
		}
		if err := cfg.hub.push(ctx, batch); err != nil {
			log.Printf("entry-prober: task %s: push failed: %v", task.ID, err)
		}
	}
	if err := cfg.hub.finish(ctx, task.ID, verdicts); err != nil {
		log.Printf("entry-prober: task %s: report: %v", task.ID, err)
		return
	}
	log.Printf("entry-prober: task %s answered: %v", task.ID, verdicts)
}

// probeOnce runs one round over a task's relays, on an engine of its own at
// socksBase so it never shares one with another loop, and returns the verdict
// per relay with the batch it read them from. No engine is the vantage's
// failure, not the relays': every relay reads probe_failed, which the hub
// takes as nothing learned, rather than condemning a box never probed.
func (cfg config) probeOnce(ctx context.Context, task *probeTask, socksBase int) (map[string]string, []probeResult) {
	verdicts := map[string]string{}
	eng, err := startEngine(ctx, cfg.xrayBin, task.Relays, socksBase, cfg.fingerprint)
	if err != nil {
		log.Printf("entry-prober: task %s: %v", task.ID, err)
		for _, e := range task.Relays {
			verdicts[e.Name] = "probe_failed"
		}
		return verdicts, nil
	}
	batch := runRound(ctx, task.Relays, eng, cfg.tunnelURL, cfg.k2Control, cfg.k1SNI, cfg.timeout)
	eng.stop()
	for _, r := range batch {
		verdicts[r.RelayName] = r.Verdict
	}
	return verdicts, batch
}

func verdictList(batch []probeResult) string {
	out := make([]string, 0, len(batch))
	for _, r := range batch {
		out = append(out, r.RelayName+"="+r.Verdict)
	}
	return strings.Join(out, " ")
}

// runRound probes every relay once and returns the batch. Per-relay failures
// become rows with success flags 0 and a verdict, so a dead relay is a datum,
// not a gap.
func runRound(ctx context.Context, edges []edge, eng *engine, tunnelURL, k2Control, k1SNI string, timeout time.Duration) []probeResult {
	now := time.Now().UTC()
	// K2 is one control for the whole round — the vantage's path to an allowed
	// site is the same for every relay, so probe it once.
	k2 := probeTLS(k2Control, hostOf(k2Control), timeout)
	batch := make([]probeResult, 0, len(edges))
	for _, e := range edges {
		res := probeEdge(ctx, e, eng.ports[e.Name], tunnelURL, k1SNI, k2, now, timeout)
		if door, ok := eng.xudp[e.Name]; ok {
			res = withXUDP(res, probeVoiceTunnel(ctx, door, timeout))
		}
		batch = append(batch, res)
	}
	return batch
}

// probeEdge runs the three signals against one relay and classifies. K1 uses
// the relay's cover unless k1SNI overrides it.
func probeEdge(ctx context.Context, e edge, socksPort int, tunnelURL, k1SNI string, k2 bool, now time.Time, timeout time.Duration) probeResult {
	if e.voice() {
		return probeVoiceEdge(ctx, e, socksPort, k2, now, timeout)
	}
	if e.xhttp() {
		return probeXhttpEdge(ctx, e, socksPort, tunnelURL, k2, now, timeout)
	}
	addr := net.JoinHostPort(e.Address, strconv.Itoa(e.Port))
	cover := e.probeCover()
	if k1SNI == "" {
		k1SNI = cover
	}

	rawTCP := probeRawTCP(addr, timeout)
	k1 := probeTLS(addr, k1SNI, timeout)
	realityOK, dialled, rttMs, httpCode := probeReality(ctx, socksPort, tunnelURL, timeout)
	if accusesRelay(classify(realityOK, dialled, k1, k2)) {
		realityOK, dialled, rttMs, httpCode = retryTunnel(ctx, socksPort, tunnelURL, timeout)
	}

	res := probeResult{
		TS:           now,
		RelayName:    e.Name,
		RealityRTTMs: rttMs,
		RealityHTTP:  httpCode,
		Cover:        cover,
		Verdict:      classify(realityOK, dialled, k1, k2),
	}
	if !realityOK && dialled {
		res.Detail = detailUpstreamSilent
	}
	res = weighPath(res, addr)
	res.RawTCP = b2u8(rawTCP)
	res.RealityOK = b2u8(realityOK)
	res.K1BareTLS = b2u8(k1)
	res.K2Control = b2u8(k2)
	return res
}

// probeXhttpEdge is probeEdge for an XHTTP relay. The row reuses the
// dataset's columns: realityOk is "the tunnel carried /healthz", k1BareTls the
// handshake by name. The cover column stays empty: the domain is sealed like
// the address, and the dataset keeps neither.
func probeXhttpEdge(ctx context.Context, e edge, socksPort int, tunnelURL string, k2 bool, now time.Time, timeout time.Duration) probeResult {
	addr := net.JoinHostPort(e.Address, strconv.Itoa(e.Port))
	rawTCP := probeRawTCP(addr, timeout)
	named, certOK := probeTLSVerified(addr, e.Host, timeout)
	bare := probeTLS(addr, "", timeout)
	tunnelOK, dialled, rttMs, httpCode := probeReality(ctx, socksPort, tunnelURL, timeout)
	if accusesRelay(classifyXhttp(tunnelOK, dialled, named, bare, certOK, k2)) {
		tunnelOK, dialled, rttMs, httpCode = retryTunnel(ctx, socksPort, tunnelURL, timeout)
	}
	res := probeResult{
		TS:           now,
		RelayName:    e.Name,
		RealityRTTMs: rttMs,
		RealityHTTP:  httpCode,
		Verdict:      classifyXhttp(tunnelOK, dialled, named, bare, certOK, k2),
		Detail:       "xhttp",
	}
	if !tunnelOK && dialled {
		res.Detail = "xhttp: " + detailUpstreamSilent
	}
	if named && !certOK {
		res.Detail = "xhttp: certificate does not verify"
	}
	res = weighPath(res, addr)
	res.RawTCP = b2u8(rawTCP)
	res.RealityOK = b2u8(tunnelOK)
	res.K1BareTLS = b2u8(named)
	res.K2Control = b2u8(k2)
	return res
}

// probeTLSVerified is probeTLS with the certificate checked for sni against
// the system roots: the handshake completing and the certificate verifying are
// reported apart, since a relay whose site answers with a bad certificate is
// our failure, not the censor's.
func probeTLSVerified(addr, sni string, timeout time.Duration) (handshake, verified bool) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: sni})
	if err == nil {
		conn.Close()
		return true, true
	}
	var verr *tls.CertificateVerificationError
	if errors.As(err, &verr) {
		return true, false
	}
	return false, false
}

// The path from a vantage can lose some TCP flows and not others: seen from
// ru-spb on 01.11, a flow to a box either always got through or never did,
// decided by its source port, with its SYN never reaching the box and ping and
// other countries untouched. One check is one flow, so a dead flow reads as a
// fingerprint or a ban, and that verdict hands a healthy machine back. Before
// a verdict accuses the relay, the tunnel gets fresh flows, and the path is
// measured by many: flows that die while others pass are the path's fault, and
// say nothing about the relay.
const (
	tunnelRetries = 2
	pathFlows     = 12
	pathFlowWait  = 3 * time.Second
)

// accusesRelay is whether a verdict counts against the relay itself.
func accusesRelay(verdict string) bool {
	switch verdict {
	case verdictFingerprint, verdictIPBanned, verdictDomainBlocked:
		return true
	}
	return false
}

// retryTunnel tries the tunnel on fresh flows, and stops at the first answer.
func retryTunnel(ctx context.Context, socksPort int, tunnelURL string, timeout time.Duration) (ok, dialled bool, rttMs float32, httpCode uint16) {
	for i := 0; i < tunnelRetries && !ok; i++ {
		ok, dialled, rttMs, httpCode = probeReality(ctx, socksPort, tunnelURL, timeout)
	}
	return ok, dialled, rttMs, httpCode
}

// weighPath takes the accusation off a verdict reached over a lossy path.
func weighPath(res probeResult, addr string) probeResult {
	if !accusesRelay(res.Verdict) {
		return res
	}
	return settlePath(res, probeFlows(addr, pathFlows, pathFlowWait), pathFlows)
}

// settlePath reads the flow count. All through: the path is sound and the
// verdict stands. None through: the address is cut off, which is what a ban
// looks like. Some through: no evidence, which is not the same as clean.
func settlePath(res probeResult, through, flows int) probeResult {
	if through == 0 || through == flows {
		return res
	}
	note := fmt.Sprintf("path: %d/%d tcp flows through, was %s", through, flows, res.Verdict)
	if res.Detail != "" {
		note = res.Detail + "; " + note
	}
	res.Verdict, res.Detail = verdictUnknown, note
	return res
}

// probeFlows opens flows to addr at once, each from its own source port, and
// counts those that connect.
func probeFlows(addr string, flows int, timeout time.Duration) int {
	results := make(chan bool, flows)
	for i := 0; i < flows; i++ {
		go func() { results <- probeRawTCP(addr, timeout) }()
	}
	through := 0
	for i := 0; i < flows; i++ {
		if <-results {
			through++
		}
	}
	return through
}

// probeRawTCP reports whether a plain TCP connection to the relay opens.
func probeRawTCP(addr string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	return true
}

// probeTLS reports whether a bare TLS handshake to addr with the given SNI
// completes. Certificate validity is irrelevant — Reality borrows someone
// else's cert and we only care that the handshake is not reset — so the
// verification is skipped on purpose.
func probeTLS(addr, sni string, timeout time.Duration) bool {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, // handshake-reachability probe, not an auth check
	})
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// probeReality fetches tunnelURL through the relay's SOCKS outbound and reports
// success (tunnelAnswered), the round-trip time, and the status code seen.
func probeReality(ctx context.Context, socksPort int, tunnelURL string, timeout time.Duration) (ok, dialled bool, rtt float32, code uint16) {
	socksAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort))
	dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, &net.Dialer{Timeout: timeout})
	if err != nil {
		return false, false, 0, 0
	}
	// Did the tunnel carry a connection at all, apart from what came back
	// through it (K3, П9.4)? A relay whose handshake is refused and a relay
	// whose handshake works while our own gateway is silent look identical in
	// one boolean, and they are opposite findings: the first is a burnt
	// protocol, the second is not the relay's fault at all. Measured as its
	// own dial through the same SOCKS, so it costs one connection.
	dialled = tunnelDialled(ctx, dialer, tunnelURL, timeout)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if cd, ok := dialer.(proxy.ContextDialer); ok {
				return cd.DialContext(ctx, network, addr)
			}
			return dialer.Dial(network, addr)
		},
		TLSHandshakeTimeout: timeout,
	}
	client := &http.Client{Timeout: timeout, Transport: transport}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", tunnelURL, nil)
	if err != nil {
		return false, dialled, 0, 0
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return false, dialled, 0, 0
	}
	defer resp.Body.Close()
	rttMs := float32(time.Since(start).Microseconds()) / 1000.0
	return tunnelAnswered(resp.StatusCode), dialled, rttMs, uint16(resp.StatusCode)
}

// tunnelAnswered says the gateway's /healthz came back through the tunnel:
// any 2xx. Hikkari's hub answers 204 where Nekko's answered 200, and a prober
// that wanted 200 read every healthy relay as refused — `fingerprint` on a
// whole fresh fleet at admission (01.11).
func tunnelAnswered(code int) bool { return code/100 == 2 }

// tunnelDialled says the tunnel carried a connection to the upstream, and
// nothing about what the upstream then answered (K3, П9.4).
//
// # Why it is a TLS handshake and not a dial
//
// Its first version dialled the upstream through the SOCKS proxy and called a
// successful CONNECT the proof. It was not one: the SOCKS inbound that answers
// is our OWN local xray, and it answers the CONNECT before — and regardless of
// whether — the Reality outbound ever reaches the relay. So K3 read `true`
// whenever the prober's xray was running, which is always, and П9.7's first
// chaos run caught what that costs: the relay's unit was stopped for nine
// minutes, every other signal went to zero, and the verdict stayed `unknown`
// (k3 beats k1 in the matrix) — a word the detector reads as "no evidence" and
// never opens an incident for. The autopilot was structurally blind to a dead
// relay, and the one visible clue, this row's detail, was being overwritten by
// the voice probe on top of it.
//
// A completed TLS handshake cannot be faked by the local proxy: it needs bytes
// from the upstream's own key, so they travelled through the Reality handshake,
// through the relay and back. The certificate is deliberately NOT verified —
// whether the upstream's certificate is valid is the upstream's property, and
// blaming the relay for our own expired certificate is exactly the
// misattribution K3 exists to prevent.
func tunnelDialled(ctx context.Context, dialer proxy.Dialer, tunnelURL string, timeout time.Duration) bool {
	u, err := url.Parse(tunnelURL)
	if err != nil {
		return false
	}
	host := u.Host
	if u.Port() == "" {
		port := "443"
		if u.Scheme == "http" {
			port = "80"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var conn net.Conn
	if cd, ok := dialer.(proxy.ContextDialer); ok {
		conn, err = cd.DialContext(dctx, "tcp", host)
	} else {
		conn, err = dialer.Dial("tcp", host)
	}
	if err != nil {
		return false
	}
	defer conn.Close()
	if u.Scheme != "https" {
		// A plain-HTTP upstream gives nothing to handshake with, so the dial
		// is all there is to go on. The hub's own health URL is https
		// (scripts/edge/deploy-prober.sh renders it), and this branch is here
		// so a local run against a plain upstream is honest rather than
		// always-false.
		return true
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	tc := tls.Client(conn, &tls.Config{
		ServerName:         u.Hostname(),
		InsecureSkipVerify: true, //nolint:gosec // see the doc comment: the certificate is the upstream's property, not the relay's
		MinVersion:         tls.VersionTLS12,
	})
	return tc.HandshakeContext(dctx) == nil
}

// pushBatch POSTs the batch to the entry-probes ingest endpoint with the shared
// bearer token. An empty batch is still sent as {"probes":[]} (the endpoint
// returns 204); the caller always has at least one relay, so this is rare.
func pushBatch(ctx context.Context, client *http.Client, url, token string, batch []probeResult) error {
	body, err := json.Marshal(map[string]any{"probes": batch})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}
	return nil
}

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// hostOf strips the port from a host:port, for use as the TLS SNI.
func hostOf(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if strings.TrimSpace(v) == "" {
		log.Fatalf("entry-prober: %s is required", name)
	}
	return v
}

func envStr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("entry-prober: %s=%q is not an int, using %d", name, v, def)
	}
	return def
}
