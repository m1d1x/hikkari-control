package entry

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
)

// DefaultCovers are the cover sites a new relay borrows, in race order — the
// set proven through ТСПУ (plan 42 §9, П4).
var DefaultCovers = []string{"www.samsung.com", "www.nvidia.com", "www.tesla.com"}

// Identity is everything a relay is: the secrets nekko-edge runs with and what
// a client needs to dial it. Born on the hub, sealed at rest
// (entry_relays.identity_ct); the edge gets it rendered as EDGE_* at deploy,
// the client gets the public part inside a signed record.
//
// A stream relay (its box speaks Reality) fills the Reality fields; a voice
// relay (its box speaks Hysteria2) fills Hysteria and leaves them empty. Port
// and LocalPortBase are both kinds'.
type Identity struct {
	PrivateKey string   `json:"private_key"` // x25519, base64url, clamped — what Xray stores
	PublicKey  string   `json:"public_key"`  // x25519, base64url
	ShortID    string   `json:"short_id"`    // hex
	UUID       string   `json:"uuid"`        // VLESS user id
	Covers     []string `json:"covers"`
	Port       int      `json:"port"`
	// LocalPortBase is the first of the relay's block of loopback ports
	// (relayLoopbackBlock of them, distinct per relay on one box): a stream
	// relay's per-cover fallbacks from the start, the admission echo of
	// either kind at echoPort.
	LocalPortBase int `json:"local_port_base"`
	// Hysteria is set on a voice relay and only there.
	Hysteria *HysteriaIdentity `json:"hysteria,omitempty"`
	// Xhttp is set on an XHTTP stream relay and only there; UUID is its VLESS
	// user, the Reality fields stay empty.
	Xhttp *XhttpIdentity `json:"xhttp,omitempty"`
	// Awg is set on an AmneziaWG relay and only there.
	Awg *AwgIdentity `json:"awg,omitempty"`
}

// AwgIdentity is the AmneziaWG half of a relay: the device's key, the
// vantage's own peer, and the obfuscation both ends must agree on, drawn per
// relay so that no two relays share what a censor would look for.
type AwgIdentity struct {
	ServerPrivateKey string `json:"server_private_key"`
	ServerPublicKey  string `json:"server_public_key"`
	ProbePrivateKey  string `json:"probe_private_key"`
	ProbePublicKey   string `json:"probe_public_key"`
	Jc               int    `json:"jc"`
	Jmin             int    `json:"jmin"`
	Jmax             int    `json:"jmax"`
	S1               int    `json:"s1"`
	S2               int    `json:"s2"`
	H1               string `json:"h1"`
	H2               string `json:"h2"`
	H3               string `json:"h3"`
	H4               string `json:"h4"`
}

// AwgProbeAddress is the vantage's address inside every AmneziaWG relay; the
// subscribers' are drawn from the same network past it
// (internal/subscription/awg.go).
const AwgProbeAddress = "10.66.0.1"

// The UDP port of an AmneziaWG relay is drawn from a high range: unlike TLS and
// QUIC it has no reason to sit on 443, where UDP that is not QUIC stands out.
const (
	awgPortMin = 20000
	awgPortMax = 60000
)

// NewAwgKeyPair is a WireGuard key pair, base64 as both ends' configs take it.
func NewAwgKeyPair() (private, public string, err error) {
	raw := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	raw[0] &= 248
	raw[31] = raw[31]&127 | 64
	pub, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(pub), nil
}

// newAwgIdentity mints an AmneziaWG relay. The junk packets stay small and few:
// they go out ahead of every handshake, and a phone pays for them. S1 and S2
// must not make the two handshake messages the same size (S1+148 != S2+92), or
// the message type could be read off the length. H1..H4 must be distinct, and
// not WireGuard's own 1..4.
func newAwgIdentity(port int) (Identity, error) {
	serverPriv, serverPub, err := NewAwgKeyPair()
	if err != nil {
		return Identity{}, err
	}
	probePriv, probePub, err := NewAwgKeyPair()
	if err != nil {
		return Identity{}, err
	}
	a := &AwgIdentity{
		ServerPrivateKey: serverPriv, ServerPublicKey: serverPub,
		ProbePrivateKey: probePriv, ProbePublicKey: probePub,
	}
	if a.Jc, err = randomInt(3, 6); err != nil {
		return Identity{}, err
	}
	if a.Jmin, err = randomInt(40, 60); err != nil {
		return Identity{}, err
	}
	if a.Jmax, err = randomInt(a.Jmin+20, 200); err != nil {
		return Identity{}, err
	}
	for {
		if a.S1, err = randomInt(15, 120); err != nil {
			return Identity{}, err
		}
		if a.S2, err = randomInt(15, 120); err != nil {
			return Identity{}, err
		}
		if a.S1+148 != a.S2+92 {
			break
		}
	}
	seen := map[int]bool{}
	for _, h := range []*string{&a.H1, &a.H2, &a.H3, &a.H4} {
		for {
			n, err := randomInt(5, 2147483647)
			if err != nil {
				return Identity{}, err
			}
			if !seen[n] {
				seen[n] = true
				*h = strconv.Itoa(n)
				break
			}
		}
	}
	return Identity{Port: port, Awg: a}, nil
}

