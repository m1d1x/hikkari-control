package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/core"
)

const (
	testDomain = "notes.example-relay.xyz"
	testPath   = "/k7Qp2_Zx9-mWv3Lr"
	testClient = "00000000-0000-4000-8000-000000000003"
)

func xhttpEnv(t *testing.T, port, local int) (map[string]string, string) {
	t.Helper()
	cert, key, pin := leafFor(t, testDomain)
	b64 := base64.StdEncoding.EncodeToString
	return map[string]string{
		"EDGE_PROTO":           protoXhttp,
		"EDGE_LISTEN":          "127.0.0.1:" + strconv.Itoa(port),
		"EDGE_XH_DOMAIN":       testDomain,
		"EDGE_XH_PATH":         testPath,
		"EDGE_CLIENTS":         testClient,
		"EDGE_LOCAL_PORT_BASE": strconv.Itoa(local),
		"EDGE_XH_CERT":         b64([]byte(cert)),
		"EDGE_XH_KEY":          b64([]byte(key)),
	}, pin
}

func TestAnXhttpEdgeReportsEveryProblemAtOnce(t *testing.T) {
	_, err := XhttpConfigFromEnv(env(map[string]string{"EDGE_XH_CERT": "x"}))
	if err == nil {
		t.Fatal("empty env must fail")
	}
	for _, name := range []string{"EDGE_XH_DOMAIN", "EDGE_XH_PATH", "EDGE_CLIENTS", "EDGE_XH_CERT"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	m, _ := xhttpEnv(t, 443, 10001)
	for what, edit := range map[string]func(map[string]string){
		"ip as domain":     func(m map[string]string) { m["EDGE_XH_DOMAIN"] = "203.0.113.9" },
		"short path":       func(m map[string]string) { m["EDGE_XH_PATH"] = "/short" },
		"empty segment":    func(m map[string]string) { m["EDGE_XH_PATH"] = "/k7Qp2_Zx9//mWv3Lr" },
		"query in path":    func(m map[string]string) { m["EDGE_XH_PATH"] = testPath + "?a=1" },
		"cert without key": func(m map[string]string) { delete(m, "EDGE_XH_KEY") },
	} {
		mm := map[string]string{}
		for k, v := range m {
			mm[k] = v
		}
		edit(mm)
		if _, err := XhttpConfigFromEnv(env(mm)); err == nil {
			t.Errorf("accepted: %s", what)
		}
	}
	// The secret never lands in an error message.
	m["EDGE_XH_PATH"] = "/secret-but-wrong/../x"
	if _, err := XhttpConfigFromEnv(env(m)); err == nil || strings.Contains(err.Error(), "secret-but-wrong") {
		t.Fatalf("path error: %v", err)
	}
}

func TestOnlyTheSecretPathAndBelowIsTheTunnel(t *testing.T) {
	c := XhttpConfig{Path: testPath}
	for p, want := range map[string]bool{
		testPath:                   true,
		testPath + "/":             true,
		testPath + "/abc/0":        true,
		testPath + "x":             false,
		"/":                        false,
		testPath[:len(testPath)-1]: false,
	} {
		if got := c.tunnelPath(p); got != want {
			t.Errorf("tunnelPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// tcpEcho echoes every connection on 127.0.0.1:port, prefixed with tag.
func tcpEcho(t *testing.T, port int, tag string) {
	t.Helper()
	tcpEchoOn(t, "127.0.0.1", port, tag)
}

// tcpEchoOn is tcpEcho on one address of the machine.
func tcpEchoOn(t *testing.T, host string, port int, tag string) {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := io.WriteString(c, tag+":"+line); err != nil {
						return
					}
				}
			}()
		}
	}()
}

// startXhttpEdge brings up the edge the way run() does: Xray, then the front.
func startXhttpEdge(t *testing.T, m map[string]string, sites ...int) (XhttpConfig, *core.Instance) {
	t.Helper()
	c, err := XhttpConfigFromEnv(env(m))
	if err != nil {
		t.Fatal(err)
	}
	doc := c.XrayConfig()
	asInternet(t, doc, "tcp", sites...)
	inst := startXray(t, doc)
	if err := serveFront(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	return c, inst
}

// xhttpClient is a real Xray client: VLESS over XHTTP over TLS to the edge,
// the certificate pinned, SNI and Host the relay's domain while the dial goes
// to the address — what the app does with a signed record. One local door per
// destination.
func xhttpClient(t *testing.T, edgePort int, pin, mode string, dests map[string]int) map[string]int {
	t.Helper()
	return xhttpClientAs(t, testClient, edgePort, pin, mode, dests)
}

// xhttpClientAs is xhttpClient dialing as user id.
func xhttpClientAs(t *testing.T, id string, edgePort int, pin, mode string, dests map[string]int) map[string]int {
	t.Helper()
	targets := map[string]string{}
	for name, port := range dests {
		// The address, not "localhost": the echoes listen on IPv4 only, and an
		// exit that resolved localhost to ::1 would find nobody there.
		targets[name] = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	return xhttpClientTo(t, id, edgePort, pin, mode, targets)
}

// xhttpClientTo is the client with one local door per "host:port", the host
// an address or a name for the exit to resolve.
func xhttpClientTo(t *testing.T, id string, edgePort int, pin, mode string, targets map[string]string) map[string]int {
	t.Helper()
	doors := map[string]int{}
	var inbounds []map[string]any
	for name, target := range targets {
		host, portText, err := net.SplitHostPort(target)
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			t.Fatal(err)
		}
		door := freePort(t, "tcp")
		doors[name] = door
		inbounds = append(inbounds, map[string]any{
			"tag": "door-" + name, "listen": "127.0.0.1", "port": door, "protocol": "dokodemo-door",
			"settings": map[string]any{"address": host, "port": port, "network": "tcp"},
		})
	}
	startXray(t, map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": inbounds,
		"outbounds": []map[string]any{{
			"protocol": "vless",
			"settings": map[string]any{"vnext": []map[string]any{{
				"address": "127.0.0.1", "port": edgePort,
				"users": []map[string]any{{"id": id, "encryption": "none"}},
			}}},
			"streamSettings": map[string]any{
				"network":       "xhttp",
				"security":      "tls",
				"xhttpSettings": map[string]any{"host": testDomain, "path": testPath, "mode": mode},
				"tlsSettings": map[string]any{
					"serverName":           testDomain,
					"fingerprint":          "chrome",
					"alpn":                 []string{"h2", "http/1.1"},
					"pinnedPeerCertSha256": pin,
				},
			},
		}},
	})
	return doors
}

// talk sends a line through door and returns the reply, or "" when the tunnel
// gives nothing back within wait.
func talk(t *testing.T, door int, wait time.Duration) string {
	t.Helper()
	c, err := net.DialTimeout("tcp4", "127.0.0.1:"+strconv.Itoa(door), wait)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(wait))
	if _, err := io.WriteString(c, "ping\n"); err != nil {
		return ""
	}
	line, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line)
}

