package main

// awg.go probes an AmneziaWG relay. Xray has no such outbound, so the vantage
// runs a real client in this process, on a TCP/IP stack of its own, and rides
// the tunnel to the same URL as every other protocol.
//
// A relay that does not answer gets the verdict a voice relay gets: unknown.
// UDP has no refusal to read, so silence cannot tell a censor from a lost path.
// The relay stays unadmitted (admission wants a pass), and an admitted one is
// not accused on it.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
)

// awgEdge is the hub's AwgParams, by contract on the JSON.
type awgEdge struct {
	ServerPublicKey  string `json:"server_public_key"`
	ClientPrivateKey string `json:"client_private_key"`
	ClientAddress    string `json:"client_address"`
	Jc               int    `json:"jc"`
	Jmin             int    `json:"jmin"`
	Jmax             int    `json:"jmax"`
	S1               int    `json:"s1"`
	S2               int    `json:"s2"`
	S3               int    `json:"s3"`
	S4               int    `json:"s4"`
	H1               string `json:"h1"`
	H2               string `json:"h2"`
	H3               string `json:"h3"`
	H4               string `json:"h4"`
	I1               string `json:"i1,omitempty"`
	// AmneziaWG 3.1 (internal/entry AwgV3); all empty on a 2.0 relay.
	HeaderProtectionKey    string `json:"header_protection_key,omitempty"`
	ContentPaddingAddition string `json:"content_padding_addition,omitempty"`
	RekeyAfterTime         string `json:"rekey_after_time,omitempty"`
	RekeyTimeout           string `json:"rekey_timeout,omitempty"`
	RejectAfterTime        string `json:"reject_after_time,omitempty"`
	KeepaliveTimeout       string `json:"keepalive_timeout,omitempty"`
	MaxHandshakeAttempts   string `json:"max_handshake_attempts,omitempty"`
	RandomTrailers         bool   `json:"random_trailers,omitempty"`
	DisableCookies         bool   `json:"disable_cookies,omitempty"`
}

const (
	awgProbeTries = 3
	awgProbeMTU   = 1280
)

// awgProbeDNS resolves the tunnel URL's name, through the tunnel.
var awgProbeDNS = netip.MustParseAddr("1.1.1.1")

// probeAwgEdge rides the tunnel. Each try is a new device, so a new source
// port: a path that loses some flows and not others (main.go) gets its chances.
func probeAwgEdge(ctx context.Context, e edge, tunnelURL string, k2 bool, now time.Time, timeout time.Duration) probeResult {
	res := probeResult{TS: now, RelayName: e.Name, K2Control: b2u8(k2), Detail: "awg"}
	var ok bool
	for try := 0; try < awgProbeTries && !ok; try++ {
		rtt, code, err := awgFetch(ctx, e, tunnelURL, timeout)
		ok = err == nil && tunnelAnswered(code)
		res.RealityRTTMs, res.RealityHTTP = rtt, uint16(code)
	}
	res.RealityOK = b2u8(ok)
	res.Verdict = classifyAwg(ok, k2)
	return res
}

func classifyAwg(ok, k2 bool) string {
	switch {
	case !k2:
		return verdictNetworkDown
	case ok:
		return verdictClean
	}
	return verdictUnknown
}

// awgConfig is the device's control text for dialling e.
func awgConfig(e edge) (string, error) {
	a := e.Awg
	priv, err := awgHex(a.ClientPrivateKey)
	if err != nil {
		return "", fmt.Errorf("client key: %w", err)
	}
	pub, err := awgHex(a.ServerPublicKey)
	if err != nil {
		return "", fmt.Errorf("server key: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\njc=%d\njmin=%d\njmax=%d\ns1=%d\ns2=%d\ns3=%d\ns4=%d\nh1=%s\nh2=%s\nh3=%s\nh4=%s\n",
		priv, a.Jc, a.Jmin, a.Jmax, a.S1, a.S2, a.S3, a.S4, a.H1, a.H2, a.H3, a.H4)
	if a.I1 != "" {
		fmt.Fprintf(&b, "i1=%s\n", a.I1)
	}
	if a.HeaderProtectionKey != "" {
		key, err := awgHex(a.HeaderProtectionKey)
		if err != nil {
			return "", fmt.Errorf("header protection key: %w", err)
		}
		fmt.Fprintf(&b, "header_protection_key=%s\n", key)
	}
	for _, r := range []struct{ key, value string }{
		{"content_padding_addition", a.ContentPaddingAddition},
		{"rekey_after_time", a.RekeyAfterTime},
		{"rekey_timeout", a.RekeyTimeout},
		{"reject_after_time", a.RejectAfterTime},
		{"keepalive_timeout", a.KeepaliveTimeout},
		{"max_handshake_attempts", a.MaxHandshakeAttempts},
	} {
		if r.value != "" {
			fmt.Fprintf(&b, "%s=%s\n", r.key, r.value)
		}
	}
	if a.RandomTrailers {
		b.WriteString("random_trailers=true\n")
	}
	if a.DisableCookies {
		b.WriteString("disable_cookies=true\n")
	}
	fmt.Fprintf(&b, "public_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\n",
		pub, net.JoinHostPort(e.Address, strconv.Itoa(e.Port)))
	return b.String(), nil
}

func awgHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("%d bytes, not 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// awgFetch brings a device up, fetches url through it and takes it down.
func awgFetch(ctx context.Context, e edge, url string, timeout time.Duration) (rttMs float32, code int, err error) {
	addr, err := netip.ParseAddr(e.Awg.ClientAddress)
	if err != nil {
		return 0, 0, fmt.Errorf("client address: %w", err)
	}
	cfg, err := awgConfig(e)
	if err != nil {
		return 0, 0, err
	}
	tunDev, tnet, err := netstack.CreateNetTUN([]netip.Addr{addr}, []netip.Addr{awgProbeDNS}, awgProbeMTU)
	if err != nil {
		return 0, 0, err
	}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf})
	defer dev.Close()
	if err := dev.IpcSet(cfg); err != nil {
		return 0, 0, err
	}
	if err := dev.Up(); err != nil {
		return 0, 0, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	client := &http.Client{
		Transport:     &http.Transport{DialContext: tnet.DialContext, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return float32(time.Since(start).Milliseconds()), resp.StatusCode, nil
}
