package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	egressHealthPath = "/.well-known/hikkari-egress"
	egressHTTPS      = "https://cp.cloudflare.com/generate_204"
	egressSTUN       = "stun.cloudflare.com:3478"
	egressTimeout    = 2 * time.Second
)

var egressHTTPClient = &http.Client{
	Transport:     &http.Transport{MaxIdleConnsPerHost: 2},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// serveEgressHealth is an authenticated synthetic request on the relay's
// own TLS and QUIC listeners. Its token is derived from a trunk key and can
// only request checks of the fixed destinations above. Other requests go
// through the regular relay or cover site handlers.
func (n *naiveRuntime) serveEgressHealth(w http.ResponseWriter, r *http.Request, datagram bool) bool {
	if r.Method != http.MethodHead || r.URL.Path != egressHealthPath || len(n.cfg.Pipe.Keys) == 0 {
		return false
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(token) != 64 {
		return false
	}
	match := 0
	for _, key := range n.cfg.Pipe.Keys {
		want := fmt.Sprintf("%x", sha256.Sum256([]byte("hikkari-egress-health/v1:"+key)))
		match |= subtle.ConstantTimeCompare([]byte(token), []byte(want))
	}
	if match != 1 {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), egressTimeout)
	defer cancel()
	healthy := checkEgressHTTPS
	if datagram {
		healthy = checkEgressSTUN
	}
	if healthy(ctx) {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	return true
}

func checkEgressHTTPS(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, egressHTTPS, nil)
	if err != nil {
		return false
	}
	res, err := egressHTTPClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusNoContent
}

func checkEgressSTUN(ctx context.Context) bool {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp4", egressSTUN)
	if err != nil {
		return false
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var req [20]byte
	binary.BigEndian.PutUint16(req[:2], 1)
	binary.BigEndian.PutUint32(req[4:8], 0x2112a442)
	rand.Read(req[8:])
	if _, err := conn.Write(req[:]); err != nil {
		return false
	}
	var res [1500]byte
	n, err := conn.Read(res[:])
	return err == nil && n >= 20 && binary.BigEndian.Uint16(res[:2]) == 0x0101 &&
		int(binary.BigEndian.Uint16(res[2:4])) == n-20 && bytes.Equal(req[4:], res[4:20])
}
