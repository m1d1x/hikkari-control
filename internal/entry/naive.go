package entry

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// NaiveParams carries the relay's probe credential. Subscriber links replace
// it with that subscriber's access key, exactly as on the other transports.
type NaiveParams struct {
	Host string `json:"host"`
	User string `json:"user"`
	Pass string `json:"pass"`
	H3   bool   `json:"h3,omitempty"`
	// UDP makes the relay a Hikkari v1.01 relay: the edge adds the datagram
	// channel on udp/<port> and UDP-over-TCP in the tunnel (edge/hikkari.go).
	// The TCP side is the same Naive, so Naive-only links keep working.
	UDP bool `json:"udp,omitempty"`
}

func domainProtocol(proto string) bool { return proto == ProtoVlessXhttp || proto == ProtoNaive }

func newNaiveIdentity(port, base int, host string) (Identity, error) {
	if err := checkRelayDomain(host); err != nil {
		return Identity{}, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return Identity{}, err
	}
	return Identity{Port: port, LocalPortBase: base, Naive: &NaiveParams{Host: host, User: "probe", Pass: hex.EncodeToString(secret[:]), UDP: true}}, nil
}

func (j *jobRun) installNaive(r Remote, rel provRelay) error {
	n := rel.id.Naive
	if n == nil || !shellSafeDomainRe.MatchString(n.Host) {
		return fmt.Errorf("naive: invalid domain")
	}
	site := frontSite(n.Host)
	if _, err := r.Run(j.ctx, "umask 022 && mkdir -p /var/www/"+n.Host+" && cat > /var/www/"+n.Host+"/index.html", bytes.NewReader(site)); err != nil {
		return fmt.Errorf("naive site: %w", err)
	}
	if _, err := r.Run(j.ctx, "bash -s", strings.NewReader(naiveResolverScript)); err != nil {
		return fmt.Errorf("naive resolver: %w", err)
	}
	if j.naiveAdopt {
		out, err := r.Run(j.ctx, "if systemctl is-active --quiet naive-node-caddy.service; then systemctl disable --now naive-node-caddy.service; printf stopped; fi", nil)
		if err != nil {
			return fmt.Errorf("naive legacy handover: %w", err)
		}
		j.naiveLegacyStopped = strings.TrimSpace(out) == "stopped"
		j.naiveInstance = rel.name
	}
	j.log("Naive: Caddy/forwardproxy, сайт %d КБ, локальный DNS cache", len(site)>>10)
	return nil
}

// A local validating cache bounds negative DNS answers too. Reuse an existing
// unbound instance rather than write overlapping listener declarations.
//
// Ubuntu 24.04's package points auto-trust-anchor-file at
// /var/lib/unbound/root.key but a fresh HostVDS image has no such file, so
// unbound-checkconf and the service both failed (ams10, 07.11). unbound-anchor,
// a separate package there, writes it; its exit status 1 means "anchor
// updated", not an error.
const naiveResolverScript = `set -eu
` + aptInstallScript + `apt_install unbound unbound-anchor
if [ ! -s /var/lib/unbound/root.key ]; then
 install -d -o unbound -g unbound /var/lib/unbound
 unbound-anchor -a /var/lib/unbound/root.key || [ -s /var/lib/unbound/root.key ]
 chown unbound:unbound /var/lib/unbound/root.key
fi
if ! systemctl is-active --quiet unbound; then
 cat >/etc/unbound/unbound.conf.d/hikkari-naive.conf <<'CONF'
server:
 interface: 127.0.0.1
 access-control: 127.0.0.0/8 allow
 prefetch: yes
 cache-min-ttl: 30
 cache-max-negative-ttl: 60
forward-zone:
 name: "."
 forward-addr: 1.1.1.1
 forward-addr: 9.9.9.9
CONF
 unbound-checkconf
 systemctl reset-failed unbound 2>/dev/null || true
 systemctl enable --now unbound
fi
printf 'nameserver 127.0.0.1\noptions timeout:2 attempts:2\n' >/etc/resolv.conf
`

// withHikkariUDP turns a Naive relay into a Hikkari relay on the same
// address, port, domain and credentials.
func (id Identity) withHikkariUDP() Identity {
	n := *id.Naive
	n.UDP, n.H3 = true, false
	id.Naive = &n
	return id
}

func (id Identity) withDomain(domain string) (Identity, error) {
	if id.Naive == nil {
		return id.withXhttpDomain(domain)
	}
	if err := checkRelayDomain(domain); err != nil {
		return Identity{}, err
	}
	n := *id.Naive
	n.Host = domain
	id.Naive = &n
	return id, nil
}
