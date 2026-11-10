package main

// The exit end of the cascade's trunk (package trunk) rides on a Hikkari
// relay: a member is a CONNECT to trunk.Host behind the relay's own TLS,
// certificate, padding and site, authorised by a trunk key instead of a
// subscriber's. What it carries reaches only this box's own services on
// loopback, on the ports EDGE_HIKKARI_TRUNK_PORTS names (the relay itself;
// an experiment's receiver): a trunk key opens no road to the internet.
//
//	entry (RU) ═ CONNECT trunk.hikkari.arpa ═► this relay :443 ─► 127.0.0.1:<port>
//
// A trunk carries clients' own relay connections, which this relay counts
// under their keys when they arrive on loopback; the trunk's bytes are not
// counted again.

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/m1d1x/hikkari-protocol/connect"
	"github.com/m1d1x/hikkari-protocol/trunk"
)

// trunkExitConfig is EDGE_HIKKARI_TRUNK_*: the keys entries present and the
// loopback ports their streams may reach. Zero keys is a relay without a
// trunk.
type trunkExitConfig struct {
	Keys  []string
	Ports map[uint16]bool
}

func trunkExitFromEnv(get func(string) string) (trunkExitConfig, error) {
	var c trunkExitConfig
	raw := strings.TrimSpace(get("EDGE_HIKKARI_TRUNK_KEYS"))
	if raw == "" {
		if get("EDGE_HIKKARI_TRUNK_PORTS") != "" {
			return c, errors.New("EDGE_HIKKARI_TRUNK_PORTS without EDGE_HIKKARI_TRUNK_KEYS")
		}
		return c, nil
	}
	for _, key := range strings.Split(raw, ",") {
		if err := trunk.ParseKey(key); err != nil {
			return c, fmt.Errorf("EDGE_HIKKARI_TRUNK_KEYS: %w", err)
		}
		c.Keys = append(c.Keys, key)
	}
	ports := strings.TrimSpace(get("EDGE_HIKKARI_TRUNK_PORTS"))
	if ports == "" {
		return c, errors.New("EDGE_HIKKARI_TRUNK_PORTS: empty, a trunk must reach something")
	}
	c.Ports = map[uint16]bool{}
	for _, p := range strings.Split(ports, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("EDGE_HIKKARI_TRUNK_PORTS: %q", p)
		}
		c.Ports[uint16(n)] = true
	}
	return c, nil
}

func (c trunkExitConfig) admits(key string) bool {
	ok := false
	for _, k := range c.Keys {
		ok = subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 || ok
	}
	return ok
}

// trunkLocal dials this box's own services for the trunk's streams and
// flows.
// Each connection is tracked as its stream's or flow's (trunk_observe.go).
type trunkLocal struct {
	ports   map[uint16]bool
	targets *trunkTargets
}

var errTrunkPort = errors.New("trunk: port not open to the trunk")

func (l trunkLocal) DialStream(ctx context.Context, port uint16) (net.Conn, error) {
	if !l.ports[port] {
		return nil, errTrunkPort
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	return l.track(ctx, "tcp", c, err)
}

func (l trunkLocal) DialFlow(ctx context.Context, port uint16) (net.Conn, error) {
	if !l.ports[port] {
		return nil, errTrunkPort
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	return l.track(ctx, "udp", c, err)
}

func (l trunkLocal) track(ctx context.Context, network string, c net.Conn, err error) (net.Conn, error) {
	if err != nil || l.targets == nil {
		return c, err
	}
	if tg, ok := trunk.TargetFrom(ctx); ok {
		return l.targets.track(network, c, tg), nil
	}
	return c, nil
}

// isTrunkHost reports whether a CONNECT asks for a trunk member.
func isTrunkHost(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		host = hostPort
	}
	want, _, _ := net.SplitHostPort(trunk.Host)
	return strings.EqualFold(host, want)
}

// startTrunkExit makes the runtime's trunk exit and logs its counters every
// minute while it has sessions.
func (n *naiveRuntime) startTrunkExit(ctx context.Context) {
	if len(n.cfg.Trunk.Keys) == 0 {
		return
	}
	targets := newTrunkTargets()
	x := trunk.NewExit(trunk.Config{Observe: n.trunkObserved}, trunkLocal{n.cfg.Trunk.Ports, targets})
	n.mu.Lock()
	n.trunk = x
	n.targets = targets
	n.mu.Unlock()
	go x.Run(ctx)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if sessions, st := x.Stats(); sessions > 0 {
					log.Printf("nekko-edge: trunk exit: sessions=%d members=%d stalled=%d broken=%d resent=%d streams=%d/%d reset=%d up=%d down=%d flows=%d dgrams=%d/%d copies=%d pauses=%d paused_ms=%d",
						sessions, st.Members, st.MembersStall, st.MembersBroken, st.Resent, st.Streams, st.StreamsOpened, st.StreamsReset,
						st.Up, st.Down, st.Flows, st.DgramsOut, st.DgramsIn, st.DgramsCopy, st.Pauses, st.Paused.Milliseconds())
				}
			}
		}
	}()
	log.Printf("nekko-edge: trunk exit on, %d key(s), ports %v", len(n.cfg.Trunk.Keys), sortedPorts(n.cfg.Trunk.Ports))
}

func sortedPorts(ports map[uint16]bool) []int {
	out := make([]int, 0, len(ports))
	for p := range ports {
		out = append(out, int(p))
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// serveTrunk answers a CONNECT that is a trunk's (its user or its host). A
// trunk key on trunk.Host gets a member; anything else is a stranger and
// gets what strangers get.
func (n *naiveRuntime) serveTrunk(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler, user, pass string) error {
	n.mu.Lock()
	x := n.trunk
	closed := n.closed
	n.mu.Unlock()
	if x == nil || closed || user != trunk.User || !isTrunkHost(r.Host) || !n.cfg.Trunk.admits(pass) {
		// Not ours to tell apart from a wrong password: the proxy's own
		// probe resistance answers.
		r = r.Clone(r.Context())
		r.Header.Del("Proxy-Authorization")
		return next.ServeHTTP(w, r)
	}
	if r.ProtoMajor < 2 {
		http.Error(w, "unsupported transport", http.StatusBadRequest)
		return nil
	}
	ctx, cancel := context.WithCancel(r.Context())
	n.mu.Lock()
	n.serial++
	id := n.serial
	n.sessions[id] = naiveSession{"", cancel}
	n.mu.Unlock()
	defer func() { cancel(); n.mu.Lock(); delete(n.sessions, id); n.mu.Unlock() }()
	ctx = context.WithValue(ctx, connect.ContextKeyDial{}, func(ctx context.Context) (net.Conn, error) {
		proxySide, relaySide := net.Pipe()
		go func() {
			defer relaySide.Close()
			stop := context.AfterFunc(ctx, func() { _ = relaySide.Close() })
			defer stop()
			n.tunnelEnded(trunkGroup, "", x.ServeMember(relaySide))
		}()
		return proxySide, nil
	})
	r = r.Clone(ctx)
	r.Header.Set("Proxy-Authorization", basicAuth(n.cfg.User, n.cfg.Pass))
	_ = next.ServeHTTP(w, r)
	return nil
}
