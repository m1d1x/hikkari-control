package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/proxy"

	"github.com/m1d1x/hikkari-control/internal/entrybench"
)

// Benches of a relay build (roadmap 42 П6.14 Ф3). The hub queues one before
// and one after a new nekko-edge goes on a box; this vantage drives traffic
// shaped like our messenger through one relay of that box to the hub's bench
// routes, and reports a summary the rollout compares.
//
// Three flows at once, as a live client would have them:
//
//	chat  — a few hundred bytes each way every 1.5–4 s on a held connection;
//	        the RTT of each exchange.
//	voice — a 120-byte frame every 20 ms on a WebSocket the hub echoes; RTT,
//	        jitter, late and lost frames, stream breaks.
//	media — a 3 MB download and a 1 MB upload every 20–40 s; throughput.
//
// No bursts beyond what a photo is, no scanning, randomised pacing: the run
// must not itself look like circumvention to the censor watching the relay.

// benchShape is the rhythm of a run. A test shortens the gaps; the vantage
// runs messengerShape.
type benchShape struct {
	chatMinGap, chatMaxGap   time.Duration
	mediaFirst, mediaMaxGap  time.Duration // first transfer after [mediaFirst, 2×mediaFirst]
	mediaMinGap              time.Duration
	mediaDown, mediaUp       int
	voiceEvery               time.Duration
	chatTimeout, mediaTimout time.Duration
	// After the run, frames still in flight get this long to come back
	// before they count as lost.
	voiceDrain time.Duration
}

// messengerShape keeps a five-minute run near 40 MB: chat at human pace, a
// photo-sized transfer every half minute, voice frames every 20 ms.
var messengerShape = benchShape{
	chatMinGap: 1500 * time.Millisecond, chatMaxGap: 4 * time.Second,
	mediaFirst: 5 * time.Second, mediaMinGap: 20 * time.Second, mediaMaxGap: 40 * time.Second,
	mediaDown: 3 << 20, mediaUp: 1 << 20,
	voiceEvery:  20 * time.Millisecond,
	chatTimeout: 10 * time.Second, mediaTimout: 90 * time.Second,
	voiceDrain: 2 * time.Second,
}

// Sizes that do not change with the rhythm.
const (
	chatMinBody  = 200
	chatMaxBody  = 2000
	chatMinReply = 100
	chatMaxReply = 3000
	voiceFrame   = 120
)

type benchParams struct {
	DurationSec int `json:"durationSec"`
}

// serveBenches claims the hub's benches and runs them, one at a time, on its
// own xray: a bench takes minutes and must not delay an admission probe.
func (cfg config) serveBenches(ctx context.Context) {
	ticker := time.NewTicker(cfg.taskPoll)
	defer ticker.Stop()
	for {
		task, err := cfg.hub.claim(ctx, "bench")
		if err != nil {
			log.Printf("entry-prober: claim bench: %v", err)
		} else if task != nil {
			cfg.bench(ctx, task)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (cfg config) bench(ctx context.Context, task *probeTask) {
	var p benchParams
	_ = json.Unmarshal(task.Params, &p)
	d := time.Duration(p.DurationSec) * time.Second
	d = min(max(d, time.Minute), 10*time.Minute)
	names := make([]string, len(task.Relays))
	for i, r := range task.Relays {
		names[i] = r.Name
	}
	log.Printf("entry-prober: bench %s through %v for %s", task.ID, names, d)

	var res entrybench.Result
	eng, err := startEngine(ctx, cfg.xrayBin, task.Relays, cfg.socksBase+200, cfg.fingerprint)
	if err != nil {
		res.Error = "движок xray не поднялся: " + err.Error()
	} else {
		base, berr := benchBase(cfg.tunnelURL)
		if berr != nil {
			res.Error = berr.Error()
		} else {
			targets := make([]benchTarget, len(task.Relays))
			for i, r := range task.Relays {
				targets[i] = benchTarget{base: base, token: cfg.hub.token, dialer: socksDialer(eng.ports[r.Name], cfg.timeout)}
			}
			res = runBenches(ctx, targets, d, messengerShape)
		}
		eng.stop()
	}
	verdicts := map[string]string{}
	for i, name := range names {
		run := &res
		if i == 1 {
			run = res.Control
		}
		verdicts[name] = "benched"
		if run == nil || run.Error != "" {
			verdicts[name] = "bench_failed"
		}
	}
	if err := cfg.hub.finishBench(ctx, task.ID, verdicts, res); err != nil {
		log.Printf("entry-prober: bench %s: report: %v", task.ID, err)
		return
	}
	out, _ := json.Marshal(res)
	log.Printf("entry-prober: bench %s done: %s", task.ID, out)
}

// runBenches runs the bench through every target AT ONCE — the canary's relay
// and, when the hub named one, the control's — so both see the same minutes
// of the same path. The first is the result; the second rides as its Control.
func runBenches(ctx context.Context, targets []benchTarget, d time.Duration, sh benchShape) entrybench.Result {
	runs := make([]entrybench.Result, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runs[i] = runBench(ctx, t, d, sh)
		}()
	}
	wg.Wait()
	res := runs[0]
	if len(runs) > 1 {
		ctl := runs[1]
		res.Control = &ctl
	}
	return res
}

// benchBase is the hub as the tunnel reaches it: the scheme and host of the
// URL the routine probes fetch through every relay.
func benchBase(tunnelURL string) (string, error) {
	u, err := url.Parse(tunnelURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("ENTRY_PROBE_TUNNEL_URL %q names no host", tunnelURL)
	}
	return u.Scheme + "://" + u.Host + entrybench.Base, nil
}

func socksDialer(port int, timeout time.Duration) proxy.ContextDialer {
	d, _ := proxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil,
		&net.Dialer{Timeout: timeout})
	return d.(proxy.ContextDialer)
}

// benchTarget is where a run's traffic goes and how it gets there.
type benchTarget struct {
	base   string // https://<hub>/api/v1/internal/entry-bench
	token  string
	dialer proxy.ContextDialer // through the relay; a plain dialer in tests
}

func (t benchTarget) client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         t.dialer.DialContext,
			TLSHandshakeTimeout: timeout,
			MaxIdleConnsPerHost: 1,
		},
	}
}

