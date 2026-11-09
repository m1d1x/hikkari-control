package stand

// The cascade watch's protocol layer (internal/cascadewatch; hikkari-client
// plans/2026-11-05-ru-cascade.md): a task of its own kind, CascadeTaskKind,
// queued by the watch every round for each observation point. Its links are
// not the fleet's but the exit's Hikkari relay twice, straight and through the
// trunk entry (subscription.CascadeProbeLinks), and on each the point runs
// what a tester's diag:load runs on the core users run (1 MiB, 50 at once, 3
// after: the burst knob at 50, one round) and then a call: 30 s of
// voice-sized STUN requests through the profile, as diag:voice sends them.
// Rows go to the relay dataset like any stand row; the round's rows and calls
// go to the watch too, which judges the round.

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CascadeTaskKind is the knob column of a cascade watch task.
const CascadeTaskKind = "cascade-v1"

// CascadeSource is the watch as the stand sees it (cascadewatch.Watch).
type CascadeSource interface {
	// CascadeLinks are the links a cascade task of entryBox measures.
	CascadeLinks(ctx context.Context, entryBox string) ([]string, error)
	// CascadeFinished takes a cascade task's result: the point it ran on,
	// the entry and round it was queued for.
	CascadeFinished(ctx context.Context, slot, entryBox string, round time.Time, res Result) error
}

// SetCascade gives the scheduler the watch.
func (s *Scheduler) SetCascade(c CascadeSource) { s.cascade = c }

