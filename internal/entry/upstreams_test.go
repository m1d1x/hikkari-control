package entry

import (
	"context"
	"errors"
	"testing"
)

func TestUpstreamsIncludeNamedSFUSignalingWithSchemePorts(t *testing.T) {
	got, err := Upstreams("api.example.org:443", "wss://sfu-ru-1.example.org/ws", "ws://sfu-eu-1.example.org:8443/ws", "wss://sfu-ru-1.example.org/again", "ws://local.example.org/ws", "wss://localhost:443/ws", "wss://[::1]/ws", "ftp://other.example.org:443/x")
	if err != nil {
		t.Fatal(err)
	}
	want := "api.example.org:443,sfu-ru-1.example.org:443,sfu-eu-1.example.org:8443,local.example.org:80"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

type liveStreamTargets struct {
	endpoints []string
	err       error
}

func (s *liveStreamTargets) Streams(context.Context) ([]string, error) { return s.endpoints, s.err }

func TestRelayReadsCurrentStreamTargetsAndFailsOnRegistryError(t *testing.T) {
	source := &liveStreamTargets{endpoints: []string{"wss://sfu-a.example.org/ws"}}
	j := jobRun{ctx: context.Background(), p: &Provisioner{upstream: "api.example.org:443,update.example.org:443", streams: source}}
	got, err := j.streamUpstreams()
	if err != nil || got != "api.example.org:443,update.example.org:443,sfu-a.example.org:443" {
		t.Fatalf("initial: %q %v", got, err)
	}
	source.endpoints = []string{"wss://sfu-b.example.org/ws"}
	got, err = j.streamUpstreams()
	if err != nil || got != "api.example.org:443,update.example.org:443,sfu-b.example.org:443" {
		t.Fatalf("refresh: %q %v", got, err)
	}
	source.err = errors.New("registry unavailable")
	if _, err := j.streamUpstreams(); !errors.Is(err, source.err) {
		t.Fatalf("registry error was lost: %v", err)
	}
	j.acc.proto = ProtoHysteria2
	if got, err := j.streamUpstreams(); err != nil || got != j.p.upstream {
		t.Fatalf("datagram relay queried stream targets: %q %v", got, err)
	}
}

func TestUpstreamsListTheGatewayAndTheClientsHTTPHosts(t *testing.T) {
	got, err := Upstreams("api.example.org:443",
		"https://s3.storage.example.net", "https://update.example.org", "http://files.example.org:8080")
	if err != nil {
		t.Fatal(err)
	}
	want := "api.example.org:443,s3.storage.example.net:443,update.example.org:443,files.example.org:8080"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestUpstreamsTakeABareHostAsHTTPS(t *testing.T) {
	got, _ := Upstreams("api.example.org:443", "update.example.org")
	if got != "api.example.org:443,update.example.org:443" {
		t.Fatalf("got %q", got)
	}
}

// A relay that forwarded to its own loopback would open the box's local
// services to every client key holder; the edge cannot match an IP anyway.
func TestUpstreamsNeverIncludeLoopbackOrIPs(t *testing.T) {
	got, err := Upstreams("api.example.org:443",
		"http://localhost:9800", "http://minio.localhost:9000", "https://10.0.0.5", "http://[::1]:9000", "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "api.example.org:443" {
		t.Fatalf("got %q", got)
	}
}

func TestUpstreamsListEachHostOnce(t *testing.T) {
	got, _ := Upstreams("api.example.org:443", "https://api.example.org", "https://API.example.org/x")
	if got != "api.example.org:443" {
		t.Fatalf("got %q", got)
	}
}

func TestUpstreamsRefuseAGatewayWithoutAPort(t *testing.T) {
	if _, err := Upstreams("api.example.org"); err == nil {
		t.Fatal("want an error")
	}
}
