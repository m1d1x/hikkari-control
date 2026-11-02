package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/netstack"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	"golang.org/x/crypto/curve25519"
)

func awgKeyPair(t *testing.T) (priv, pub string) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	raw[0] &= 248
	raw[31] = raw[31]&127 | 64
	p, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(p)
}

var testObf = AwgObfuscation{Jc: 4, Jmin: 40, Jmax: 70, S1: 33, S2: 57, H1: "1234567", H2: "2345678", H3: "3456789", H4: "4567890"}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// awgClient is a real AmneziaWG client, on a stack of its own in the test.
func awgClient(t *testing.T, serverPub string, port int, addr string, obf AwgObfuscation, priv string) *netstack.Net {
	t.Helper()
	tunDev, tnet, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr(addr)}, nil, awgMTU)
	if err != nil {
		t.Fatal(err)
	}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf})
	t.Cleanup(dev.Close)
	privHex, _ := awgKeyHex(priv)
	pubHex, _ := awgKeyHex(serverPub)
	cfg := fmt.Sprintf("private_key=%s\njc=%d\njmin=%d\njmax=%d\ns1=%d\ns2=%d\nh1=%s\nh2=%s\nh3=%s\nh4=%s\npublic_key=%s\nendpoint=127.0.0.1:%d\nallowed_ip=0.0.0.0/0\npersistent_keepalive_interval=1\n",
		privHex, obf.Jc, obf.Jmin, obf.Jmax, obf.S1, obf.S2, obf.H1, obf.H2, obf.H3, obf.H4, pubHex, port)
	if err := dev.IpcSet(cfg); err != nil {
		t.Fatal(err)
	}
	if err := dev.Up(); err != nil {
		t.Fatal(err)
	}
	return tnet
}

// awgFarSite is where a test's client thinks it goes: an address on the far
// side of the stack, as a client's traffic always is.
const awgFarSite = "198.51.100.7"

// dialTo sends every flow to one local address: the tests of the device itself.
func dialTo(addr string) awgDialer {
	return func(ctx context.Context, dest xnet.Destination) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, dest.Network.SystemString(), addr)
	}
}