// The DoD of the edge in one process: every XHTTP mode carries a stream to a
// site through the secret path, a port on the box itself gets nothing, and
// everything off the path is a plain site — not an Xray error.
func TestAnXhttpEdgeTunnelsUnderTheSecretPathAndIsASiteElsewhere(t *testing.T) {
	edgePort, local := freePort(t, "tcp"), freeXhttpLocalPort(t)
	sitePort, strangerPort := freePort(t, "tcp"), freePort(t, "tcp")
	tcpEcho(t, sitePort, "site")
	tcpEcho(t, strangerPort, "stranger")
	m, pin := xhttpEnv(t, edgePort, local)
	_, _ = startXhttpEdge(t, m, sitePort)

	for _, mode := range []string{"packet-up", "stream-up", "stream-one"} {
		t.Run(mode, func(t *testing.T) {
			doors := xhttpClient(t, edgePort, pin, mode, map[string]int{"site": sitePort, "stranger": strangerPort})
			if got := talk(t, doors["site"], 5*time.Second); got != "site:ping" {
				t.Fatalf("through the tunnel: %q", got)
			}
			if got := talk(t, doors["stranger"], 2*time.Second); got != "" {
				t.Fatalf("a port on the box answered: %q", got)
			}
		})
	}

	// A browser, a scanner, an active probe: no secret, no tunnel.
	hc := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, ServerName: testDomain},
		},
	}
	base := "https://127.0.0.1:" + strconv.Itoa(edgePort)
	get := func(path string) (int, string, string) {
		t.Helper()
		resp, err := hc.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Proto, string(body)
	}
	if code, proto, body := get("/"); code != 200 || proto != "HTTP/2.0" || !strings.Contains(body, "<title>") {
		t.Fatalf("home: %d %s %q", code, proto, body)
	}
	for _, p := range []string{"/wp-login.php", testPath + "x", "/k7Qp2"} {
		if code, _, body := get(p); code != 404 || !strings.Contains(body, "Not Found") {
			t.Fatalf("%s: %d %q", p, code, body)
		}
	}
	resp, err := hc.Post(base+"/", "text/plain", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /: %d", resp.StatusCode)
	}

	// A ClientHello without SNI still gets the domain's certificate.
	conn, err := tls.Dial("tcp4", "127.0.0.1:"+strconv.Itoa(edgePort), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if names := conn.ConnectionState().PeerCertificates[0].DNSNames; len(names) != 1 || names[0] != testDomain {
		t.Fatalf("no-SNI certificate: %v", names)
	}
}

