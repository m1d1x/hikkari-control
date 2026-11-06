package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// An XHTTP edge (plan 42 П4в; EDGE_PROTO=vless-xhttp) is an ordinary HTTPS
// site on the relay's own domain with a tunnel under one secret path.
//
// By default the edge terminates TLS itself — a real certificate, Let's Encrypt
// over TLS-ALPN-01 on the same :443 — so there is no nginx on the box (П5: one
// binary) and nothing on the path buffers a streaming upload:
//
//	:443 TLS (own domain, real cert) ─► net/http
//	  ├─ <secret path>/…  ─► reverse proxy, h2c ─► Xray XHTTP inbound 127.0.0.1:<base>
//	  │                                              └─ VLESS user ─► rule by destination
//	  │                                                   └─ direct (egress.go)
//	  └─ anything else     ─► the decoy site
//
// EDGE_XH_FRONT=plaintext puts a real reverse proxy (nginx) in front instead:
// the proxy terminates TLS on :443, owns the certificate and serves the cover
// site, and forwards the secret path to the edge, which then listens in
// cleartext on a loopback EDGE_LISTEN and keeps only the tunnel:
//
//	nginx :443 TLS ─► <secret path>/… ─► 127.0.0.1:8443 cleartext (h2c / HTTP/1.1)
//	                                       └─ this edge's reverse proxy ─► Xray
//
// A node whose address must look lived-in (H10) takes this front: a genuine
// nginx and a real site, as zovfy has, in place of the Go front's `Server:
// nginx` on a placeholder page.
//
// A prober without the path sees a small site and its 404s, never an Xray
// error: the path, not the protocol, is what tells the tunnel apart.

// XhttpConfig is everything one XHTTP edge needs, read from the environment.
type XhttpConfig struct {
	// Listen is the public address, normally 0.0.0.0:443.
	Listen string
	// Domain is the relay's own domain: the certificate's name.
	Domain string
	// Path is the secret path the tunnel lives under. A credential.
	Path string
	// Clients are the VLESS user ids allowed through.
	Clients []string
	// LocalPort is the loopback port of the Xray XHTTP inbound.
	LocalPort int
	// CertPEM / KeyPEM: a certificate issued elsewhere. Empty means ACME.
	CertPEM, KeyPEM string
	// ACMEDir caches the account key and certificates across restarts, so a
	// restart does not cost a Let's Encrypt order (they are rate limited).
	ACMEDir string
	// ACMEEmail is the optional contact on the ACME account.
	ACMEEmail string
	// ACMEURL overrides the directory, e.g. Let's Encrypt staging.
	ACMEURL string
	// SiteDir, when set, is served as the decoy site instead of the built-in
	// page.
	SiteDir string
	// LogLevel is Xray's loglevel.
	LogLevel string
	// Front says who terminates TLS on the public port: the edge itself
	// (frontTLS, the default) or a reverse proxy ahead of it (frontPlaintext),
	// in which case the edge listens in cleartext on a loopback EDGE_LISTEN and
	// the proxy owns the certificate and the cover site.
	Front string
}

// Front values for EDGE_XH_FRONT: who terminates TLS on the public port.
const (
	// frontTLS: the edge terminates TLS itself on EDGE_LISTEN (one binary, no
	// nginx). The default.
	frontTLS = "tls"
	// frontPlaintext: a reverse proxy (nginx) terminates TLS ahead of the edge
	// and forwards the secret path to it; the edge serves the same handler in
	// cleartext on a loopback EDGE_LISTEN. The certificate and the cover site
	// belong to the proxy.
	frontPlaintext = "plaintext"
)

// minSecretPathLen and secretPathRe mirror nekko-entry record.rs
// check_secret_path: the client refuses any other shape.
const minSecretPathLen = 17

var secretPathRe = regexp.MustCompile(`^(/[A-Za-z0-9_-]+)+$`)