func (t benchTarget) do(ctx context.Context, c *http.Client, method, path string, body []byte) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return n, err
	}
	if resp.StatusCode != http.StatusOK {
		return n, fmt.Errorf("%s %s: %d", method, path, resp.StatusCode)
	}
	return n, nil
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func between(lo, hi time.Duration) time.Duration {
	return lo + time.Duration(mrand.Int64N(int64(hi-lo)+1))
}

func betweenInt(lo, hi int) int { return lo + mrand.IntN(hi-lo+1) }

// runBench drives the three flows for d and summarises them.
func runBench(ctx context.Context, t benchTarget, d time.Duration, sh benchShape) entrybench.Result {
	start := time.Now()
	// The tunnel must carry one exchange before the run is a measurement of
	// the build; otherwise it is a measurement of nothing.
	chat := t.client(sh.chatTimeout)
	var warm error
	for range 3 {
		if _, warm = t.do(ctx, chat, http.MethodPost, "/echo?reply=100", randomBytes(100)); warm == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if warm != nil {
		return entrybench.Result{Error: "туннель не провёз ни одного обмена: " + warm.Error()}
	}

	runCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		res   entrybench.Result
		chats []float64
		downs []float64
		ups   []float64
	)
	wg.Add(3)
	go func() { // chat
		defer wg.Done()
		for sleep(runCtx, between(sh.chatMinGap, sh.chatMaxGap)) {
			path := "/echo?reply=" + strconv.Itoa(betweenInt(chatMinReply, chatMaxReply))
			body := randomBytes(betweenInt(chatMinBody, chatMaxBody))
			at := time.Now()
			_, err := t.do(ctx, chat, http.MethodPost, path, body)
			mu.Lock()
			res.ChatN++
			if err != nil {
				res.ChatErrors++
			} else {
				chats = append(chats, ms(time.Since(at)))
			}
			mu.Unlock()
		}
	}()
	go func() { // media
		defer wg.Done()
		media := t.client(sh.mediaTimout)
		gap := between(sh.mediaFirst, 2*sh.mediaFirst)
		for sleep(runCtx, gap) {
			gap = between(sh.mediaMinGap, sh.mediaMaxGap)
			at := time.Now()
			n, err := t.do(ctx, media, http.MethodGet, "/blob?size="+strconv.Itoa(sh.mediaDown), nil)
			took := time.Since(at)
			mu.Lock()
			res.MediaN++
			res.BytesDown += n
			if err != nil {
				res.MediaErrors++
			} else {
				downs = append(downs, mbps(n, took))
			}
			mu.Unlock()
			at = time.Now()
			_, err = t.do(ctx, media, http.MethodPost, "/sink", randomBytes(sh.mediaUp))
			took = time.Since(at)
			mu.Lock()
			res.MediaN++
			if err != nil {
				res.MediaErrors++
			} else {
				res.BytesUp += int64(sh.mediaUp)
				ups = append(ups, mbps(int64(sh.mediaUp), took))
			}
			mu.Unlock()
		}
	}()
	var voice voiceStats
	go func() {
		defer wg.Done()
		voice = runVoice(runCtx, t, sh)
	}()
	wg.Wait()

	res.DurationS = time.Since(start).Seconds()
	res.ChatRTTP50Ms, res.ChatRTTP95Ms = percentile(chats, 0.5), percentile(chats, 0.95)
	res.DownMbps, res.UpMbps = percentile(downs, 0.5), percentile(ups, 0.5)
	res.VoiceSent, res.VoiceLost, res.VoiceLate = voice.sent, voice.sent-voice.received, voice.late
	res.VoiceRTTP50Ms, res.VoiceJitterMs, res.VoiceDrops = percentile(voice.rtts, 0.5), voice.jitter(), voice.drops
	res.Drops = res.ChatErrors + res.MediaErrors + res.VoiceDrops
	return res
}