// EDGE_XH_FRONT decides who terminates TLS on the public port, and that sets
// where the edge listens: its own :443 by default, a loopback port when a proxy
// is in front.
func TestEDGEXHFrontChoosesWhoTerminatesTLS(t *testing.T) {
	m, _ := xhttpEnv(t, 0, 10001)
	delete(m, "EDGE_LISTEN")

	c, err := XhttpConfigFromEnv(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.Front != frontTLS || c.Listen != "0.0.0.0:443" {
		t.Fatalf("default: front %q listen %q, want %q and the public port", c.Front, c.Listen, frontTLS)
	}

	m["EDGE_XH_FRONT"] = "plaintext"
	c, err = XhttpConfigFromEnv(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.Front != frontPlaintext || c.Listen != "127.0.0.1:8443" {
		t.Fatalf("plaintext: front %q listen %q, want loopback", c.Front, c.Listen)
	}

	// A plaintext front needs no certificate of its own: the proxy owns it.
	delete(m, "EDGE_XH_CERT")
	delete(m, "EDGE_XH_KEY")
	if _, err := XhttpConfigFromEnv(env(m)); err != nil {
		t.Fatalf("plaintext without a cert: %v", err)
	}

	// An explicit listen is kept.
	m["EDGE_LISTEN"] = "127.0.0.1:9000"
	if c, err := XhttpConfigFromEnv(env(m)); err != nil || c.Listen != "127.0.0.1:9000" {
		t.Fatalf("explicit listen: %q %v", c.Listen, err)
	}

	m["EDGE_XH_FRONT"] = "caddy"
	if _, err := XhttpConfigFromEnv(env(m)); err == nil || !strings.Contains(err.Error(), "EDGE_XH_FRONT") {
		t.Fatalf("unknown front: %v", err)
	}
}

// tlsFrontProxy stands in for nginx: it terminates TLS on its own port with the
// domain's certificate and forwards every request to the edge's loopback over
// h2c, streaming both ways (nginx's proxy_buffering off).
func tlsFrontProxy(t *testing.T, edgeAddr string) (int, string) {
	t.Helper()
	cert, key, pin := leafFor(t, testDomain)
	crt, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	target := &url.URL{Scheme: "http", Host: edgeAddr}
	protos := new(http.Protocols)
	protos.SetUnencryptedHTTP2(true)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
		},
		Transport: &http.Transport{
			Protocols:           protos,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
		FlushInterval: -1,
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &http.Server{Handler: rp, TLSConfig: &tls.Config{Certificates: []tls.Certificate{crt}}}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port, pin
}

// With a proxy in front, the edge serves the tunnel in cleartext on loopback and
// the client's XHTTP still reaches a site through the secret path. This is the
// fronted shape a real nginx gives on a node (H10); the proxy here is the stand
// -in, nginx itself is verified on the box.
func TestAPlaintextFrontTunnelsBehindATLSProxy(t *testing.T) {
	edgePort, local := freePort(t, "tcp"), freeXhttpLocalPort(t)
	sitePort := freePort(t, "tcp")
	tcpEcho(t, sitePort, "site")

	m, _ := xhttpEnv(t, edgePort, local)
	delete(m, "EDGE_XH_CERT")
	delete(m, "EDGE_XH_KEY")
	m["EDGE_XH_FRONT"] = "plaintext"
	c, err := XhttpConfigFromEnv(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.Front != frontPlaintext {
		t.Fatalf("front %q", c.Front)
	}
	doc := c.XrayConfig()
	asInternet(t, doc, "tcp", sitePort)
	startXray(t, doc)
	if err := serveFront(t.Context(), c); err != nil {
		t.Fatal(err)
	}

	frontPort, pin := tlsFrontProxy(t, c.Listen)
	// stream-one is a single full-duplex POST a half-duplex proxy cannot carry;
	// it is measured on the node, where nginx's behaviour is the real answer.
	for _, mode := range []string{"packet-up", "stream-up"} {
		t.Run(mode, func(t *testing.T) {
			doors := xhttpClient(t, frontPort, pin, mode, map[string]int{"site": sitePort})
			if got := talk(t, doors["site"], 5*time.Second); got != "site:ping" {
				t.Fatalf("through the fronted tunnel: %q", got)
			}
		})
	}
}

func TestTheBuiltInDecoyDiffersByDomain(t *testing.T) {
	a, b := decoyPage("alpha.example.xyz"), decoyPage("bravo.example.top")
	if a == b {
		t.Fatal("two domains drew the same page")
	}
	if decoyPage("alpha.example.xyz") != a {
		t.Fatal("the page is not stable for one domain")
	}
	if strings.Contains(decoyPage("<x>.example.xyz"), "<x>") {
		t.Fatal("the domain is not escaped")
	}
}

// What a scanner reads from the decoy must be what nginx would say, not Go.
func TestTheDecoyAnswersLikeNginx(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>home</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes", "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, site := range map[string]http.Handler{"built-in": decoy("alpha.example.xyz", ""), "site dir": decoy("alpha.example.xyz", dir)} {
		t.Run(name, func(t *testing.T) {
			get := func(method, path string) *http.Response {
				rec := httptest.NewRecorder()
				site.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
				return rec.Result()
			}
			home := get(http.MethodGet, "/")
			if home.StatusCode != 200 || home.Header.Get("Server") != "nginx" {
				t.Fatalf("home: %d %v", home.StatusCode, home.Header)
			}
			for _, c := range []struct {
				method, path string
				code         int
				title        string
			}{
				{http.MethodGet, "/nope", 404, "404 Not Found"},
				{http.MethodGet, "/notes/", 404, "404 Not Found"}, // no listing
				{http.MethodPost, "/", 405, "405 Not Allowed"},
			} {
				if name == "built-in" && c.path == "/notes/" {
					continue
				}
				res := get(c.method, c.path)
				body, _ := io.ReadAll(res.Body)
				if res.StatusCode != c.code {
					t.Fatalf("%s %s: %d", c.method, c.path, res.StatusCode)
				}
				if !strings.Contains(string(body), "<center><h1>"+c.title+"</h1></center>\r\n<hr><center>nginx</center>") {
					t.Fatalf("%s %s: %q", c.method, c.path, body)
				}
				if res.Header.Get("Server") != "nginx" || res.Header.Get("Content-Type") != "text/html" {
					t.Fatalf("%s %s: %v", c.method, c.path, res.Header)
				}
				if res.Header.Get("X-Content-Type-Options") != "" || res.Header.Get("Allow") != "" {
					t.Fatalf("%s %s: Go's own headers: %v", c.method, c.path, res.Header)
				}
			}
		})
	}
}

func freeXhttpLocalPort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 128; attempt++ {
		port := freePort(t, "tcp")
		if port <= 65000 {
			return port
		}
	}
	t.Fatal("no free XHTTP local port within 1024..65000")
	return 0
}