// QueueCascade queues a cascade task of entryBox for round on each of slots,
// unless the slot still has one of that entry not done: a point that falls
// behind skips rounds rather than piling them up.
func (s *Scheduler) QueueCascade(ctx context.Context, entryBox string, round time.Time, slots []string) (int, error) {
	queued := 0
	for _, slot := range slots {
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO stand_tasks (slot, knob, vals, base, rounds, baseline_path, cascade_entry, cascade_round)
			SELECT $1, $2, ARRAY['50'], '', 1, '/', $3, $4
			WHERE NOT EXISTS (SELECT 1 FROM stand_tasks WHERE slot = $1 AND knob = $2 AND cascade_entry = $3
			                  AND status IN ('queued', 'claimed'))`,
			slot, CascadeTaskKind, entryBox, round)
		if err != nil {
			return queued, fmt.Errorf("queue cascade task: %w", err)
		}
		queued += int(tag.RowsAffected())
	}
	return queued, nil
}

func (s *Scheduler) cascadeTask(ctx context.Context, id uuid.UUID) (entryBox string, round time.Time, err error) {
	err = s.pool.QueryRow(ctx, `SELECT coalesce(cascade_entry, ''), coalesce(cascade_round, created_at) FROM stand_tasks WHERE id = $1`, id).
		Scan(&entryBox, &round)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, ErrNoSuchTask
	}
	return entryBox, round, err
}

// CascadeVoice is one call through one profile of a cascade task: what
// diag:voice says of a call, measured on a point.
type CascadeVoice struct {
	Profile string `json:"profile"`
	// Sent and Got count the requests and their answers.
	Sent int `json:"sent"`
	Got  int `json:"got"`
	// LossRun is the longest run of requests in a row with no answer.
	LossRun int `json:"loss_run"`
	// Gaps60 and Gaps150 count pauses between answers longer than 60 and
	// 150 ms, the ones a call hears; GapMax is the longest.
	Gaps60  int     `json:"gaps60"`
	Gaps150 int     `json:"gaps150"`
	GapMax  float64 `json:"gap_max_ms"`
	RTTp50  float64 `json:"rtt_p50_ms"`
	Jitter  float64 `json:"jitter_ms"`
	Error   string  `json:"error,omitempty"`
}

const (
	cascadeVoiceTarget   = "stun.cloudflare.com:3478"
	cascadeVoicePackets  = 1500 // 30 s
	cascadeVoiceInterval = 20 * time.Millisecond
	cascadeVoicePayload  = 160
	cascadeVoiceWarmup   = 8 * time.Second
	cascadeVoiceTail     = 2 * time.Second
)

// runCascade measures every profile of the task's links: the load of
// diag:load, then a call.
func (r Runner) runCascade(ctx context.Context, t Task, profiles []Profile) Result {
	res := Result{Core: r.Kit.Label}
	var errs []string
	for _, p := range profiles {
		plan, err := NewPlan(p.Config, Spec{Profile: p.Label, Knob: LoadBurst, Values: []string{strconv.Itoa(Burst)}, BasePath: t.BaselinePath, Rounds: 1})
		if err != nil {
			errs = append(errs, p.Label+": "+err.Error())
			continue
		}
		plan.Core = r.Kit.Label
		rows, err := r.runPlan(ctx, plan)
		res.Rows = append(res.Rows, rows...)
		if err != nil {
			errs = append(errs, p.Label+": "+err.Error())
		}
		v := r.cascadeVoice(ctx, p)
		res.Voice = append(res.Voice, v)
		r.log("stand: cascade %s: %d rows, call %d/%d gaps60=%d", p.Label, len(rows), v.Got, v.Sent, v.Gaps60)
		if ctx.Err() != nil {
			break
		}
	}
	if len(profiles) == 0 {
		errs = append(errs, "в задаче каскада нет профилей")
	}
	res.Error = joinErrs(errs)
	return res
}

func joinErrs(errs []string) string {
	out := ""
	for i, e := range errs {
		if i > 0 {
			out += "; "
		}
		out += e
	}
	return out
}

// cascadeVoice runs the core with the profile behind a local UDP door to a
// STUN server and makes a 30 s call through it.
func (r Runner) cascadeVoice(ctx context.Context, p Profile) CascadeVoice {
	v := CascadeVoice{Profile: p.Label}
	door, stop, err := startVoiceDoor(ctx, r.Kit.Core, p.Config)
	if err != nil {
		v.Error = err.Error()
		return v
	}
	defer stop()
	call(ctx, door, &v)
	return v
}

// startVoiceDoor is the core with the profile's outbound behind a direct UDP
// inbound forwarded to the STUN server; a mixed inbound says when it is up.
func startVoiceDoor(ctx context.Context, core string, outbound map[string]any) (int, func(), error) {
	ob, err := clone(outbound)
	if err != nil {
		return 0, nil, err
	}
	for _, k := range []string{"domain_resolver", "detour", "bind_interface", "routing_mark", "flow_retry_delay"} {
		delete(ob, k)
	}
	ob["tag"] = "probe"
	host, port, err := net.SplitHostPort(cascadeVoiceTarget)
	if err != nil {
		return 0, nil, err
	}
	tp, _ := strconv.Atoi(port)
	// The door hands the core an address, not a name: the Hikkari outbound
	// carries datagrams to addresses only (a name is "unsupported address"
	// and the call reads as lost), as a client's datagrams through its tun
	// always are.
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		return 0, nil, fmt.Errorf("resolve %s: %v", host, err)
	}
	host = ips[0].String()
	ready, err := freePort("tcp")
	if err != nil {
		return 0, nil, err
	}
	door, err := freePort("udp")
	if err != nil {
		return 0, nil, err
	}
	dir, err := os.MkdirTemp("", "hikkari-cascade-voice-")
	if err != nil {
		return 0, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	raw, err := json.Marshal(map[string]any{
		"log": map[string]any{"level": "error"},
		"inbounds": []any{
			map[string]any{"type": "mixed", "tag": "ready", "listen": "127.0.0.1", "listen_port": ready},
			map[string]any{"type": "direct", "tag": "door", "listen": "127.0.0.1", "listen_port": door, "network": "udp",
				"override_address": host, "override_port": tp},
		},
		"outbounds": []any{ob},
		"route":     map[string]any{"final": "probe"},
	})
	if err != nil {
		cleanup()
		return 0, nil, err
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		cleanup()
		return 0, nil, err
	}
	pctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(pctx, core, "run", "-c", path)
	cmd.Dir = filepath.Dir(core) // libcronet.so beside the core
	if err := cmd.Start(); err != nil {
		cancel()
		cleanup()
		return 0, nil, errors.New("core did not start")
	}
	stop := func() { cancel(); _ = cmd.Wait(); cleanup() }
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && pctx.Err() == nil; time.Sleep(50 * time.Millisecond) {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ready)), 100*time.Millisecond); err == nil {
			c.Close()
			return door, stop, nil
		}
	}
	stop()
	return 0, nil, errors.New("core not ready")
}

func freePort(network string) (int, error) {
	if network == "udp" {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return 0, err
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port, nil
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// call sends cascadeVoicePackets binding requests every 20 ms through the
// door after one answered warm-up request, and reads what a call hears from
// the answers.
func call(ctx context.Context, door int, v *CascadeVoice) {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: door})
	if err != nil {
		v.Error = err.Error()
		return
	}
	defer conn.Close()
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	buf := make([]byte, 2048)
	warmed := false
	for deadline := time.Now().Add(cascadeVoiceWarmup); !warmed && time.Now().Before(deadline) && ctx.Err() == nil; {
		_, _ = conn.Write(stunBinding(1<<31, nonce))
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if n, err := conn.Read(buf); err == nil {
			_, warmed = stunAnswer(buf[:n], nonce)
		}
	}
	v.Sent = cascadeVoicePackets
	if !warmed {
		v.Error = "no answer to the warm-up"
		v.LossRun = cascadeVoicePackets
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	type answer struct {
		seq uint32
		at  time.Time
	}
	var mu sync.Mutex
	sentAt := make([]time.Time, cascadeVoicePackets)
	var answers []answer
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 2048)
		for {
			n, err := conn.Read(b)
			if err != nil {
				return
			}
			seq, ok := stunAnswer(b[:n], nonce)
			if !ok || seq >= cascadeVoicePackets {
				continue
			}
			mu.Lock()
			answers = append(answers, answer{seq, time.Now()})
			mu.Unlock()
		}
	}()
	tick := time.NewTicker(cascadeVoiceInterval)
	for seq := uint32(0); seq < cascadeVoicePackets; seq++ {
		mu.Lock()
		sentAt[seq] = time.Now()
		mu.Unlock()
		_, _ = conn.Write(stunBinding(seq, nonce))
		select {
		case <-ctx.Done():
			tick.Stop()
			v.Error = "cancelled"
			return
		case <-tick.C:
		}
	}
	tick.Stop()
	_ = conn.SetReadDeadline(time.Now().Add(cascadeVoiceTail))
	<-done
	mu.Lock()
	defer mu.Unlock()
	callStats(v, sentAt, func(yield func(uint32, time.Time) bool) {
		for _, a := range answers {
			if !yield(a.seq, a.at) {
				return
			}
		}
	})
}

// callStats reads a call from its answers: each seq counted once, gaps
// between answers in the order they came, round trips against sentAt, and
// RFC 3550 jitter over the transit times.
func callStats(v *CascadeVoice, sentAt []time.Time, answers func(func(uint32, time.Time) bool)) {
	seen := make([]bool, len(sentAt))
	var rtts []float64
	var last time.Time
	var jitter, prevTransit float64
	first := true
	answers(func(seq uint32, at time.Time) bool {
		if int(seq) >= len(seen) || seen[seq] {
			return true
		}
		seen[seq] = true
		v.Got++
		rtt := float64(at.Sub(sentAt[seq]).Microseconds()) / 1000
		rtts = append(rtts, rtt)
		if !last.IsZero() {
			gap := float64(at.Sub(last).Microseconds()) / 1000
			if gap > 60 {
				v.Gaps60++
			}
			if gap > 150 {
				v.Gaps150++
			}
			if gap > v.GapMax {
				v.GapMax = gap
			}
		}
		last = at
		if !first {
			d := rtt - prevTransit
			if d < 0 {
				d = -d
			}
			jitter += (d - jitter) / 16
		}
		prevTransit, first = rtt, false
		return true
	})
	run := 0
	for _, ok := range seen {
		if ok {
			run = 0
			continue
		}
		if run++; run > v.LossRun {
			v.LossRun = run
		}
	}
	if len(rtts) > 0 {
		sort.Float64s(rtts)
		v.RTTp50 = rtts[len(rtts)/2]
	}
	v.Jitter = float64(int(jitter*10)) / 10
}

const stunCookie = 0x2112A442

// stunBinding is a binding request of cascadeVoicePayload bytes, seq in its
// transaction id, padded by a PADDING attribute to an Opus frame's size.
func stunBinding(seq uint32, nonce [8]byte) []byte {
	b := make([]byte, cascadeVoicePayload)
	binary.BigEndian.PutUint16(b[0:], 0x0001)
	binary.BigEndian.PutUint16(b[2:], uint16(cascadeVoicePayload-20))
	binary.BigEndian.PutUint32(b[4:], stunCookie)
	copy(b[8:16], nonce[:])
	binary.BigEndian.PutUint32(b[16:], seq)
	binary.BigEndian.PutUint16(b[20:], 0x0026)
	binary.BigEndian.PutUint16(b[22:], uint16(cascadeVoicePayload-24))
	return b
}

func stunAnswer(b []byte, nonce [8]byte) (uint32, bool) {
	if len(b) < 20 || binary.BigEndian.Uint32(b[4:]) != stunCookie || string(b[8:16]) != string(nonce[:]) {
		return 0, false
	}
	return binary.BigEndian.Uint32(b[16:]), true
}
