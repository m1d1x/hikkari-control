package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// selfSigned is a throwaway certificate for the upstream stub. Its validity is
// irrelevant to what is being tested — K3 does not verify the certificate, on
// purpose (see tunnelDialled).
func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "upstream.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// directDialer makes tunnelDialled testable without a SOCKS proxy: the thing
// under test is what it accepts as proof, not how it got the connection.
type directDialer struct{}

func (directDialer) Dial(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, 2*time.Second)
}

// TestK3NeedsTheUpstreamsOwnBytes is the П9.7 regression: a connection that
// opens and then answers nothing is NOT the tunnel carrying traffic.
//
// Before the fix K3 was a bare dial, and the thing that answered it was the
// prober's own local proxy — so K3 was true with the relay's unit stopped, the
// verdict stayed `unknown`, and the detector opened nothing. A listener that
// accepts and stays silent stands in for exactly that.
func TestK3NeedsTheUpstreamsOwnBytes(t *testing.T) {
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			// Accept and say nothing, like a SOCKS CONNECT that succeeded
			// locally while the far end was never reached.
			_ = c
		}
	}()

	url := "https://" + silent.Addr().String() + "/healthz"
	if tunnelDialled(context.Background(), directDialer{}, url, 2*time.Second) {
		t.Fatal("K3 held on a connection that carried nothing: a dial is not evidence")
	}
}

// TestK3HoldsWhenTheUpstreamHandshakes is the other half: a TLS upstream that
// completes a handshake and then answers no HTTP at all must still read as
// "the tunnel carried a connection". That is the whole reason K3 exists —
// a silent gateway of our own is not a burnt relay.
func TestK3HoldsWhenTheUpstreamHandshakes(t *testing.T) {
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Handshake on read, then nothing: no HTTP answer, on purpose.
			go func() {
				defer c.Close()
				_ = c.(*tls.Conn).Handshake()
				time.Sleep(time.Second)
			}()
		}
	}()

	url := "https://" + ln.Addr().String() + "/healthz"
	if !tunnelDialled(context.Background(), directDialer{}, url, 3*time.Second) {
		t.Fatal("K3 failed on an upstream that handshook: the tunnel did carry a connection")
	}
}

// TestTheVoiceDoorDoesNotEraseTheStreamsDetail: the detail is where a row says
// why it was called what it was called, and the voice probe used to write over
// it. Losing that line is what kept the broken K3 invisible.
func TestTheVoiceDoorDoesNotEraseTheStreamsDetail(t *testing.T) {
	r := withXUDP(probeResult{Verdict: verdictUnknown, Detail: detailUpstreamSilent}, voiceProbe{got: 0, sent: 100})
	if r.Detail == "xudp 0/100" {
		t.Fatal("the stream probe's detail was overwritten by the voice door")
	}
	if want := detailUpstreamSilent + "; xudp 0/100"; r.Detail != want {
		t.Fatalf("detail = %q, want %q", r.Detail, want)
	}
	if r.Verdict != verdictUnknown {
		t.Fatalf("verdict = %q, want it untouched", r.Verdict)
	}
}