func startTestAwg(t *testing.T, dial awgDialer) (s *awgServer, serverPub string, port int) {
	t.Helper()
	serverPriv, serverPub := awgKeyPair(t)
	port = freeUDPPort(t)
	s, err := startAwg(context.Background(), AwgConfig{PrivateKey: serverPriv, Port: port, Obf: testObf}, dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, serverPub, port
}

func awgHTTP(tnet *netstack.Net, url string, timeout time.Duration) (string, error) {
	client := http.Client{Timeout: timeout, Transport: &http.Transport{DialContext: tnet.DialContext}}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// A subscriber on the hub's list gets through, is counted under their key, and
// is cut off when the list no longer names them. Another peer's session is not
// touched by that.
func TestAwgPeersFollowTheHubsList(t *testing.T) {
	site := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "через туннель") })}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go site.Serve(ln)
	url := "http://" + awgFarSite + ":8080/"

	s, serverPub, port := startTestAwg(t, dialTo(ln.Addr().String()))
	alicePriv, alicePub := awgKeyPair(t)
	bobPriv, bobPub := awgKeyPair(t)
	alice := awgClient(t, serverPub, port, "10.66.0.2", testObf, alicePriv)
	bob := awgClient(t, serverPub, port, "10.66.0.3", testObf, bobPriv)

	if _, err := awgHTTP(alice, url, 3*time.Second); err == nil {
		t.Fatal("a key the hub never listed got through")
	}
	if err := s.Set(context.Background(), []string{alicePub + " 10.66.0.2", bobPub + " 10.66.0.3"}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*netstack.Net{"alice": alice, "bob": bob} {
		got, err := awgHTTP(c, url, 20*time.Second)
		if err != nil || got != "через туннель" {
			t.Fatalf("%s through the tunnel: %q, %v", name, got, err)
		}
	}

	_, _, conns, keys := s.totals(time.Now())
	if conns != 2 {
		t.Errorf("connected peers = %d, want 2", conns)
	}
	if keys[alicePub].Up == 0 || keys[alicePub].Down == 0 {
		t.Errorf("alice's traffic is not counted under her key: %+v", keys)
	}

	if err := s.Set(context.Background(), []string{bobPub + " 10.66.0.3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := awgHTTP(alice, url, 3*time.Second); err == nil {
		t.Fatal("a key off the hub's list still gets through")
	}
	if got, err := awgHTTP(bob, url, 20*time.Second); err != nil || got != "через туннель" {
		t.Fatalf("bob lost his session when alice was removed: %q, %v", got, err)
	}
}

// Without the matching obfuscation, a client is not answered at all: that is
// what the parameters are for.
func TestAwgRefusesPlainWireGuardHandshakes(t *testing.T) {
	s, serverPub, port := startTestAwg(t, dialTo("127.0.0.1:1"))
	priv, pub := awgKeyPair(t)
	if err := s.Set(context.Background(), []string{pub + " 10.66.0.2"}); err != nil {
		t.Fatal(err)
	}
	wrong := testObf
	wrong.H1 = "7654321"
	client := awgClient(t, serverPub, port, "10.66.0.2", wrong, priv)
	if _, err := awgHTTP(client, "http://"+awgFarSite+":8080/", 3*time.Second); err == nil {
		t.Fatal("expected no connection")
	}
	if stats, _ := s.stats(); !stats[pub].lastHandshake.IsZero() {
		t.Fatal("a handshake with the wrong headers completed")
	}
}

// A flow from the tunnel enters Xray's router as the tunnel's inbound, so what a
// VPN exit must never reach is refused by the same rules as on every other
// protocol. The one destination those rules allow on the box proves the path
// is open and that a refusal is the rules at work.
func TestAwgFlowsLeaveThroughTheEgressRules(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			c.Close()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	echo := freeUDPPort(t)
	if err := serveEcho(ctx, echo); err != nil {
		t.Fatal(err)
	}
	serverPriv, _ := awgKeyPair(t)
	doc := AwgConfig{PrivateKey: serverPriv, Port: 1, Obf: testObf}.XrayConfig()
	// The admission echo is the one loopback port the rules can allow, and
	// this protocol has no use for one: a test's way to see the path open.
	doc["outbounds"].([]any)[0] = directOutbound(echo)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	xc, err := core.LoadConfig("json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("xray config: %v", err)
	}
	inst, err := core.New(xc)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	dial := xrayDialer(inst)

	c, err := dial(ctx, xnet.UDPDestination(xnet.LocalHostIP, xnet.Port(echo)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, err := c.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), "ping") {
		t.Fatalf("the echo the rules allow did not answer: %q, %v", buf[:n], err)
	}

	port := xnet.Port(ln.Addr().(*net.TCPAddr).Port)
	if c, err := dial(ctx, xnet.TCPDestination(xnet.LocalHostIP, port)); err == nil {
		c.SetDeadline(time.Now().Add(2 * time.Second))
		c.Write([]byte("x"))
		c.Read(make([]byte, 1))
		c.Close()
	}
	select {
	case <-accepted:
		t.Fatal("a flow from the tunnel reached a service on the box's loopback")
	case <-time.After(time.Second):
	}
}

func TestParseAwgStats(t *testing.T) {
	_, pub := awgKeyPair(t)
	h, _ := awgKeyHex(pub)
	got := parseAwgStats("private_key=aa\nlisten_port=1\npublic_key=" + h + "\nrx_bytes=10\ntx_bytes=20\nlast_handshake_time_sec=5\nlast_handshake_time_nsec=0\n")
	if st := got[pub]; st.up != 10 || st.down != 20 || st.lastHandshake.Unix() != 5 {
		t.Fatalf("%+v", got)
	}
}

func TestAwgConfigFromEnvNamesWhatIsWrong(t *testing.T) {
	priv, pub := awgKeyPair(t)
	env := map[string]string{
		"EDGE_AWG_PRIVATE_KEY": priv, "EDGE_PORT": "51820", "EDGE_AWG_PROBE_KEY": pub, "EDGE_AWG_PROBE_ADDRESS": "10.66.0.1",
		"EDGE_AWG_JC": "4", "EDGE_AWG_JMIN": "40", "EDGE_AWG_JMAX": "70", "EDGE_AWG_S1": "33", "EDGE_AWG_S2": "57",
		"EDGE_AWG_H1": "1", "EDGE_AWG_H2": "2", "EDGE_AWG_H3": "3", "EDGE_AWG_H4": "4",
	}
	get := func(k string) string { return env[k] }
	if _, err := AwgConfigFromEnv(get); err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]string{"EDGE_AWG_PRIVATE_KEY": "abc", "EDGE_PORT": "0", "EDGE_AWG_JC": "0", "EDGE_AWG_JMAX": "10", "EDGE_AWG_H3": "", "EDGE_AWG_PROBE_KEY": "nope", "EDGE_AWG_PROBE_ADDRESS": "nope"} {
		was := env[key]
		env[key] = bad
		if _, err := AwgConfigFromEnv(get); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s=%q: %v", key, bad, err)
		}
		env[key] = was
	}
}
