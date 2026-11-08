package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/m1d1x/hikkari-control/internal/stand"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type naiveEdge struct {
	Host string `json:"host"`
	User string `json:"user"`
	Pass string `json:"pass"`
	H3   bool   `json:"h3,omitempty"`
}

// Admission uses the same pinned Chromium core as the client, not a Go TLS
// impersonation. Absence of a usable kit is no evidence against a relay.
func probeNaiveEdge(ctx context.Context, e edge, target string, k2 bool, now time.Time, timeout time.Duration) probeResult {
	res := probeResult{TS: now, RelayName: e.Name, Detail: "naive", Verdict: "probe_failed", K2Control: b2u8(k2)}
	if e.Naive == nil {
		return res
	}
	cfg := standFromEnv()
	fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	kit, err := stand.Fetch(fetchCtx, cfg.download, cfg.kitURL, cfg.dir)
	cancel()
	if kit.Core == "" || kit.Cronet == "" {
		res.Detail = "naive: client kit with Cronet unavailable"
		return res
	}
	client, stop, err := startNaiveClient(ctx, kit.Core, e, timeout)
	if err != nil {
		res.Detail = "naive: client did not start"
		return res
	}
	defer stop()
	for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
		begin := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return res
		}
		response, err := client.Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		res.RealityHTTP = uint16(response.StatusCode)
		if tunnelAnswered(response.StatusCode) {
			res.RealityOK = 1
			res.RealityRTTMs = float32(time.Since(begin).Seconds() * 1000)
			res.Verdict = verdictClean
			break
		}
	}
	if res.RealityOK == 0 {
		if !k2 {
			res.Verdict = verdictNetworkDown
		} else {
			res.Verdict = verdictUnknown
		}
	}
	if !e.Naive.H3 {
		res.RawTCP = b2u8(probeRawTCP(net.JoinHostPort(e.Address, strconv.Itoa(e.Port)), timeout))
	}
	if e.Proto == protoHikkari {
		res = probeDatagram(ctx, kit.Core, e, res, timeout)
	}
	return res
}

func startNaiveClient(ctx context.Context, binary string, e edge, timeout time.Duration) (*http.Client, func(), error) {
	port, stop, err := startNaiveProxy(ctx, binary, e)
	if err != nil {
		return nil, nil, err
	}
	proxy, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	return &http.Client{Timeout: timeout, Transport: transport}, func() { transport.CloseIdleConnections(); stop() }, nil
}

// startNaiveProxy runs the client core with one naive outbound behind a local
// mixed inbound (HTTP and SOCKS5 on one port) and returns that port.
func startNaiveProxy(ctx context.Context, binary string, e edge) (int, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dir, err := os.MkdirTemp("", "hikkari-naive-probe-")
	if err != nil {
		return 0, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	config := map[string]any{"log": map[string]any{"disabled": true}, "inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": port}}, "outbounds": []any{map[string]any{"type": "naive", "tag": "probe", "server": e.Address, "server_port": e.Port, "username": e.Naive.User, "password": e.Naive.Pass, "quic": e.Naive.H3, "tls": map[string]any{"enabled": true, "server_name": e.Naive.Host}}}, "route": map[string]any{"final": "probe"}}
	raw, err := json.Marshal(config)
	if err != nil {
		cleanup()
		return 0, nil, err
	}
	path := filepath.Join(dir, "config.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		cleanup()
		return 0, nil, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(processCtx, binary, "run", "-c", path)
	cmd.Dir = filepath.Dir(binary)
	if err = cmd.Start(); err != nil {
		cancel()
		cleanup()
		return 0, nil, fmt.Errorf("client start failed")
	}
	stop := func() { cancel(); _ = cmd.Wait(); cleanup() }
	ready := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && processCtx.Err() == nil {
		c, e := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if e == nil {
			c.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		stop()
		return 0, nil, fmt.Errorf("client not ready")
	}
	return port, stop, nil
}