// XhttpConfigFromEnv reads the EDGE_* variables of an XHTTP edge. Every problem
// is reported at once, so a broken env file is fixed in one pass.
func XhttpConfigFromEnv(getenv func(string) string) (XhttpConfig, error) {
	c := XhttpConfig{
		Listen:    getenv("EDGE_LISTEN"),
		Domain:    strings.ToLower(strings.TrimSpace(getenv("EDGE_XH_DOMAIN"))),
		Path:      getenv("EDGE_XH_PATH"),
		Clients:   list(getenv("EDGE_CLIENTS")),
		ACMEDir:   getenv("EDGE_ACME_DIR"),
		ACMEEmail: getenv("EDGE_ACME_EMAIL"),
		ACMEURL:   getenv("EDGE_ACME_URL"),
		SiteDir:   getenv("EDGE_SITE_DIR"),
		LogLevel:  getenv("EDGE_LOG_LEVEL"),
		Front:     strings.ToLower(strings.TrimSpace(getenv("EDGE_XH_FRONT"))),
	}
	if c.Front == "" {
		c.Front = frontTLS
	}
	if c.Listen == "" {
		// Behind a proxy the edge is never the public port: it listens on
		// loopback, where only the proxy reaches it.
		if c.Front == frontPlaintext {
			c.Listen = "127.0.0.1:8443"
		} else {
			c.Listen = "0.0.0.0:443"
		}
	}
	if c.LogLevel == "" {
		c.LogLevel = "warning"
	}
	if c.ACMEDir == "" {
		// systemd's StateDirectory (the hub's unit sets one per relay): the
		// only writable place under ProtectSystem=strict and DynamicUser.
		if state := getenv("STATE_DIRECTORY"); state != "" {
			c.ACMEDir = filepath.Join(state, "acme")
		} else {
			c.ACMEDir = "/var/lib/nekko-edge/acme"
		}
	}
	var errs []error
	if c.Domain == "" || net.ParseIP(c.Domain) != nil || !strings.Contains(c.Domain, ".") {
		errs = append(errs, fmt.Errorf("EDGE_XH_DOMAIN: want the relay's domain name, got %q", c.Domain))
	}
	if len(c.Path) < minSecretPathLen || len(c.Path) > 128 || !secretPathRe.MatchString(c.Path) {
		// The path is a secret: never echo it.
		errs = append(errs, fmt.Errorf("EDGE_XH_PATH: want /[A-Za-z0-9_-] segments, %d..128 long", minSecretPathLen))
	}
	if len(c.Clients) == 0 {
		errs = append(errs, errors.New("EDGE_CLIENTS is empty"))
	}
	if c.Front != frontTLS && c.Front != frontPlaintext {
		errs = append(errs, fmt.Errorf("EDGE_XH_FRONT: want %q or %q, got %q", frontTLS, frontPlaintext, c.Front))
	}

	c.LocalPort = 10001
	if v := getenv("EDGE_LOCAL_PORT_BASE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1024 || n > 65000 {
			errs = append(errs, fmt.Errorf("EDGE_LOCAL_PORT_BASE: want 1024..65000, got %q", v))
		}
		c.LocalPort = n
	}
	cert, key := getenv("EDGE_XH_CERT"), getenv("EDGE_XH_KEY")
	if (cert == "") != (key == "") {
		errs = append(errs, errors.New("EDGE_XH_CERT and EDGE_XH_KEY come together"))
	}
	if cert != "" && key != "" {
		pc, err1 := base64.StdEncoding.DecodeString(cert)
		pk, err2 := base64.StdEncoding.DecodeString(key)
		if err1 != nil || err2 != nil {
			errs = append(errs, errors.New("EDGE_XH_CERT / EDGE_XH_KEY are not base64"))
		} else if _, err := tls.X509KeyPair(pc, pk); err != nil {
			errs = append(errs, fmt.Errorf("EDGE_XH_CERT / EDGE_XH_KEY: %w", err))
		} else {
			c.CertPEM, c.KeyPEM = string(pc), string(pk)
		}
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("EDGE_LISTEN: %w", err))
	}
	return c, errors.Join(errs...)
}

// xhttpTag is an XHTTP edge's one tunnel inbound.
const xhttpTag = "xhttp"

// XrayConfig renders the Xray JSON document of the tunnel half: an XHTTP
// inbound on loopback, plaintext (TLS ends in the front), reachable only
// through the front's secret path.
func (c XhttpConfig) XrayConfig() map[string]any {
	clients := make([]map[string]any, 0, len(c.Clients))
	for _, id := range c.Clients {
		clients = append(clients, map[string]any{"id": id})
	}
	const tag = xhttpTag
	doc := map[string]any{
		"log": map[string]any{"loglevel": c.LogLevel},
		"inbounds": []map[string]any{{
			"tag":      tag,
			"listen":   "127.0.0.1",
			"port":     c.LocalPort,
			"protocol": "vless",
			"settings": map[string]any{
				"clients":    clients,
				"decryption": "none",
			},
			"streamSettings": map[string]any{
				"network": "xhttp",
				// Every mode is accepted: the record names the one a client
				// uses, and the comparative run (П4б) tries them all on one
				// relay.
				"xhttpSettings": map[string]any{"path": c.Path, "mode": "auto"},
			},
			"sniffing": tunnelSniffing(),
		}},
		"outbounds": []map[string]any{
			// First outbound is Xray's default: the drop, as on a Reality edge.
			{"tag": "drop", "protocol": "blackhole"},
			directOutbound(0),
		},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules":          egressRules([]string{tag}),
		},
	}
	withStats(doc)
	return doc
}