// XhttpIdentity is the XHTTP half of a stream relay (П4в): the box's domain,
// copied in when the relay is minted, and the secret path the tunnel lives
// under. The path is a credential like a Reality key — it is what tells the
// tunnel from the decoy site — so it is drawn per relay and travels only in
// the relay's signed record and its env.
type XhttpIdentity struct {
	Domain string `json:"domain"`
	Path   string `json:"path"`
	Mode   string `json:"mode"`
}

// HysteriaIdentity is the voice half of a relay: its certificate, its
// credential and the masks it wears on the wire. Everything here is drawn per
// relay (voice plan §3.1) — no signature common to all Nekko entries, and a
// relay that is caught gives away only its own.
type HysteriaIdentity struct {
	// CertPEM / KeyPEM: a self-signed P-256 leaf for ServerName. No CA: the
	// signed record carries CertSHA256 and the client pins it.
	CertPEM    string `json:"cert_pem"`
	KeyPEM     string `json:"key_pem"`
	CertSHA256 string `json:"cert_sha256"`
	ServerName string `json:"server_name"`
	Auth       string `json:"auth"`
	Masks      []Mask `json:"masks"`
}

// proto is the protocol this identity speaks.
func (id Identity) proto() string {
	switch {
	case id.Hysteria != nil:
		return ProtoHysteria2
	case id.Xhttp != nil:
		return ProtoVlessXhttp
	case id.Awg != nil:
		return ProtoAmneziaWG
	}
	return ProtoVlessRealityVision
}

// relayLoopbackBlock is how many loopback ports each relay on a box owns,
// from its LocalPortBase.
const relayLoopbackBlock = 100

// echoPort is the loopback UDP echo the voice admission probe aims at. A
// voice relay binds nothing else on loopback, so it sits at the base; a stream
// relay's covers start there, so its echo takes the last port of the block.
func (id Identity) echoPort() int {
	if id.Hysteria != nil {
		return id.LocalPortBase
	}
	if id.Xhttp != nil || id.Awg != nil {
		return 0 // no admission echo: streams only, or a tunnel of its own
	}
	return id.LocalPortBase + relayLoopbackBlock - 1
}

// voiceCertLifetime outlives any relay: the pin, not the validity window, is
// what the client checks, and an expiring leaf would only be a way for a
// forgotten relay to die on a date.
const voiceCertLifetime = 10 * 365 * 24 * time.Hour

// newVoiceIdentity mints a voice relay: a fresh certificate, auth and masks.
// The masks are Salamander (the wire is uniform noise, no QUIC look) under a
// few junk packets ahead of the first real one (AmneziaWG's idea), with the
// junk drawn per relay. Both measured clean through ТСПУ (voice plan Ф1:
// "noise" and "amnezia"). DTLS-looking prefixes are not used: the soak saw
// them throttled for hours (Ф2).
func newVoiceIdentity(port, localPortBase int) (Identity, error) {
	name, err := randomServerName()
	if err != nil {
		return Identity{}, err
	}
	certPEM, keyPEM, pin, err := selfSignedLeaf(name)
	if err != nil {
		return Identity{}, err
	}
	auth, err := randomHex(16)
	if err != nil {
		return Identity{}, err
	}
	password, err := randomHex(16)
	if err != nil {
		return Identity{}, err
	}
	junk, err := randomJunk()
	if err != nil {
		return Identity{}, err
	}
	return Identity{
		Port:          port,
		LocalPortBase: localPortBase,
		Hysteria: &HysteriaIdentity{
			CertPEM:    certPEM,
			KeyPEM:     keyPEM,
			CertSHA256: pin,
			ServerName: name,
			Auth:       auth,
			Masks: []Mask{
				{Type: "salamander", Password: password},
				{Type: "noise", Junk: junk, ResetSecs: &[2]int{0, 0}},
			},
		},
	}, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// randomInt is uniform in [lo, hi].
func randomInt(lo, hi int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(hi-lo+1)))
	if err != nil {
		return 0, err
	}
	return lo + int(n.Int64()), nil
}