// sleep waits d, false when the run ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return ctx.Err() == nil
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func mbps(n int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n*8) / d.Seconds() / 1e6
}

// percentile of xs by nearest rank; 0 for none.
func percentile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	i := int(q*float64(len(s))+0.5) - 1
	return s[min(max(i, 0), len(s)-1)]
}

type voiceStats struct {
	sent, received, late, drops int
	rtts                        []float64
}

func (v voiceStats) jitter() float64 {
	if len(v.rtts) < 2 {
		return 0
	}
	var sum float64
	for i := 1; i < len(v.rtts); i++ {
		d := v.rtts[i] - v.rtts[i-1]
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return sum / float64(len(v.rtts)-1)
}

// runVoice keeps a frame-every-20-ms stream up until ctx ends, reopening it
// when it breaks (each break counted). A frame carries its sequence number and
// send time; the hub echoes it, so the RTT is read off the echo itself.
func runVoice(ctx context.Context, t benchTarget, sh benchShape) voiceStats {
	var st voiceStats
	wsURL := "wss" + t.base[len("https"):] + "/stream"
	if len(t.base) >= 5 && t.base[:5] == "http:" {
		wsURL = "ws" + t.base[len("http"):] + "/stream"
	}
	dialer := websocket.Dialer{NetDialContext: t.dialer.DialContext, HandshakeTimeout: sh.chatTimeout}
	header := http.Header{"Authorization": {"Bearer " + t.token}}
	for ctx.Err() == nil {
		conn, _, err := dialer.DialContext(ctx, wsURL, header)
		if err != nil {
			if ctx.Err() == nil {
				st.drops++
				sleep(ctx, time.Second)
			}
			continue
		}
		broke := voiceSession(ctx, conn, &st, sh)
		conn.Close()
		if broke {
			st.drops++
			sleep(ctx, time.Second)
		}
	}
	return st
}

// voiceSession runs one stream until ctx ends (false) or it breaks (true).
func voiceSession(ctx context.Context, conn *websocket.Conn, st *voiceStats, sh benchShape) bool {
	var mu sync.Mutex
	readDone := make(chan bool, 1)
	go func() {
		for {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				readDone <- true
				return
			}
			if len(frame) < 12 {
				continue
			}
			sent := time.Unix(0, int64(binary.BigEndian.Uint64(frame[4:12])))
			rtt := ms(time.Since(sent))
			mu.Lock()
			st.received++
			st.rtts = append(st.rtts, rtt)
			if rtt > entrybench.VoiceLateMs {
				st.late++
			}
			mu.Unlock()
		}
	}()
	tick := time.NewTicker(sh.voiceEvery)
	defer tick.Stop()
	frame := make([]byte, voiceFrame)
	_, _ = rand.Read(frame[12:])
	var seq uint32
	for {
		select {
		case <-ctx.Done():
			// Let the frames in flight come back, then stop reading.
			time.Sleep(sh.voiceDrain)
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			conn.Close()
			<-readDone
			return false
		case <-readDone:
			return true
		case <-tick.C:
			seq++
			binary.BigEndian.PutUint32(frame[0:4], seq)
			binary.BigEndian.PutUint64(frame[4:12], uint64(time.Now().UnixNano()))
			_ = conn.SetWriteDeadline(time.Now().Add(sh.chatTimeout))
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return true
			}
			mu.Lock()
			st.sent++
			mu.Unlock()
		}
	}
}
