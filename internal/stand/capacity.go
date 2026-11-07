package stand

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/capacity"
)

const latencyURL = "https://www.gstatic.com/generate_204"

const capacityURL = "https://speed.cloudflare.com/__down"

// measureCapacity adapts the payload to the speed, with a hard per-profile
// budget. A fast, byte-limited sample is explicitly a lower bound, not a peak.
func measureCapacity(ctx context.Context, client *http.Client, endpoint, key string) capacity.Sample {
	result := capacity.Sample{Key: key, MeasuredAt: time.Now().Unix(), Error: "unreachable"}
	failure := result
	budget := int64(capacity.MaxBytes)
	size := int64(256 << 10)
	for round := 0; budget >= 256<<10 && round < 5; round++ {
		if size > budget {
			size = budget
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		u, err := url.Parse(endpoint)
		if err != nil {
			cancel()
			return failure
		}
		q := u.Query()
		q.Set("bytes", strconv.FormatInt(size, 10))
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(attempt, http.MethodGet, u.String(), nil)
		if err != nil {
			cancel()
			return failure
		}
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("Cache-Control", "no-cache")
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			return partial(result, failure)
		}
		if resp.StatusCode != http.StatusOK || (resp.ContentLength >= 0 && resp.ContentLength != size) {
			resp.Body.Close()
			cancel()
			// The test service refuses some large requests (403 at 16 MiB from
			// RU points); a completed smaller round still stands as a lower bound.
			return partial(result, failure)
		}
		started := time.Now()
		n, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, size))
		elapsed := time.Since(started)
		resp.Body.Close()
		cancel()
		budget -= n
		if readErr != nil && !errors.Is(readErr, context.DeadlineExceeded) {
			return partial(result, failure)
		}
		if n < 256<<10 || elapsed.Milliseconds() < 1 {
			size *= 4
			continue
		}
		// The first small request warms TLS and the congestion window. Never expose
		// a latency check as a throughput result.
		if round > 0 {
			result.Bytes = n
			result.DurationMS = elapsed.Milliseconds()
			result.Mbps = float64(n) * 8 / float64(result.DurationMS) / 1000
			result.Limited = elapsed < 750*time.Millisecond
			result.Error = ""
			if !result.Limited {
				return result
			}
		}
		size *= 4
	}
	return result
}

// partial keeps a completed round when a later, larger round fails.
func partial(result, failure capacity.Sample) capacity.Sample {
	if result.Error == "" && result.Mbps > 0 {
		result.Limited = true
		return result
	}
	return failure
}

func (r Runner) runCapacity(ctx context.Context, profiles []Profile) Result {
	result := Result{Core: r.Kit.Label}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	// Check the observation point/test endpoint before spending relay bandwidth.
	direct := &http.Transport{Proxy: nil, DisableCompression: true}
	defer direct.CloseIdleConnections()
	control := measureCapacity(ctx, &http.Client{Transport: direct}, capacityURL, strings.Repeat("0", 64))
	for _, p := range profiles {
		if ctx.Err() != nil {
			result.Error = "capacity run cancelled"
			break
		}
		if len(p.CapacityKey) != 64 {
			result.Error = "kit parser does not provide capacity-v1 identities"
			break
		}
		sample := r.measureProfileCapacity(ctx, p, control.Error == "")
		if control.Error != "" {
			sample.Mbps = 0
			sample.Bytes = 0
			sample.DurationMS = 0
			sample.Error = "control_unavailable"
		}
		if control.Error == "" {
			// A near-control rate may be the point's/test service's limit. Do not call
			// that the relay's maximum, regardless of how large the payload was.
			if sample.Error == "" && sample.Mbps >= control.Mbps*0.8 {
				sample.Limited = true
			}
		}
		result.Capacity = append(result.Capacity, sample)
		r.log("capacity: %s %.1f Mbps limited=%v status=%s", p.Label, sample.Mbps, sample.Limited, sample.Error)
	}
	if len(result.Capacity) == 0 && result.Error == "" {
		result.Error = "no capacity profiles in kit"
	}
	return result
}

func (r Runner) measureProfileCapacity(ctx context.Context, p Profile, download bool) capacity.Sample {
	return r.measureProfileCapacityAt(ctx, p, download, capacityURL, latencyURL)
}

func (r Runner) measureProfileCapacityAt(ctx context.Context, p Profile, download bool, downloadURL, rttURL string) capacity.Sample {
	failure := capacity.Sample{Key: p.CapacityKey, MeasuredAt: time.Now().Unix(), Error: "core_unavailable"}
	cfg, _, err := Config(p.Config, func(map[string]any, string) error { return nil }, "", p.Resolver)
	if err != nil {
		return failure
	}
	dir, err := os.MkdirTemp("", "hikkari-capacity-")
	if err != nil {
		return failure
	}
	defer os.RemoveAll(dir)
	// Only the isolated measurement core is started. The production tunnel and
	// observation worker are neither stopped nor reconfigured.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return failure
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(cfg, "__PORT__", strconv.Itoa(port))), 0600); err != nil {
		return failure
	}
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, r.Kit.Core, "run", "-c", path)
	if err := cmd.Start(); err != nil {
		return failure
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := false
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		connection, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			connection.Close()
			ready = true
			break
		}
		select {
		case <-probeCtx.Done():
			return failure
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !ready {
		return failure
	}
	proxy, _ := url.Parse(fmt.Sprintf("socks5h://127.0.0.1:%d", port))
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableCompression: true, MaxIdleConns: 2}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// A listening SOCKS port is not an established WireGuard session. Warm
	// endpoint handshake and tunnel DNS before the three timed HTTP requests.
	// This is a bounded 204 request, not another capacity download.
	if p.Config["type"] == "wireguard" {
		warmCtx, warmCancel := context.WithTimeout(probeCtx, 12*time.Second)
		req, err := http.NewRequestWithContext(warmCtx, http.MethodGet, rttURL, nil)
		if err == nil {
			req.Close = true
			if response, err := client.Do(req); err == nil {
				io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
				response.Body.Close()
			}
		}
		warmCancel()
	}
	rtt := measureLatency(probeCtx, client, rttURL)
	if !download {
		failure.RTTMS = rtt
		failure.Error = "control_unavailable"
		return failure
	}
	sample := measureCapacity(probeCtx, client, downloadURL, p.CapacityKey)
	sample.RTTMS = rtt
	return sample
}

// Three fresh connections before load: successful HTTPS round-trip, not a TCP
// connect and not time-to-first-byte of the download under load.
func measureLatency(ctx context.Context, client *http.Client, endpoint string) int {
	samples := []int{}
	for i := 0; i < 3; i++ {
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, err := http.NewRequestWithContext(attempt, http.MethodGet, endpoint, nil)
		if err != nil {
			cancel()
			return 0
		}
		req.Close = true
		started := time.Now()
		resp, err := client.Do(req)
		if err == nil {
			_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			if resp.StatusCode == http.StatusNoContent && readErr == nil {
				samples = append(samples, max(1, int(time.Since(started).Milliseconds())))
			}
		}
		cancel()
	}
	if len(samples) < 2 {
		return 0
	}
	sort.Ints(samples)
	return samples[len(samples)/2]
}