// randomServerName is a plausible-looking host name. Under the masks nobody
// sees it; it only has to match the certificate.
func randomServerName() (string, error) {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	n, err := randomInt(6, 12)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for range n {
		i, err := randomInt(0, len(letters)-1)
		if err != nil {
			return "", err
		}
		b.WriteByte(letters[i])
	}
	tlds := []string{"com", "net", "org", "io"}
	t, err := randomInt(0, len(tlds)-1)
	if err != nil {
		return "", err
	}
	return b.String() + "." + tlds[t], nil
}

// randomJunk draws 1–4 junk packets of 40–180 bytes, each length range its
// own, with a pause of at most 2 ms after each: the first voice packet to a
// destination waits for the sum (nekko-entry record.rs MAX_JUNK_DELAY_MS).
func randomJunk() ([]Junk, error) {
	count, err := randomInt(1, 4)
	if err != nil {
		return nil, err
	}
	out := make([]Junk, 0, count)
	for range count {
		lo, err := randomInt(40, 120)
		if err != nil {
			return nil, err
		}
		hi, err := randomInt(lo, 180)
		if err != nil {
			return nil, err
		}
		out = append(out, Junk{Len: [2]int{lo, hi}, DelayMs: [2]int{0, 2}})
	}
	return out, nil
}

// selfSignedLeaf makes the voice relay's certificate and returns it, its key
// (PEM) and the SHA-256 of its DER — the pin a client checks.
func selfSignedLeaf(serverName string) (certPEM, keyPEM, pin string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", "", "", err
	}
	now := time.Now().Add(-time.Hour)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: serverName},
		DNSNames:              []string{serverName},
		NotBefore:             now,
		NotAfter:              now.Add(voiceCertLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		hex.EncodeToString(sum[:]), nil
}

// xhttpPathBytes of randomness make the secret path: 18 bytes are 24
// base64url characters, "/" in front — above the record's 17-character floor
// and as unguessable as the VLESS id beside it.
const xhttpPathBytes = 18

// newXhttpIdentity mints an XHTTP relay on the box's domain: a VLESS id and a
// secret path. stream-up by default — the edge terminates TLS itself, nothing
// buffers a streaming upload (plan 42 П4в, поправка 2).
func newXhttpIdentity(port, localPortBase int, domain string) (Identity, error) {
	uuid, err := randomUUID()
	if err != nil {
		return Identity{}, err
	}
	p := make([]byte, xhttpPathBytes)
	if _, err := rand.Read(p); err != nil {
		return Identity{}, err
	}
	return Identity{
		UUID:          uuid,
		Port:          port,
		LocalPortBase: localPortBase,
		Xhttp: &XhttpIdentity{
			Domain: domain,
			Path:   "/" + base64.RawURLEncoding.EncodeToString(p),
			Mode:   XhttpStreamUp,
		},
	}, nil
}

// randomUUID is a version-4 UUID, as `nekko-edge keygen` prints.
func randomUUID() (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}

// newIdentity mints a relay identity — the same keys `nekko-edge keygen`
// prints (edge/main.go).
func newIdentity(port, localPortBase int) (Identity, error) {
	priv := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(priv); err != nil {
		return Identity{}, err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return Identity{}, err
	}
	sid := make([]byte, 8)
	if _, err := rand.Read(sid); err != nil {
		return Identity{}, err
	}
	uuid, err := randomUUID()
	if err != nil {
		return Identity{}, err
	}
	enc := base64.RawURLEncoding
	return Identity{
		PrivateKey:    enc.EncodeToString(priv),
		PublicKey:     enc.EncodeToString(pub),
		ShortID:       hex.EncodeToString(sid),
		UUID:          uuid,
		Covers:        append([]string(nil), DefaultCovers...),
		Port:          port,
		LocalPortBase: localPortBase,
	}, nil
}

// withCovers returns the identity hiding behind a different set of cover
// sites, everything else untouched (roadmap 42 П9.6б, playbook rotate_dest).
//
// The keys deliberately stay: a dead cover site is somebody else's outage, and
// the relay's credentials were never shown to anyone because of it. Rotating
// them too would cost every holder its record for no reason.
//
// Refused on an empty set and on a relay with no Reality half. A Reality
// handshake has to borrow some real site's certificate, so "no covers" is not
// a configuration — it is a relay that cannot answer at all, and writing it
// would turn a dead cover into a dead relay.
func (id Identity) withCovers(covers []string) (Identity, error) {
	if id.proto() != ProtoVlessRealityVision {
		return Identity{}, fmt.Errorf("%w: релей говорит %s, прикрытий у него нет", ErrRejected, id.proto())
	}
	if len(covers) == 0 {
		return Identity{}, fmt.Errorf("%w: без прикрытия reality-релей не отвечает никому", ErrRejected)
	}
	out := id
	out.Covers = append([]string(nil), covers...)
	return out, nil
}

