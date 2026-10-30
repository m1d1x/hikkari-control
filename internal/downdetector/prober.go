package downdetector

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	ProbeInterval = 60 * time.Second
	ProbeTimeout  = 2 * time.Second
	// downAfter consecutive failures turn a server down; one success turns it up.
	downAfter = 3
)

// Target is one server of the registry, with its client-side id.
type Target struct {
	ID   string
	Name string
	Host string
	Port int
}

type probeState struct {
	fails  int
	probed bool
}

// Prober TCP-connects to every registry server from the outside.
type Prober struct {
	targets []Target
	dial    func(ctx context.Context, addr string) error

	mu    sync.Mutex
	state map[string]*probeState
}

func NewProber(targets []Target) *Prober {
	p := &Prober{targets: targets, state: map[string]*probeState{}}
	p.dial = func(ctx context.Context, addr string) error {
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		return c.Close()
	}
	for _, t := range targets {
		p.state[t.ID] = &probeState{}
	}
	return p
}

// External is up, down, or unknown for servers outside the registry and
// for registry servers not probed yet.
func (p *Prober) External(id string) External {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state[id]
	switch {
	case st == nil || !st.probed:
		return ExternalUnknown
	case st.fails >= downAfter:
		return ExternalDown
	case st.fails == 0:
		return ExternalUp
	}
	// 1–2 failures in a row: still up until the third.
	return ExternalUp
}

// Round probes every target once, in parallel.
func (p *Prober) Round(ctx context.Context) {
	var wg sync.WaitGroup
	for _, t := range p.targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
			err := p.dial(cctx, net.JoinHostPort(t.Host, strconv.Itoa(t.Port)))
			cancel()
			if ctx.Err() != nil {
				return
			}
			p.record(t.ID, err == nil)
		}(t)
	}
	wg.Wait()
}

func (p *Prober) record(id string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state[id]
	st.probed = true
	if ok {
		st.fails = 0
	} else {
		st.fails++
	}
}

// Run probes every ProbeInterval until ctx ends.
func (p *Prober) Run(ctx context.Context) {
	t := time.NewTicker(ProbeInterval)
	defer t.Stop()
	for {
		p.Round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
