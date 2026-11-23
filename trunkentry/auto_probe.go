package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/sagernet/quic-go/http3"
)

const (
	// Each egress is probed on its own every autoProbeInterval, a probe
	// waiting at most autoProbeTimeout, so autoSilence holds several of
	// them and one late answer moves no one.
	autoProbeInterval = 250 * time.Millisecond
	autoProbeTimeout  = time.Second
	// countryProbeTimeout bounds the one-shot country-probe the hub runs
	// after a rollout, whose connections start cold.
	countryProbeTimeout = 3 * time.Second
	autoVerdictTick     = 100 * time.Millisecond
	autoHealthWrite     = time.Second
)

type countryGatewayConfig struct {
	listen, backends [2]netip.AddrPort
	sni              string
	keys             [2]string
}

func countryConfig(get func(string) string) (countryGatewayConfig, error) {
	var c countryGatewayConfig
	ports := map[uint16]bool{}
	for _, country := range []egressCountry{countryNL, countryDE} {
		prefix := "EDGE_COUNTRY_" + country.String()
		for _, v := range []struct {
			suffix   string
			addr     *netip.AddrPort
			loopback bool
		}{
			{"_LISTEN", &c.listen[country], false},
			{"_BACKEND", &c.backends[country], true},
		} {
			key := prefix + v.suffix
			addr, err := autoEgressAddress(get(key), v.loopback)
			if err != nil {
				return c, fmt.Errorf("%s: %w", key, err)
			}
			if ports[addr.Port()] {
				return c, errors.New("EDGE_COUNTRY: нужны четыре разных порта")
			}
			ports[addr.Port()] = true
			*v.addr = addr
		}
		c.keys[country] = get(prefix + "_TRUNK_KEY")
		if trunk.ParseKey(c.keys[country]) != nil {
			return c, fmt.Errorf("%s: нужен ключ магистрали", prefix)
		}
	}
	c.sni = get("EDGE_COUNTRY_SNI")
	if c.sni == "" {
		return c, errors.New("EDGE_COUNTRY_SNI: нужен домен релея")
	}
	return c, nil
}

func runCountryGateways(cmd string) error {
	c, err := countryConfig(os.Getenv)
	if err != nil || cmd == "check" {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	nl := newEgressProbe(c.backends[countryNL], c.sni, c.keys[countryNL])
	defer nl.close()
	de := newEgressProbe(c.backends[countryDE], c.sni, c.keys[countryDE])
	defer de.close()
	probes := [2]*egressProbe{countryNL: nl, countryDE: de}
	checkBoth := func() [2]egressHealth {
		standby := make(chan egressHealth, 1)
		go func() { standby <- de.check(ctx, countryProbeTimeout) }()
		return [2]egressHealth{nl.check(ctx, countryProbeTimeout), <-standby}
	}
	if cmd == "country-probe" {
		for i, h := range checkBoth() {
			if !h.healthy() {
				return fmt.Errorf("%s: tcp=%t udp=%t", egressCountry(i), h.tcp, h.udp)
			}
		}
		return nil
	}
	nlGateway, err := newAutoGateway(ctx, c.listen[countryNL], c.backends, countryNL)
	if err != nil {
		return err
	}
	deGateway, err := newAutoGateway(ctx, c.listen[countryDE], c.backends, countryDE)
	if err != nil {
		nlGateway.tcp.Close()
		nlGateway.udp.Close()
		return err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchCountries(ctx, probes, [2]*autoGateway{nlGateway, deGateway}, runtimeDirectory())
	}()
	errs := make(chan error, 2)
	go func() { errs <- nlGateway.serve() }()
	go func() { errs <- deGateway.serve() }()
	sdNotify("READY=1")
	err = <-errs
	cancel()
	<-errs
	<-done
	return err
}

type egressAnswer struct {
	country egressCountry
	at      time.Time
	health  egressHealth
}

// watchCountries probes both egresses side by side, each on its own clock,
// and moves the gateways when the verdict changes.
func watchCountries(ctx context.Context, probes [2]*egressProbe, gateways [2]*autoGateway, dir string) {
	answers := make(chan egressAnswer, 2)
	var probing sync.WaitGroup
	defer probing.Wait()
	for i, probe := range probes {
		probing.Go(func() { probeCountry(ctx, egressCountry(i), probe, answers) })
	}
	choice := newEgressChoice(time.Now())
	tick := time.NewTicker(autoVerdictTick)
	defer tick.Stop()
	var written time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-answers:
			choice.observe(a.at, a.country, a.health)
		case <-tick.C:
		}
		now := time.Now()
		previous := choice.down
		down := choice.verdict(now)
		if down != previous {
			for _, gateway := range gateways {
				gateway.route(down)
			}
			log.Printf("country egress down=%s previous=%s", down, previous)
		}
		if dir != "" && (down != previous || now.Sub(written) >= autoHealthWrite) {
			written = now
			if err := writeCountryHealth(dir, down); err != nil {
				log.Printf("country egress health: %v", err)
			}
		}
	}
}

func probeCountry(ctx context.Context, country egressCountry, probe *egressProbe, answers chan<- egressAnswer) {
	tick := time.NewTicker(autoProbeInterval)
	defer tick.Stop()
	for {
		health := probe.check(ctx, autoProbeTimeout)
		select {
		case answers <- egressAnswer{country, time.Now(), health}:
		case <-ctx.Done():
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func writeCountryHealth(dir string, down egressCountry) error {
	tmp := filepath.Join(dir, "health.new")
	if err := os.WriteFile(tmp, fmt.Appendf(nil, "%d %s\n", time.Now().Unix(), down), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "health"))
}

// The same health request goes through the relay on each transport. The
// relay checks external HTTPS for the stream leg and external STUN for the
// datagram leg. The entry uses only an existing trunk key; the health token
// cannot authorize a CONNECT or a subscriber's QUIC session.
type egressProbe struct {
	endpoint, token string
	tcp             *http.Transport
	udp             *http3.Transport
}

func newEgressProbe(addr netip.AddrPort, sni, key string) *egressProbe {
	tlsCfg := &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS13}
	// The URL names the local listener; TLS still authenticates the relay.
	return &egressProbe{
		endpoint: "https://" + addr.String() + "/.well-known/hikkari-egress",
		token:    fmt.Sprintf("%x", sha256.Sum256([]byte("hikkari-egress-health/v1:"+key))),
		tcp:      &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: tlsCfg},
		udp:      &http3.Transport{TLSClientConfig: tlsCfg.Clone()},
	}
}

func (p *egressProbe) close() {
	p.tcp.CloseIdleConnections()
	p.udp.Close()
}

func (p *egressProbe) check(ctx context.Context, timeout time.Duration) egressHealth {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tcp := make(chan bool, 1)
	go func() { tcp <- p.round(ctx, p.tcp) }()
	udp := p.round(ctx, p.udp)
	if !udp {
		// Clear a stale QUIC connection without waiting for its idle timeout.
		p.udp.Close()
		p.udp = &http3.Transport{TLSClientConfig: p.udp.TLSClientConfig}
	}
	return egressHealth{tcp: <-tcp, udp: udp}
}

func (p *egressProbe) round(ctx context.Context, transport http.RoundTripper) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, p.endpoint, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}
