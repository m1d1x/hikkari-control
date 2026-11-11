package main

// wirestand_load.go is the stand's synthetic users over a wire: what
// cascade-load-v1 puts through the entry (a call of 50 frames a second of
// 160 B and a game of 200 a second of 80 B per user), generated inside the
// stand's entry process and sent over its wire to the exit's echo. It prices
// a wire in CPU per user datagram (hikkari-client plans/2026-11-11-entry-hot-
// path-and-unit-economics.md, «Что бенч не мерил»). Synthetic only.
//
// WIRESTAND_LOAD is "<unix>:<step>,<step>,...", the steps one after another
// from that second. A step is <mode><users>x<minutes>, or 0x<minutes>:
//
//   - u: every user its own conversation, every frame its own write (as the
//     entry forwards a datagram today);
//   - g: glued: the frames of all users due within loadGlueEvery go out
//     together in one write of up to standGlueMax bytes, one conversation;
//   - n: no wire: the same frames to an echo inside the process, the price
//     of the generator and its books, to subtract;
//   - 0: nothing (users 0).
//
// A write carries one or more frames, each u16 length + frame; a frame is
// flow u16, kind u8, 0, seq u32, sent unix µs u64, padding to its size. The
// echo sends a write back as it came. Every minute of the wall clock inside
// a step, one line: load wire= from= mode= users= sent= got= writes= ...

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	loadVoiceEvery = 20 * time.Millisecond
	loadVoiceSize  = 160
	loadGameEvery  = 5 * time.Millisecond
	loadGameSize   = 80
	loadTick       = time.Millisecond
	// loadGlueEvery is how long glued frames wait for company: 2 ms, the
	// plan's 1–2 ms at its upper end (few users fill little in 1 ms).
	loadGlueEvery = 2 * time.Millisecond
	loadHeader    = 16
	// standGlueMax is the most one glued write holds: a full QUIC packet's
	// room on h3; the datagram wires keep a margin under their channel's
	// own limit.
	standGlueMax     = 1200
	standGlueMaxDgrm = 1100
	loadGrace        = 2 * time.Second
)

// loadStep is one step of the plan.
type loadStep struct {
	Mode  byte // 'u', 'g', 'n', '0'
	Users int
	For   time.Duration
}

// loadPlan is WIRESTAND_LOAD.
type loadPlan struct {
	From  time.Time
	Steps []loadStep
}

func (p loadPlan) Until() time.Time {
	t := p.From
	for _, s := range p.Steps {
		t = t.Add(s.For)
	}
	return t
}

func parseLoadPlan(s string) (loadPlan, error) {
	var p loadPlan
	at, steps, ok := strings.Cut(s, ":")
	from, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil {
		return p, fmt.Errorf("WIRESTAND_LOAD: %q is not <unix>:<steps>", s)
	}
	p.From = time.Unix(from, 0)
	for _, part := range strings.Split(steps, ",") {
		bad := fmt.Errorf("WIRESTAND_LOAD: step %q is not <u|g|n><users 1–16>x<minutes 1–120> or 0x<minutes>", part)
		if len(part) < 3 || !strings.ContainsRune("ugn0", rune(part[0])) {
			return p, bad
		}
		u, m, ok := strings.Cut(part[1:], "x")
		users, err1 := strconv.Atoi(u)
		if part[0] == '0' {
			users, err1 = 0, nil
			if u != "" {
				return p, bad
			}
		}
		minutes, err2 := strconv.Atoi(m)
		if !ok || err1 != nil || err2 != nil || minutes < 1 || minutes > 120 || (part[0] != '0' && (users < 1 || users > 16)) {
			return p, bad
		}
		p.Steps = append(p.Steps, loadStep{Mode: part[0], Users: users, For: time.Duration(minutes) * time.Minute})
	}
	return p, nil
}

// runStandLoad runs the plan over the wire.
func runStandLoad(ctx context.Context, wire string, w standWire, p loadPlan) {
	at := p.From
	for _, s := range p.Steps {
		end := at.Add(s.For)
		if time.Now().Before(end) && s.Mode != '0' {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(at)):
			}
			standOut.Print(standLog + fmt.Sprintf("load wire=%s step %c%d until %s", wire, s.Mode, s.Users, end.UTC().Format("15:04:05")))
			runLoadStep(ctx, wire, w, s, end)
		}
		at = end
	}
}