// rotatedReality returns the identity with fresh Reality credentials and the
// same everything else (roadmap 42 П9.6, playbook rotate_reality).
//
// What rotates is every per-relay secret the protocol shows on the wire: the
// x25519 pair, the short id and the VLESS user. What does not is the port, the
// loopback block and the cover sites — the port and the block are the box's
// layout, and changing covers is a different repair against a different cause
// (rotate_dest, П9.4). A censor that fingerprinted this relay learns nothing
// from the new one, and the machine is not touched, which is why this repair
// is free and starts on `auto`.
//
// Refused on a voice or XHTTP relay: they have no Reality half, and silently
// doing nothing would read as a successful repair.
func (id Identity) rotatedReality() (Identity, error) {
	if id.proto() != ProtoVlessRealityVision {
		return Identity{}, fmt.Errorf("%w: релей говорит %s, ключей reality у него нет", ErrRejected, id.proto())
	}
	fresh, err := newIdentity(id.Port, id.LocalPortBase)
	if err != nil {
		return Identity{}, err
	}
	fresh.Covers = append([]string(nil), id.Covers...)
	return fresh, nil
}

// EdgeEnv renders the identity as the env nekko-edge runs with (edge/config.go).
// Every relay is a VPN exit: what its tunnel carries leaves through the
// edge's own exit (edge/egress.go), so the env names no destination at all.
// CONTAINS SECRETS: goes to the box over ssh, 0600.
//
// Multi-line and JSON values travel base64-encoded: the env file is read both
// by systemd and by a shell (the swap script's check), and the two disagree
// about quotes.
func (id Identity) EdgeEnv() (map[string]string, error) {
	if h := id.Hysteria; h != nil {
		finalmask, err := json.Marshal(xrayFinalmask(h.Masks))
		if err != nil {
			return nil, err
		}
		b64 := base64.StdEncoding.EncodeToString
		return map[string]string{
			"EDGE_PROTO":        ProtoHysteria2,
			"EDGE_LISTEN":       "0.0.0.0:" + strconv.Itoa(id.Port),
			"EDGE_HY_AUTH":      h.Auth,
			"EDGE_HY_CERT":      b64([]byte(h.CertPEM)),
			"EDGE_HY_KEY":       b64([]byte(h.KeyPEM)),
			"EDGE_HY_FINALMASK": b64(finalmask),
			"EDGE_ECHO_PORT":    strconv.Itoa(id.echoPort()),
		}, nil
	}
	if x := id.Xhttp; x != nil {
		return map[string]string{
			"EDGE_PROTO":           ProtoVlessXhttp,
			"EDGE_LISTEN":          "0.0.0.0:" + strconv.Itoa(id.Port),
			"EDGE_XH_DOMAIN":       x.Domain,
			"EDGE_XH_PATH":         x.Path,
			"EDGE_CLIENTS":         id.UUID,
			"EDGE_LOCAL_PORT_BASE": strconv.Itoa(id.LocalPortBase),
		}, nil
	}
	if a := id.Awg; a != nil {
		return map[string]string{
			"EDGE_PROTO":             ProtoAmneziaWG,
			"EDGE_PORT":              strconv.Itoa(id.Port),
			"EDGE_AWG_PRIVATE_KEY":   a.ServerPrivateKey,
			"EDGE_AWG_PROBE_KEY":     a.ProbePublicKey,
			"EDGE_AWG_PROBE_ADDRESS": AwgProbeAddress,
			"EDGE_AWG_JC":            strconv.Itoa(a.Jc),
			"EDGE_AWG_JMIN":          strconv.Itoa(a.Jmin),
			"EDGE_AWG_JMAX":          strconv.Itoa(a.Jmax),
			"EDGE_AWG_S1":            strconv.Itoa(a.S1),
			"EDGE_AWG_S2":            strconv.Itoa(a.S2),
			"EDGE_AWG_H1":            a.H1,
			"EDGE_AWG_H2":            a.H2,
			"EDGE_AWG_H3":            a.H3,
			"EDGE_AWG_H4":            a.H4,
		}, nil
	}
	covers := ""
	for i, c := range id.Covers {
		if i > 0 {
			covers += ","
		}
		covers += c
	}
	env := map[string]string{
		"EDGE_PROTO":           ProtoVlessRealityVision,
		"EDGE_PRIVATE_KEY":     id.PrivateKey,
		"EDGE_SHORT_IDS":       id.ShortID,
		"EDGE_CLIENTS":         id.UUID,
		"EDGE_COVERS":          covers,
		"EDGE_LISTEN":          "0.0.0.0:" + strconv.Itoa(id.Port),
		"EDGE_LOCAL_PORT_BASE": strconv.Itoa(id.LocalPortBase),
		"EDGE_ECHO_PORT":       strconv.Itoa(id.echoPort()),
	}
	return env, nil
}

