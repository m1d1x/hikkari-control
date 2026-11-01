// Package subscription serves a subscriber their share of the fleet as an
// ordinary VPN subscription: the relays the entry pool batched for their
// account, as client links (vless://), base64 over HTTPS — what the Hikkari
// client and any other client already import.
package subscription

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// errNoLink: a relay the client cannot dial from a plain link.
var errNoLink = errors.New("relay has no client link")

// Link renders Hikkari links. Extended Hysteria2 links require the Xray
// adapter: pinSHA256 is the leaf certificate digest, and finalmask is the
// base64url JSON mask list from the signed record (including Noise).
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
	case r.Proto == entry.ProtoHysteria2 && r.HysteriaParams != nil:
		p := r.HysteriaParams
		pin, err := hex.DecodeString(p.CertSHA256)
		if err != nil || len(pin) != 32 || p.Auth == "" || p.ServerName == "" || len(p.Masks) == 0 {
			return "", errNoLink
		}
		masks, err := json.Marshal(p.Masks)
		if err != nil {
			return "", err
		}
		q := url.Values{}
		q.Set("sni", p.ServerName)
		q.Set("alpn", "h3")
		q.Set("pinSHA256", p.CertSHA256)
		q.Set("finalmask", base64.RawURLEncoding.EncodeToString(masks))
		u := url.URL{Scheme: "hysteria2", User: url.User(p.Auth), Host: host, RawQuery: q.Encode(), Fragment: label}
		return u.String(), nil
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

// SingBoxHysteriaLink renders a Hysteria2 relay for clients without Xray's
// finalmask — sing-box first among them, which carries every protocol of the
// subscription in one engine so the client can switch between them live
// (owner, 01.11). The salamander mask is the whole of what such a client
// wears; the relay's noise mask only adds junk the server sends, which
// salamander drops on the way in. The leaf is self-signed, so the link pins
// its public key (hikkari-spki, hex SHA-256 of the SubjectPublicKeyInfo) for
// the Hikkari client to hand sing-box as tls.certificate_public_key_sha256; a
// record signed before it carried that pin gets no link rather than an
// unpinned one.
func SingBoxHysteriaLink(r entry.RelayRecord) (string, error) {
	p := r.HysteriaParams
	if r.Proto != entry.ProtoHysteria2 || p == nil || p.Auth == "" || p.ServerName == "" || p.PubKeySHA256 == "" {
		return "", errNoLink
	}
	salamander := ""
	for _, m := range p.Masks {
		if m.Type == "salamander" {
			salamander = m.Password
		}
	}
	if salamander == "" {
		return "", errNoLink
	}
	q := url.Values{}
	q.Set("sni", p.ServerName)
	q.Set("alpn", "h3")
	q.Set("obfs", "salamander")
	q.Set("obfs-password", salamander)
	q.Set("hikkari-spki", p.PubKeySHA256)
	host := net.JoinHostPort(r.Address, strconv.Itoa(r.Port))
	u := url.URL{Scheme: "hysteria2", User: url.User(p.Auth), Host: host, RawQuery: q.Encode(), Fragment: "Hikkari " + r.Name}
	return u.String(), nil
}
