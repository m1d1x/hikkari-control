// Package subscription serves a subscriber their share of the fleet as an
// ordinary VPN subscription: the relays the entry pool batched for their
// account, as client links (vless://), base64 over HTTPS — what the Hikkari
// client and any other client already import.
package subscription

import (
	"errors"
	"net"
	"net/url"
	"strconv"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// errNoLink: a relay the client cannot dial from a plain link.
var errNoLink = errors.New("relay has no client link")

// Link renders one relay as the link a client imports. Hysteria2 relays have
// none yet: they are reached by a self-signed certificate that must be pinned
// and wear masks beyond salamander, and a plain hysteria2:// link can carry
// neither — the client would dial without the pin (open to interception) or
// not get through at all.
func Link(r entry.RelayRecord) (string, error) {
	host := net.JoinHostPort(r.Address, strconv.Itoa(r.Port))
	label := "Hikkari " + r.Name
	switch {
	case r.Proto == entry.ProtoVlessRealityVision && r.RealityParams != nil:
		p := r.RealityParams
		if len(p.Covers) == 0 {
			return "", errNoLink
		}
		q := url.Values{}
		q.Set("encryption", "none")
		q.Set("flow", "xtls-rprx-vision")
		q.Set("security", "reality")
		q.Set("type", "tcp")
		q.Set("sni", p.Covers[0])
		q.Set("fp", "chrome")
		q.Set("pbk", p.PublicKey)
		q.Set("sid", p.ShortID)
		return vless(p.UUID, host, q, label), nil
	case r.Proto == entry.ProtoVlessXhttp && r.XhttpParams != nil:
		p := r.XhttpParams
		q := url.Values{}
		q.Set("encryption", "none")
		q.Set("security", "tls")
		q.Set("type", "xhttp")
		q.Set("sni", p.Host)
		q.Set("host", p.Host)
		q.Set("path", p.Path)
		q.Set("alpn", "h2,http/1.1")
		q.Set("fp", "chrome")
		if p.Mode != "" {
			q.Set("mode", p.Mode)
		}
		return vless(p.UUID, host, q, label), nil
	}
	return "", errNoLink
}

func vless(uuid, host string, q url.Values, label string) string {
	u := url.URL{Scheme: "vless", User: url.User(uuid), Host: host, RawQuery: q.Encode(), Fragment: label}
	return u.String()
}
