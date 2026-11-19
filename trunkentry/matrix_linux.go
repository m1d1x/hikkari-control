package main

import (
	"bufio"
	"context"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// runMatrix pings each target of the matrix (matrix.go) and watches the box
// until ctx ends: a target's minute, the box's minute and a verdict on each
// receiver's silence of matrixSilence or more, once a minute. The pings go
// by the kernel's unprivileged ICMP sockets (net.ipv4.ping_group_range: the
// brain is a DynamicUser without CAP_NET_RAW).
func runMatrix(ctx context.Context, targets []matrixTarget) {
	if len(targets) == 0 {
		return
	}
	m := &matrix{}
	for _, t := range targets {
		c, err := icmp.ListenPacket("udp4", "0.0.0.0")
		if err != nil {
			log.Printf("nekko-trunk-entry: matrix: %s: %v (the matrix stands without it)", t.Name, err)
			continue
		}
		s := &matrixSeries{t: t}
		m.series = append(m.series, s)
		go func() { <-ctx.Done(); c.Close() }()
		go m.ping(ctx, c, s)
		go m.read(c, s)
	}
	go m.tick(ctx)
	go m.watchBox(ctx)
	m.report(ctx)
}

type matrix struct {
	mu     sync.Mutex
	series []*matrixSeries
	sentAt map[*matrixSeries]map[uint16]time.Time
	lates  []lateAt
	secs   []boxSecond
	judged time.Time // silences that ended before it are said
	lastTo time.Time // the end of the latest silence said
}

type lateAt struct {
	at   time.Time
	late time.Duration
}

func (m *matrix) ping(ctx context.Context, c *icmp.PacketConn, s *matrixSeries) {
	t := time.NewTicker(s.t.Every)
	defer t.Stop()
	dst := &net.UDPAddr{IP: s.t.Addr.AsSlice()}
	body := make([]byte, 16)
	for seq := uint16(1); ; seq++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{Seq: int(seq), Data: body}}
		b, err := msg.Marshal(nil)
		if err != nil {
			continue
		}
		now := time.Now()
		m.mu.Lock()
		if m.sentAt == nil {
			m.sentAt = map[*matrixSeries]map[uint16]time.Time{}
		}
		if m.sentAt[s] == nil {
			m.sentAt[s] = map[uint16]time.Time{}
		}
		m.sentAt[s][seq] = now
		delete(m.sentAt[s], seq-3000) // a minute and more at 20 ms
		s.sent = append(s.sent, now)
		m.mu.Unlock()
		_, _ = c.WriteTo(b, dst)
	}
}

func (m *matrix) read(c *icmp.PacketConn, s *matrixSeries) {
	buf := make([]byte, 1500)
	for {
		n, _, err := c.ReadFrom(buf)
		if err != nil {
			return
		}
		now := time.Now()
		msg, err := icmp.ParseMessage(1, buf[:n])
		if err != nil || msg.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		e, ok := msg.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		m.mu.Lock()
		if at, ok := m.sentAt[s][uint16(e.Seq)]; ok {
			delete(m.sentAt[s], uint16(e.Seq))
			s.answers = append(s.answers, now)
			s.rtts = append(s.rtts, now.Sub(at))
		}
		m.mu.Unlock()
	}
}

// tick runs a ticker every 20 ms: a turn later than that is the brain's
// process not run (a frozen machine, the scheduler).
func (m *matrix) tick(ctx context.Context) {
	const every = 20 * time.Millisecond
	t := time.NewTicker(every)
	defer t.Stop()
	prev := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			now = time.Now()
			if late := now.Sub(prev) - every; late > 40*time.Millisecond {
				m.mu.Lock()
				m.lates = append(m.lates, lateAt{prev, late})
				m.mu.Unlock()
			}
			prev = now
		}
	}
}

// watchBox reads steal and the interfaces' drops each second.
func (m *matrix) watchBox(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	steal, total := cpuTimes()
	rx, tx := ifaceDrops()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s2, t2 := cpuTimes()
		rx2, tx2 := ifaceDrops()
		pct := 0.0
		if t2 > total {
			pct = 100 * float64(s2-steal) / float64(t2-total)
		}
		sec := boxSecond{At: time.Now(), StealPct: pct, RxDrop: rx2 - rx, TxDr: tx2 - tx}
		if rx2 < rx || tx2 < tx {
			sec.RxDrop, sec.TxDr = 0, 0
		}
		steal, total, rx, tx = s2, t2, rx2, tx2
		m.mu.Lock()
		m.secs = append(m.secs, sec)
		m.mu.Unlock()
	}
}

// report says each minute two seconds after it ends: answers come late,
// up to a second, and a silence ends with its first answer.
func (m *matrix) report(ctx context.Context) {
	const grace = 2 * time.Second
	m.judged = time.Now()
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute + grace)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		to := next.Add(-grace)
		from := to.Add(-time.Minute)
		var lines []string
		m.mu.Lock()
		for _, s := range m.series {
			lines = append(lines, s.window(from, to))
		}
		var secs []boxSecond
		for _, s := range m.secs {
			if !s.At.Before(from) && s.At.Before(to) {
				secs = append(secs, s)
			}
		}
		var lates []time.Duration
		for _, l := range m.lates {
			if !l.at.Before(from) && l.at.Before(to) {
				lates = append(lates, l.late)
			}
		}
		lines = append(lines, boxMinute(from, secs, lates))
		until := time.Now().Add(-time.Second)
		for _, s := range m.series {
			if s.t.Kind != "rx" {
				continue
			}
			for _, sil := range s.silences(m.judged, until) {
				if sil[0].Before(m.lastTo) {
					continue // the same silence, heard on another receiver
				}
				var boxLate time.Duration
				for _, l := range m.lates {
					if l.at.Before(sil[1]) && l.at.Add(l.late).After(sil[0]) {
						boxLate += l.late
					}
				}
				lines = append(lines, judgeSilence(m.series, sil[0], sil[1], boxLate).line())
				m.lastTo = sil[1]
			}
		}
		m.judged = until
		cut := time.Now().Add(-matrixKeep)
		for _, s := range m.series {
			s.trim(time.Now())
		}
		for len(m.lates) > 0 && m.lates[0].at.Before(cut) {
			m.lates = m.lates[1:]
		}
		for len(m.secs) > 0 && m.secs[0].At.Before(cut) {
			m.secs = m.secs[1:]
		}
		m.mu.Unlock()
		for _, l := range lines {
			log.Print("nekko-trunk-entry: " + l)
		}
	}
}

// cpuTimes is the steal and all of the CPUs' time from /proc/stat, in ticks.
func cpuTimes() (steal, total uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 9 || fs[0] != "cpu" {
			continue
		}
		for i, v := range fs[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i < 8 { // guest and guest_nice are in user and nice already
				total += n
			}
			if i == 7 {
				steal = n
			}
		}
		return steal, total
	}
	return 0, 0
}

// ifaceDrops is the drops in and out of every interface but loopback.
func ifaceDrops() (rx, tx uint64) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			continue
		}
		fs := strings.Fields(rest)
		if len(fs) < 12 {
			continue
		}
		r, _ := strconv.ParseUint(fs[3], 10, 64)
		t, _ := strconv.ParseUint(fs[11], 10, 64)
		rx, tx = rx+r, tx+t
	}
	return rx, tx
}