// tunnelPath: the request belongs to the tunnel — the secret path itself or
// anything under it. A path that merely starts with the same characters does
// not.
func (c XhttpConfig) tunnelPath(p string) bool {
	return p == c.Path || strings.HasPrefix(p, c.Path+"/")
}

// handler is the whole site: the tunnel under the secret path, the decoy
// everywhere else.
func (c XhttpConfig) handler() http.Handler {
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", c.LocalPort)}
	// Xray's XHTTP inbound speaks h2c with prior knowledge; one multiplexed
	// connection carries every client's requests.
	protos := new(http.Protocols)
	protos.SetUnencryptedHTTP2(true)
	tunnel := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
		},
		Transport: &http.Transport{
			Protocols:           protos,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
		// Stream both ways: a buffered response would hold the download back.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !errors.Is(err, context.Canceled) {
				log.Printf("nekko-edge: tunnel: %v", err)
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	site := decoy(c.Domain, c.SiteDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.tunnelPath(r.URL.Path) {
			site.ServeHTTP(w, r)
			return
		}
		if r.ProtoMajor == 1 {
			// stream-one over HTTP/1.1 uploads while it downloads.
			_ = http.NewResponseController(w).EnableFullDuplex()
		}
		tunnel.ServeHTTP(w, r)
	})
}

// tlsConfig is a static certificate when one is given, ACME otherwise. Any
// ClientHello — no SNI, a bare IP, a foreign name — gets the domain's
// certificate, as nginx's default server would hand out its own.
func (c XhttpConfig) tlsConfig() (*tls.Config, error) {
	if c.CertPEM != "" {
		cert, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM))
		if err != nil {
			return nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(c.ACMEDir),
		HostPolicy: autocert.HostWhitelist(c.Domain),
		Email:      c.ACMEEmail,
	}
	if c.ACMEURL != "" {
		m.Client = &acme.Client{DirectoryURL: c.ACMEURL}
	}
	cfg := m.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	// Order the certificate now rather than on the first visitor's handshake:
	// a probe right after deploy then meets a ready site, not a stalled
	// ACME order. The hello claims ECDSA support, as every browser's does, so
	// the warm-up fetches the certificate visitors will be served.
	go func() {
		_, err := m.GetCertificate(&tls.ClientHelloInfo{
			ServerName:        c.Domain,
			SignatureSchemes:  []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
			SupportedCurves:   []tls.CurveID{tls.CurveP256},
			CipherSuites:      []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
			SupportedVersions: []uint16{tls.VersionTLS13, tls.VersionTLS12},
		})
		if err != nil {
			log.Printf("nekko-edge: certificate for %s: %v", c.Domain, err)
		}
	}()
	get := cfg.GetCertificate
	cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hello.ServerName != c.Domain {
			h := *hello
			h.ServerName = c.Domain
			return get(&h)
		}
		return get(hello)
	}
	return cfg, nil
}

// serveFront runs the site until ctx ends: the edge's own HTTPS when it
// terminates TLS, or cleartext on loopback when a proxy does (frontPlaintext).
func serveFront(ctx context.Context, c XhttpConfig) error {
	if c.Front == frontPlaintext {
		return servePlaintextFront(ctx, c)
	}
	cfg, err := c.tlsConfig()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           c.handler(),
		TLSConfig:         cfg,
		ReadHeaderTimeout: 10 * time.Second,
		// No read/write timeout: the tunnel's requests are long-lived streams.
		IdleTimeout: 5 * time.Minute,
		// A handshake from a scanner is noise, not news.
		ErrorLog: log.New(discard{}, "", 0),
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	go func() {
		if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("nekko-edge: front: %v", err)
		}
	}()
	return nil
}

// servePlaintextFront runs the site in cleartext on c.Listen, a loopback port a
// reverse proxy (nginx) forwards the secret path to after terminating TLS. It
// speaks HTTP/1.1 and h2c with prior knowledge, so the proxy may use either;
// the tunnel's streams are flushed, never buffered — the proxy must do the same
// (nginx: proxy_buffering off, proxy_request_buffering off).
func servePlaintextFront(ctx context.Context, c XhttpConfig) error {
	ln, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	protos := new(http.Protocols)
	protos.SetHTTP1(true)
	protos.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:           c.handler(),
		Protocols:         protos,
		ReadHeaderTimeout: 10 * time.Second,
		// No read/write timeout: the tunnel's requests are long-lived streams.
		IdleTimeout: 5 * time.Minute,
		ErrorLog:    log.New(discard{}, "", 0),
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("nekko-edge: front: %v", err)
		}
	}()
	return nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
