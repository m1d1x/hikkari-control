package main

// wirestand_h3.go is the stand's wire h3 (variant A of the entry hot path
// plan, hikkari-client plans/2026-11-11-entry-hot-path-and-unit-economics.md):
// Naive over HTTP/3, Chromium's own QUIC on the wire, CONNECT streams only.
//
// The exit's half is Caddy as a Naive relay runs it (hikkari_proxy with
// padding and probe resistance), HTTP/3 only, on the wire's UDP port; behind
// it the stand's echo and bulk on loopback TCP. The entry's half needs
// Chromium (cronet), which the relay's build does not link: it is in
// wirestand_h3_entry.go, built with -tags with_purego,wirestand_h3 only.
//
// A stream keeps no datagram boundaries, so a conversation over h3 frames
// each write: u16 length, then the bytes. The exit's echo sends the stream
// back as it came, framing and all.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
)

const (
	wireH3 = "h3"
	// h3User is the stand's Naive user on the exit.
	h3User = "stand"
)

// h3EchoAt and h3BulkAt are the exit's loopback echo and bulk: the wire's
// port and the next one, TCP (Caddy holds only the UDP port).
func h3EchoAt(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }
func h3BulkAt(port int) string { return fmt.Sprintf("127.0.0.1:%d", port+1) }

// h3CA is the stand's own root, derived from the secret on both ends: the
// entry trusts it (Chromium checks the exit's certificate against it), the
// exit signs its certificate with it. Its key is the same on both ends; its
// signature need not be (ECDSA draws), Chromium matches a root by name and key.
func h3CA(cfg WireStandConfig) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	var key *ecdsa.PrivateKey
	seed := cfg.key("h3 ca", 32)
	for i := 0; key == nil; i++ {
		k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), seed)
		if err == nil {
			key = k
			break
		}
		if i > 8 {
			return nil, nil, nil, err
		}
		s := sha256.Sum256(seed) // out of range: draw again from the seed
		seed = s[:]
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hikkari wire stand"},
		NotBefore: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2027, 11, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	return ca, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// h3Leaf is the exit's certificate for the SNI, signed by the stand's root.
func h3Leaf(cfg WireStandConfig) (certPEM, keyPEM []byte, err error) {
	ca, caKey, _, err := h3CA(cfg)
	if err != nil {
		return nil, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cfg.SNI}, DNSNames: []string{cfg.SNI},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(0, 0, 90),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// h3ExitDocument is the exit's Caddy: HTTP/3 only on the wire's port, the
// stand's root-signed certificate, Naive as a relay serves it, and a plain
// page for anyone without the credential. The proxy reaches the loopback
// echo and bulk only.
func h3ExitDocument(cfg WireStandConfig) ([]byte, error) {
	certPEM, keyPEM, err := h3Leaf(cfg)
	if err != nil {
		return nil, err
	}
	credential := []byte(base64.StdEncoding.EncodeToString([]byte(h3User + ":" + cfg.password("h3"))))
	return json.Marshal(map[string]any{
		"admin":   map[string]any{"disabled": true, "config": map[string]any{"persist": false}},
		"logging": map[string]any{"logs": map[string]any{"default": map[string]any{"level": "FATAL"}}},
		"apps": map[string]any{
			"tls": map[string]any{"certificates": map[string]any{"load_pem": []any{map[string]any{"certificate": string(certPEM), "key": string(keyPEM)}}}},
			"http": map[string]any{"servers": map[string]any{"h3": map[string]any{
				"listen": []string{fmt.Sprintf(":%d", cfg.Port)}, "protocols": []string{"h3"},
				"automatic_https":         map[string]any{"disable": true},
				"tls_connection_policies": []any{map[string]any{}},
				"routes": []any{map[string]any{"handle": []any{
					map[string]any{"handler": "hikkari_proxy", "hide_ip": true, "hide_via": true, "probe_resistance": map[string]any{},
						"auth_credentials": [][]byte{credential},
						"acl":              []any{map[string]any{"subjects": []string{"127.0.0.1"}, "allow": true}, map[string]any{"subjects": []string{"all"}, "allow": false}}},
					map[string]any{"handler": "static_response", "status_code": 200, "body": "<!doctype html><title>ok</title>"},
				}}},
			}}},
		},
	})
}

// serveStandH3 is the exit's half: echo and bulk on loopback, Caddy before.
func serveStandH3(ctx context.Context, cfg WireStandConfig) error {
	echo, err := net.Listen("tcp", h3EchoAt(cfg.Port))
	if err != nil {
		return err
	}
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if tc, ok := c.(*net.TCPConn); ok {
					_ = tc.SetNoDelay(true)
				}
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	bulk, err := net.Listen("tcp", h3BulkAt(cfg.Port))
	if err != nil {
		echo.Close()
		return err
	}
	go serveBulk(bulk)
	context.AfterFunc(ctx, func() { _ = echo.Close(); _ = bulk.Close(); _ = caddy.Stop() })
	doc, err := h3ExitDocument(cfg)
	if err != nil {
		return err
	}
	return caddy.Load(doc, true)
}

// h3Conn is a conversation over one stream: every write a frame.
type h3Conn struct {
	net.Conn
	wmu  sync.Mutex
	wbuf []byte
	hdr  [2]byte
}

func newH3Conn(c net.Conn) *h3Conn { return &h3Conn{Conn: c} }

func (c *h3Conn) Write(p []byte) (int, error) {
	if len(p) > 0xffff {
		return 0, errors.New("h3: frame too big")
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.wbuf = binary.BigEndian.AppendUint16(c.wbuf[:0], uint16(len(p)))
	c.wbuf = append(c.wbuf, p...)
	if _, err := c.Conn.Write(c.wbuf); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Read gives one frame (cut to p).
func (c *h3Conn) Read(p []byte) (int, error) {
	if _, err := io.ReadFull(c.Conn, c.hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(c.hdr[:]))
	if n <= len(p) {
		return io.ReadFull(c.Conn, p[:n])
	}
	if _, err := io.ReadFull(c.Conn, p); err != nil {
		return 0, err
	}
	_, err := io.CopyN(io.Discard, c.Conn, int64(n-len(p)))
	return len(p), err
}