// xrayFinalmask renders masks as Xray's finalmask.udp list. The one Go
// renderer: the edge serves it as is (EDGE_HY_FINALMASK) and the prober dials
// with it; the client renders the same list from the signed record
// (nekko-transport-xray config.rs masks_json), and the two must agree byte
// for byte in meaning — edge/voice_test.go dials through one with the other.
func xrayFinalmask(masks []Mask) []map[string]any {
	span := func(r [2]int) string { return strconv.Itoa(r[0]) + "-" + strconv.Itoa(r[1]) }
	out := make([]map[string]any, 0, len(masks))
	for _, m := range masks {
		switch m.Type {
		case "salamander":
			out = append(out, map[string]any{"type": "salamander", "settings": map[string]any{"password": m.Password}})
		case "noise":
			junk := make([]map[string]any, len(m.Junk))
			for i, j := range m.Junk {
				junk[i] = map[string]any{"rand": span(j.Len), "delay": span(j.DelayMs)}
			}
			reset := [2]int{}
			if m.ResetSecs != nil {
				reset = *m.ResetSecs
			}
			out = append(out, map[string]any{"type": "noise", "settings": map[string]any{
				"reset": span(reset), "noise": junk,
			}})
		}
	}
	return out
}

// record is the signed record of this identity on a box at address. region is
// the box's voice region, "" on a stream box.
func (id Identity) record(name, address, region string, issuedAt, expiresAt int64) RelayRecord {
	if h := id.Hysteria; h != nil {
		return RelayRecord{
			Name:    name,
			Address: address,
			Port:    id.Port,
			Proto:   ProtoHysteria2,
			HysteriaParams: &HysteriaParams{
				Region:       region,
				Auth:         h.Auth,
				ServerName:   h.ServerName,
				CertSHA256:   h.CertSHA256,
				PubKeySHA256: publicKeyPin(h.CertPEM),
				Masks:        h.Masks,
			},
			IssuedAt:  issuedAt,
			ExpiresAt: expiresAt,
		}
	}
	if x := id.Xhttp; x != nil {
		return RelayRecord{
			Name:    name,
			Address: address,
			Port:    id.Port,
			Proto:   ProtoVlessXhttp,
			XhttpParams: &XhttpParams{
				Host: x.Domain,
				Path: x.Path,
				UUID: id.UUID,
				Mode: x.Mode,
			},
			IssuedAt:  issuedAt,
			ExpiresAt: expiresAt,
		}
	}
	if a := id.Awg; a != nil {
		return RelayRecord{
			Name:    name,
			Address: address,
			Port:    id.Port,
			Proto:   ProtoAmneziaWG,
			AwgParams: &AwgParams{
				ServerPublicKey:  a.ServerPublicKey,
				ClientPrivateKey: a.ProbePrivateKey,
				ClientAddress:    AwgProbeAddress,
				Jc:               a.Jc, Jmin: a.Jmin, Jmax: a.Jmax, S1: a.S1, S2: a.S2,
				H1: a.H1, H2: a.H2, H3: a.H3, H4: a.H4,
			},
			IssuedAt:  issuedAt,
			ExpiresAt: expiresAt,
		}
	}
	return RelayRecord{
		Name:    name,
		Address: address,
		Port:    id.Port,
		Proto:   ProtoVlessRealityVision,
		RealityParams: &RealityParams{
			PublicKey: id.PublicKey,
			UUID:      id.UUID,
			ShortID:   id.ShortID,
			Covers:    id.Covers,
		},
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
	}
}

// publicKeyPin is the hex SHA-256 of a PEM leaf's SubjectPublicKeyInfo, or
// "" when the PEM does not parse: the record then goes out without the
// sing-box pin, and the subscription leaves the relay to Xray clients.
func publicKeyPin(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}
