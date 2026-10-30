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
	at     time.Time
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
	case st == nil || !st.probed || time.Since(st.at) > 2*ProbeInterval+ProbeTimeout:
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
	p.mu.Lock()
	targets := append([]Target(nil), p.targets...)
	p.mu.Unlock()
	for _, t := range targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
			err := p.dial(cctx, net.JoinHostPort(t.Host, strconv.Itoa(t.Port)))
			cancel()
			if ctx.Err() != nil {
				return
			}
			p.recordTarget(t, err == nil)
		}(t)
	}
	wg.Wait()
}

func (p *Prober) record(id string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state[id]
	if st == nil {
		return
	}
	st.probed = true
	st.at = time.Now()
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

// ReplaceTargets resets external evidence when a logical node moves. A probe
// still in flight for the old endpoint cannot update the new endpoint's state.
func (p *Prober) ReplaceTargets(targets []Target) {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := map[string]Target{}
	for _, t := range p.targets {
		old[t.ID] = t
	}
	next := map[string]*probeState{}
	for _, t := range targets {
		st := p.state[t.ID]
		if prior, ok := old[t.ID]; !ok || prior.Host != t.Host || prior.Port != t.Port || st == nil {
			st = &probeState{}
		}
		next[t.ID] = st
	}
	p.targets = append([]Target(nil), targets...)
	p.state = next
}
func (p *Prober) recordTarget(t Target, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, current := range p.targets {
		if current.ID != t.ID || current.Host != t.Host || current.Port != t.Port {
			continue
		}
		st := p.state[t.ID]
		if st == nil {
			return
		}
		st.probed = true
		st.at = time.Now()
		if ok {
			st.fails = 0
		} else {
			st.fails++
		}
		return
	}
}
