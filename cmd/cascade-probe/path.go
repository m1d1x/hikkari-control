package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Path is the way between two boxes with no protocol on it: bare TCP from
// flows distinct source ports (a censor kills flows by source port,
// plans/2026-11-02-tcp-flow-loss.md), each holding its connection for a
// moment, and ICMP by the system's ping.
type Path struct {
	Target    string  `json:"target"`
	Flows     int     `json:"flows"`
	Opened    int     `json:"opened"`
	Refused   int     `json:"refused"`
	TimedOut  int     `json:"timed_out"`
	ConnP50MS float64 `json:"connect_p50_ms"`
	ConnMaxMS float64 `json:"connect_max_ms"`
	// ICMP: sent, received, round trip; -1 when ping is not there.
	PingSent int     `json:"ping_sent"`
	PingRecv int     `json:"ping_recv"`
	PingAvg  float64 `json:"ping_avg_ms"`
	PingMax  float64 `json:"ping_max_ms"`
	PingMdev float64 `json:"ping_mdev_ms"`
}

const (
	defaultFlows = 12
	connectWait  = 3 * time.Second
	pingCount    = 20
)

func measurePath(target string, flows int) (Path, error) {
	host, _, err := net.SplitHostPort(target)
	if err != nil || net.ParseIP(host) == nil {
		return Path{}, fmt.Errorf("path: %q is not ip:port", target)
	}
	p := Path{Target: target, Flows: flows, PingSent: -1, PingRecv: -1}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.ping(host) }()
	var mu sync.Mutex
	var took []float64
	// Source ports above Linux's ephemeral range, picked at random each run;
	// a port the box holds already is passed over for another.
	ports := rand.Perm(4000)
	for i := 0; i < flows; i++ {
		var c net.Conn
		var err error
		var ms float64
		for len(ports) > 0 {
			port := 61000 + ports[0]
			ports = ports[1:]
			start := time.Now()
			d := net.Dialer{Timeout: connectWait, LocalAddr: &net.TCPAddr{Port: port}}
			c, err = d.DialContext(context.Background(), "tcp4", target)
			ms = float64(time.Since(start).Microseconds()) / 1000
			if !isErrno(err, syscall.EADDRINUSE) {
				break
			}
		}
		mu.Lock()
		switch {
		case err == nil:
			p.Opened++
			took = append(took, ms)
		case isErrno(err, syscall.ECONNREFUSED):
			p.Refused++
		default:
			p.TimedOut++
		}
		mu.Unlock()
		if c != nil {
			time.Sleep(200 * time.Millisecond)
			c.Close()
		}
	}
	sort.Float64s(took)
	if len(took) > 0 {
		p.ConnP50MS = took[len(took)/2]
		p.ConnMaxMS = took[len(took)-1]
	}
	wg.Wait()
	return p, nil
}

func isErrno(err error, want syscall.Errno) bool {
	return err != nil && errors.Is(err, want)
}

var (
	rePingCount = regexp.MustCompile(`(\d+) packets transmitted, (\d+) (?:packets )?received`)
	rePingRTT   = regexp.MustCompile(`= ([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+) ms`)
)

func (p *Path) ping(host string) {
	out, _ := exec.Command("ping", "-n", "-q", "-c", strconv.Itoa(pingCount), "-i", "0.2", "-W", "2", host).CombinedOutput()
	if m := rePingCount.FindSubmatch(out); m != nil {
		p.PingSent, _ = strconv.Atoi(string(m[1]))
		p.PingRecv, _ = strconv.Atoi(string(m[2]))
	}
	if m := rePingRTT.FindSubmatch(out); m != nil {
		p.PingAvg, _ = strconv.ParseFloat(string(m[2]), 64)
		p.PingMax, _ = strconv.ParseFloat(string(m[3]), 64)
		p.PingMdev, _ = strconv.ParseFloat(string(m[4]), 64)
	}
}