// loadBook keeps one step's minutes.
type loadBook struct {
	mu      sync.Mutex
	minutes map[int64]*loadMinute
	// last answer of each voice flow, for its pauses.
	lastVoice map[uint16]time.Time
}

type loadMinute struct {
	sent, got         [2]int
	writes, backs     int
	bytesUp           int
	rtt               [2][]time.Duration
	gaps60, gaps150   int
	gapMax            time.Duration
	writeErrs, frames int
}

func (b *loadBook) minute(t time.Time) *loadMinute {
	k := t.Truncate(time.Minute).Unix()
	m := b.minutes[k]
	if m == nil {
		m = &loadMinute{}
		b.minutes[k] = m
	}
	return m
}

// answer books one write that came back.
func (b *loadBook) answer(p []byte, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.minute(now).backs++
	for len(p) >= 2 {
		n := int(binary.BigEndian.Uint16(p))
		if n < loadHeader || len(p) < 2+n {
			return
		}
		f := p[2 : 2+n]
		p = p[2+n:]
		flow, kind := binary.BigEndian.Uint16(f), f[2]&1
		sent := time.UnixMicro(int64(binary.BigEndian.Uint64(f[8:])))
		sm := b.minutes[sent.Truncate(time.Minute).Unix()]
		if sm == nil {
			continue // its minute was said: late, counted lost
		}
		sm.got[kind]++
		sm.rtt[kind] = append(sm.rtt[kind], now.Sub(sent))
		if kind == 0 {
			if last, ok := b.lastVoice[flow]; ok {
				gap := now.Sub(last)
				am := b.minute(now)
				if gap > standGapHeard {
					am.gaps60++
					if gap > standGapWord {
						am.gaps150++
					}
				}
				if gap > am.gapMax {
					am.gapMax = gap
				}
			}
			b.lastVoice[flow] = now
		}
	}
}

// due takes the minutes whose grace is over (or every one, at the end).
func (b *loadBook) due(now time.Time, all bool) map[int64]*loadMinute {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[int64]*loadMinute{}
	for k, m := range b.minutes {
		if all || now.Sub(time.Unix(k, 0).Add(time.Minute)) >= loadGrace {
			out[k] = m
			delete(b.minutes, k)
		}
	}
	return out
}

func loadLine(wire string, s loadStep, from int64, m *loadMinute, glueMax int) string {
	q := func(r []time.Duration, pct int) string {
		if len(r) == 0 {
			return "-"
		}
		sort.Slice(r, func(i, j int) bool { return r[i] < r[j] })
		return strconv.FormatFloat(float64(standQuantile(r, pct).Microseconds())/1000, 'f', 1, 64)
	}
	return fmt.Sprintf("load wire=%s from=%d mode=%c users=%d glue_max=%d writes=%d backs=%d frames=%d bytes_up=%d voice_sent=%d voice_got=%d game_sent=%d game_got=%d voice_rtt_p50=%s voice_rtt_p99=%s game_rtt_p50=%s gaps60=%d gaps150=%d gap_max=%d write_errs=%d",
		wire, from, s.Mode, s.Users, glueMax, m.writes, m.backs, m.frames, m.bytesUp, m.sent[0], m.got[0], m.sent[1], m.got[1],
		q(m.rtt[0], 50), q(m.rtt[0], 99), q(m.rtt[1], 50), m.gaps60, m.gaps150, m.gapMax.Milliseconds(), m.writeErrs)
}

