package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/m1d1x/hikkari-protocol/trunk"
	"github.com/sagernet/quic-go/http3"
)

const (
	autoProbeInterval = time.Second
	autoProbeTimeout  = 3 * time.Second
)

type autoEgressConfig struct {
	listen, nl, de    netip.AddrPort
	sni, nlKey, deKey string
}

func autoConfig(get func(string) string) (autoEgressConfig, error) {
	var c autoEgressConfig
	for _, v := range []struct {
		name     string
		addr     *netip.AddrPort
		loopback bool
	}{
		{"EDGE_AUTO_LISTEN", &c.listen, false},
		{"EDGE_AUTO_NL", &c.nl, true},
		{"EDGE_AUTO_DE", &c.de, true},
	} {
		addr, err := autoEgressAddress(get(v.name), v.loopback)
		if err != nil {
			return c, fmt.Errorf("%s: %w", v.name, err)
		}
		*v.addr = addr
	}
	if c.nl == c.de || c.listen.Port() == c.nl.Port() || c.listen.Port() == c.de.Port() {
		return c, errors.New("EDGE_AUTO: нужны три разных порта")
	}
	c.sni, c.nlKey, c.deKey = get("EDGE_AUTO_SNI"), get("EDGE_AUTO_NL_TRUNK_KEY"), get("EDGE_AUTO_DE_TRUNK_KEY")
	if c.sni == "" {
		return c, errors.New("EDGE_AUTO_SNI: нужен домен релея")
	}
	if trunk.ParseKey(c.nlKey) != nil || trunk.ParseKey(c.deKey) != nil {
		return c, errors.New("EDGE_AUTO: нужны ключи магистрали NL и DE")
	}
	return c, nil
}

func runAutoEgress(cmd string) error {
	c, err := autoConfig(os.Getenv)
	if err != nil || cmd == "check" {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	nl := newEgressProbe(c.nl, c.sni, c.nlKey)
	defer nl.close()
	de := newEgressProbe(c.de, c.sni, c.deKey)
	defer de.close()
	if cmd == "auto-probe" {
		for _, p := range []struct {
			country string
			probe   *egressProbe
		}{{"NL", nl}, {"DE", de}} {
			if h := p.probe.check(ctx); !h.healthy() {
				return fmt.Errorf("%s: tcp=%t udp=%t", p.country, h.tcp, h.udp)
			}
		}
		return nil
	}
	gateway, err := newAutoGateway(ctx, c.listen, c.nl)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(autoProbeInterval)
		defer ticker.Stop()
		for ctx.Err() == nil {
			standby := make(chan egressHealth, 1)
			go func() { standby <- de.check(ctx) }()
			primary := nl.check(ctx)
			gateway.observe(primary, <-standby, c.de)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	sdNotify("READY=1")
	err = gateway.serve()
	cancel()
	<-done
	return err
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

func (p *egressProbe) check(ctx context.Context) egressHealth {
	ctx, cancel := context.WithTimeout(ctx, autoProbeTimeout)
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
