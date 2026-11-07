package main

import (
	"context"
	"encoding/base64"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func naiveRequest(key string) *http.Request {
	r := httptest.NewRequest(http.MethodConnect, "https://example.com:443", strings.NewReader("payload"))
	r.ProtoMajor = 2
	r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("hikkari:"+key)))
	return r
}
func TestNaiveKeysCountersAndRevocation(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	n := newNaiveRuntime(naiveConfig{User: "probe", Pass: "probe-probe-probe-probe"})
	gate := &naiveGate{runtime: n}
	if err := n.Set(context.Background(), []string{key}); err != nil {
		t.Fatal(err)
	}
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		if r.Header.Get("Proxy-Authorization") == naiveRequest(key).Header.Get("Proxy-Authorization") {
			t.Fatal("subscriber key reached upstream handler")
		}
		_, err := io.Copy(w, r.Body)
		return err
	})
	if err := gate.ServeHTTP(httptest.NewRecorder(), naiveRequest(key), next); err != nil {
		t.Fatal(err)
	}
	up, down, conns, keys := n.read()
	if up != 7 || down != 7 || conns != 0 || keys[key].Up != 7 || keys[key].Down != 7 {
		t.Fatalf("totals: %d %d %d %#v", up, down, conns, keys)
	}
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = gate.ServeHTTP(httptest.NewRecorder(), naiveRequest(key), caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			close(entered)
			<-r.Context().Done()
			return r.Context().Err()
		}))
	}()
	<-entered
	if err := n.Set(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revoked stream stayed live")
	}
	// A revoked subscriber's original header must reach the hidden upstream
	// rejection, never be rewritten into the relay's shared credential.
	_ = gate.ServeHTTP(httptest.NewRecorder(), naiveRequest(key), caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		if r.Header.Get("Proxy-Authorization") != naiveRequest(key).Header.Get("Proxy-Authorization") {
			t.Fatal("revoked key authenticated")
		}
		return nil
	}))
	_, _, _, keys = n.read()
	if keys[key].Up != 7 || keys[key].Down != 7 {
		t.Fatal("revocation discarded usage totals")
	}
}