// runLoadStep runs one step until end.
func runLoadStep(ctx context.Context, wire string, w standWire, s loadStep, end time.Time) {
	ctx, cancel := context.WithDeadline(ctx, end)
	defer cancel()
	book := &loadBook{minutes: map[int64]*loadMinute{}, lastVoice: map[uint16]time.Time{}}
	glueMax := 0
	if s.Mode == 'g' {
		glueMax = standGlueMaxDgrm
		if wire == wireH3 {
			glueMax = standGlueMax
		}
	}
	// Conversations: one per user, or one for all when glued; n's is an
	// echo in the process.
	nconv := s.Users
	if s.Mode == 'g' {
		nconv = 1
	}
	convs := make([]net.Conn, nconv)
	for i := range convs {
		var c net.Conn
		var err error
		if s.Mode == 'n' {
			c = newLoopConn()
		} else {
			dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
			c, err = w.call(dctx)
			dcancel()
		}
		if err != nil {
			standOut.Print(standLog + fmt.Sprintf("load wire=%s: conversation: %v", wire, err))
			for _, c := range convs[:i] {
				c.Close()
			}
			return
		}
		convs[i] = c
		go func(c net.Conn) {
			buf := make([]byte, 4096)
			for {
				n, err := c.Read(buf)
				if err != nil {
					return
				}
				book.answer(buf[:n], time.Now())
			}
		}(c)
	}
	defer func() {
		for _, c := range convs {
			c.Close()
		}
	}()
	say := func(all bool) {
		due := book.due(time.Now(), all)
		keys := make([]int64, 0, len(due))
		for k := range due {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, k := range keys {
			standOut.Print(standLog + loadLine(wire, s, k, due[k], glueMax))
		}
	}
	defer say(true)

	// Flows: 2 per user, voice and game, each user's phase apart.
	type flow struct {
		id    uint16
		kind  byte
		size  int
		every time.Duration
		next  time.Time
		seq   uint32
		conv  int
	}
	start := time.Now()
	var flows []*flow
	for u := 0; u < s.Users; u++ {
		conv := u
		if s.Mode == 'g' {
			conv = 0
		}
		phase := time.Duration(u) * 1300 * time.Microsecond
		flows = append(flows,
			&flow{id: uint16(2 * u), kind: 0, size: loadVoiceSize, every: loadVoiceEvery, next: start.Add(phase), conv: conv},
			&flow{id: uint16(2*u + 1), kind: 1, size: loadGameSize, every: loadGameEvery, next: start.Add(phase + 700*time.Microsecond), conv: conv})
	}
	frame := make([]byte, standGlueMax+loadVoiceSize+2)
	glued := make([]byte, 0, standGlueMax+loadVoiceSize+2)
	write := func(conv int, p []byte, frames int, now time.Time) {
		_, err := convs[conv].Write(p)
		book.mu.Lock()
		m := book.minute(now)
		m.writes++
		m.frames += frames
		m.bytesUp += len(p)
		if err != nil {
			m.writeErrs++
		}
		book.mu.Unlock()
	}
	t := time.NewTicker(loadTick)
	defer t.Stop()
	lastSay := time.Now()
	lastGlue := time.Now()
	glued, gluedFrames := glued[:0], 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		for _, f := range flows {
			for !f.next.After(now) {
				f.seq++
				p := frame[:2+f.size]
				binary.BigEndian.PutUint16(p, uint16(f.size))
				binary.BigEndian.PutUint16(p[2:], f.id)
				p[4], p[5] = f.kind, 0
				binary.BigEndian.PutUint32(p[6:], f.seq)
				binary.BigEndian.PutUint64(p[10:], uint64(now.UnixMicro()))
				book.mu.Lock()
				book.minute(now).sent[f.kind]++
				book.mu.Unlock()
				if s.Mode == 'g' {
					if len(glued)+len(p) > glueMax {
						write(0, glued, gluedFrames, now)
						glued, gluedFrames = glued[:0], 0
					}
					glued = append(glued, p...)
					gluedFrames++
				} else {
					write(f.conv, p, 1, now)
				}
				f.next = f.next.Add(f.every)
				if now.Sub(f.next) > time.Second {
					f.next = now // a stall of the process: no burst to catch up
				}
			}
		}
		if gluedFrames > 0 && now.Sub(lastGlue) >= loadGlueEvery-loadTick/2 {
			write(0, glued, gluedFrames, now)
			glued, gluedFrames = glued[:0], 0
		}
		if now.Sub(lastGlue) >= loadGlueEvery-loadTick/2 {
			lastGlue = now
		}
		if now.Sub(lastSay) >= time.Second {
			say(false)
			lastSay = now
		}
	}
}

// loopConn is n's echo: what is written comes back, in the process.
type loopConn struct {
	ch     chan []byte
	done   chan struct{}
	closed sync.Once
	net.Conn
}

func newLoopConn() *loopConn {
	return &loopConn{ch: make(chan []byte, 4096), done: make(chan struct{})}
}

func (c *loopConn) Write(p []byte) (int, error) {
	b := append([]byte(nil), p...)
	select {
	case c.ch <- b:
		return len(p), nil
	case <-c.done:
		return 0, net.ErrClosed
	default:
		return 0, errors.New("loop: full")
	}
}

func (c *loopConn) Read(p []byte) (int, error) {
	select {
	case b := <-c.ch:
		return copy(p, b), nil
	case <-c.done:
		return 0, io.EOF
	}
}

func (c *loopConn) Close() error {
	c.closed.Do(func() { close(c.done) })
	return nil
}
