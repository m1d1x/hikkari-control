package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"reflect"
	"strconv"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entrybench"
)

// hubClient talks to the hub's prober routes (internal/entry prober_api.go):
// base is the entry-probes ingest URL; the pool and the tasks hang under it.
type hubClient struct {
	base  string
	token string
	http  *http.Client
}

// probeTask is a task the hub asked for: an admission probe, or a bench
// through its one relay (П6.14).
type probeTask struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Relays []edge          `json:"relays"`
	Params json.RawMessage `json:"params"`
}

func (h hubClient) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, h.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// relays is the pool to watch: every alive relay, with dialling credentials.
func (h hubClient) relays(ctx context.Context) ([]edge, error) {
	var edges []edge
	if _, err := h.do(ctx, http.MethodGet, "/relays", nil, &edges); err != nil {
		return nil, err
	}
	return edges, validateEdges(edges)
}

// claim takes the oldest queued task of `kind` (admission | bench), or nil.
// The hub knows which observation point asks from the token alone.
func (h hubClient) claim(ctx context.Context, kind string) (*probeTask, error) {
	var t probeTask
	code, err := h.do(ctx, http.MethodPost, "/tasks/claim", map[string]string{"kind": kind}, &t)
	if err != nil || code == http.StatusNoContent {
		return nil, err
	}
	if len(t.Relays) == 0 {
		return nil, fmt.Errorf("task %s names no relay", t.ID)
	}
	return &t, validateEdges(t.Relays)
}

// finish reports a task's verdicts.
func (h hubClient) finish(ctx context.Context, id string, verdicts map[string]string) error {
	_, err := h.do(ctx, http.MethodPost, "/tasks/"+id, map[string]any{"verdicts": verdicts}, nil)
	return err
}

// finishBench reports a bench: the verdict and the run's summary.
func (h hubClient) finishBench(ctx context.Context, id string, verdicts map[string]string, res entrybench.Result) error {
	_, err := h.do(ctx, http.MethodPost, "/tasks/"+id, map[string]any{"verdicts": verdicts, "result": res}, nil)
	return err
}

// push sends probe rows to the dataset (ClickHouse entry_probes).
func (h hubClient) push(ctx context.Context, batch []probeResult) error {
	return pushBatch(ctx, h.http, h.base, h.token, batch)
}

// engine is one running xray with a SOCKS inbound per relay.
type engine struct {
	edges   []edge
	ports   map[string]int
	xudp    map[string]int // datagram doors of the stream relays that carry calls
	cleanup func()
	exited  <-chan struct{}
}

func startEngine(ctx context.Context, bin string, edges []edge, socksBase int, fingerprint string) (*engine, error) {
	config, ports, xudp, err := buildXrayConfig(edges, socksBase, fingerprint)
	if err != nil {
		return nil, err
	}
	if err := portsFree(ports); err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	cmd, cleanup, err := startXray(ctx, bin, config)
	if err != nil {
		return nil, err
	}
	e := &engine{edges: edges, ports: ports, xudp: xudp, cleanup: cleanup, exited: watchExit(cmd)}
	// Every SOCKS listener before the first probe, so a slow start is not
	// misread as every relay being dead.
	for _, ed := range edges {
		if err := waitSocks(ports[ed.Name], 10*time.Second); err != nil {
			e.stop()
			return nil, err
		}
	}
	return e, nil
}

// portsFree refuses an engine whose SOCKS ports someone else already holds.
// waitSocks only dials them, and a dial into another prober's xray succeeds:
// on a vantage shared with Nekko's prober (both default to 10810) every
// routine round rode Nekko's relays, read ours as `fingerprint`, and the hub
// re-keyed a healthy box three times (01.11). Set ENTRY_PROBE_SOCKS_BASE
// apart on such a machine.
func portsFree(ports map[string]int) error {
	for name, port := range ports {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return fmt.Errorf("socks port %d for %s is taken (another prober on this machine? set ENTRY_PROBE_SOCKS_BASE apart): %w", port, name, err)
		}
		ln.Close()
	}
	return nil
}

// serves reports whether the engine runs exactly this relay list.
func (e *engine) serves(edges []edge) bool { return reflect.DeepEqual(e.edges, edges) }

// dead reports whether xray exited under us.
func (e *engine) dead() bool {
	select {
	case <-e.exited:
		return true
	default:
		return false
	}
}

func (e *engine) stop() {
	if e == nil {
		return
	}
	e.cleanup()
	<-e.exited
}
