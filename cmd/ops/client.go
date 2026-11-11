package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultHost = "hikkari-main"
	// boxAdminPort is where Caddy serves the panel and its API on the box, bound
	// to 127.0.0.1 (compose.hub.yaml).
	boxAdminPort = 9443
	boxDir       = "/opt/hikkari-control"
	updateURL    = "https://update.hikkari.net"

	// diagWait bounds request_diag: the command waits for the next call of the
	// client (up to a minute), the run, and the flush right after it.
	diagWait = 150 * time.Second
	diagPoll = 3 * time.Second
	// loadWait bounds probe_load: a round takes about a minute a profile.
	loadWait     = 15 * time.Minute
	defaultTail  = 1000
	defaultLast  = time.Hour
	tunnelWait   = 15 * time.Second
	requestLimit = 60 * time.Second
)

// client talks to the hub's ops plane, opening what it needs on first use.
type client struct {
	host string
	http *http.Client

	mu     sync.Mutex
	base   string // set by HIKKARI_OPS_URL or by a live tunnel
	direct bool
	token  string
	tunnel *exec.Cmd
	exited chan struct{}
}

func newClient() *client {
	c := &client{host: os.Getenv("HIKKARI_OPS_HOST"), http: &http.Client{Timeout: requestLimit}}
	if c.host == "" {
		c.host = defaultHost
	}
	if u := strings.TrimRight(os.Getenv("HIKKARI_OPS_URL"), "/"); u != "" {
		c.base, c.direct = u, true
	}
	return c
}

func (c *client) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopTunnelLocked()
}

func (c *client) stopTunnelLocked() {
	if c.tunnel != nil && c.tunnel.Process != nil {
		_ = c.tunnel.Process.Kill()
	}
	c.tunnel, c.exited = nil, nil
	if !c.direct {
		c.base = ""
	}
}

// ensureBase returns the hub's URL, opening the tunnel if it is not up. A tunnel
// that died (laptop slept, network changed) is opened again.
func (c *client) ensureBase() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.direct {
		return c.base, nil
	}
	if c.tunnel != nil {
		select {
		case <-c.exited:
			c.stopTunnelLocked()
		default:
			return c.base, nil
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("no free local port: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15", "-N",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, boxAdminPort), c.host)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("ssh: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	deadline := time.Now().Add(tunnelWait)
	for {
		select {
		case <-exited:
			return "", fmt.Errorf("ssh tunnel to %s: %s", c.host, strings.TrimSpace(stderr.String()))
		default:
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return "", fmt.Errorf("ssh tunnel to %s did not come up in %s", c.host, tunnelWait)
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.tunnel, c.exited = cmd, exited
	c.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	return c.base, nil
}

func tokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if home, herr := os.UserHomeDir(); herr == nil {
		dir, err = filepath.Join(home, ".config"), nil
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hikkari", "ops-token"), nil
}

// ensureToken finds the token, issuing one on the box when there is none.
func (c *client) ensureToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	if t := strings.TrimSpace(os.Getenv("HIKKARI_OPS_TOKEN")); t != "" {
		c.token = t
		return t, nil
	}
	path, err := tokenPath()
	if err != nil {
		return "", err
	}
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			c.token = t
			return t, nil
		}
	}
	if c.direct {
		return "", errors.New("HIKKARI_OPS_URL is set but there is no token: set HIKKARI_OPS_TOKEN")
	}
	label, _ := os.Hostname()
	label = "agent-" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return -1
	}, label)
	remote := fmt.Sprintf("cd %s && docker compose -f compose.hub.yaml exec -T hub /usr/local/bin/hub ops-token issue %s", boxDir, label)
	var stderr bytes.Buffer
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", c.host, remote)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("issuing an ops token on %s: %v: %s", c.host, err, strings.TrimSpace(stderr.String()))
	}
	token := strings.TrimSpace(string(out))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	c.token = token
	return token, nil
}

// apiError is the hub's refusal, kept whole: its text is what the caller reads.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

// request calls the ops plane. A connection failure reopens the tunnel once.
func (c *client) request(method, path string, query url.Values, body any) ([]byte, error) {
	token, err := c.ensureToken()
	if err != nil {
		return nil, err
	}
	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		base, err := c.ensureBase()
		if err != nil {
			return nil, err
		}
		u := base + path
		if len(query) > 0 {
			u += "?" + query.Encode()
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestLimit)
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			cancel()
			return nil, err
		}
		// The box's Caddy serves the operator plane for the host «localhost»,
		// the panel's own origin; any other host gets an empty 200 from it.
		if !c.direct {
			req.Host = "localhost"
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			cancel()
			if attempt == 0 && !c.direct {
				c.mu.Lock()
				c.stopTunnelLocked()
				c.mu.Unlock()
				continue
			}
			return nil, fmt.Errorf("hub: %w", err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("hub: %w", err)
		}
		if resp.StatusCode/100 != 2 {
			msg := strings.TrimSpace(string(data))
			var e struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(data, &e) == nil && e.Error != "" {
				msg = e.Error
			}
			if resp.StatusCode == http.StatusUnauthorized {
				msg += " (токен отозван или выпущен на другом хабе)"
			}
			return nil, &apiError{status: resp.StatusCode, msg: fmt.Sprintf("hub %d: %s", resp.StatusCode, msg)}
		}
		// Every ops route answers with a body: an empty one came from something
		// in front of the hub, and must not read as «no data».
		if len(data) == 0 {
			return nil, fmt.Errorf("hub: empty answer %d from %s: the request did not reach the hub", resp.StatusCode, path)
		}
		return data, nil
	}
}

func prettyJSON(data []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, data, "", "  "); err != nil {
		return string(data)
	}
	return buf.String()
}

func installPath(who, suffix string) (string, error) {
	if strings.TrimSpace(who) == "" {
		return "", errors.New("who is required: tester-003, 003, an install id or a unique prefix")
	}
	return "/api/v1/ops/testers/" + url.PathEscape(who) + suffix, nil
}

// call runs one command by name; the shell and MCP both land here.
func (c *client) call(cmd string, args map[string]string) (string, error) {
	switch cmd {
	case "testers":
		data, err := c.request(http.MethodGet, "/api/v1/ops/testers", nil, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "logs":
		return c.logs(args)
	case "diag":
		return c.diag(args)
	case "probe":
		return c.probe(args)
	case "provoke":
		return c.provoke(args)
	case "load":
		return c.load(args)
	case "probe-voice":
		return c.probeVoice(args)
	case "voice":
		q := url.Values{}
		for _, k := range []string{"who", "proto", "last"} {
			if v := args[k]; v != "" {
				q.Set(k, v)
			}
		}
		if args["rows"] != "" && args["rows"] != "0" {
			q.Set("rows", "1")
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/voice", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "hikkari":
		q := url.Values{}
		for _, k := range []string{"who", "side", "path", "metric", "last", "step"} {
			if v := args[k]; v != "" {
				q.Set(k, v)
			}
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/hikkari", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "legs":
		q := url.Values{}
		for _, k := range []string{"who", "last"} {
			if v := args[k]; v != "" {
				q.Set(k, v)
			}
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/legs", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "matrix":
		return c.matrix(args)
	case "stand":
		q := url.Values{}
		if v := args["limit"]; v != "" {
			q.Set("limit", v)
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/stand", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "knobs":
		q := url.Values{}
		for _, k := range []string{"profile", "days"} {
			if v := args[k]; v != "" {
				q.Set(k, v)
			}
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/knobs", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "edge-hellos":
		q := url.Values{}
		for _, k := range []string{"relay", "last"} {
			if v := args[k]; v != "" {
				q.Set(k, v)
			}
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/edge-hellos", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "knob-check":
		q := url.Values{}
		for _, k := range []string{"who", "profile", "last", "limit"} {
			if v := args[k]; v != "" {
				q.Set(k, v)
			}
		}
		if args["mismatch"] != "" && args["mismatch"] != "0" {
			q.Set("mismatch", "1")
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/knob-check", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "runs-record":
		return c.runsRecord(args)
	case "trunk":
		q := url.Values{}
		for _, k := range []string{"last", "layer", "entry", "rows", "limit"} {
			if v := args[k]; v != "" {
				q.Set(k, v)
			}
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/trunk", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "cascade-load":
		body := map[string]any{"slot": args["slot"], "entry": args["entry"], "echo": args["echo"], "path": args["path"]}
		for _, k := range []string{"users", "minutes", "game_pps"} {
			n, err := strconv.Atoi(args[k])
			if err != nil {
				return "", fmt.Errorf("%s: нужно число", k)
			}
			body[k] = n
		}
		data, err := c.request(http.MethodPost, "/api/v1/ops/cascade-load", nil, body)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "runs":
		q := url.Values{}
		for _, k := range []string{"profile", "source", "last", "limit"} {
			if v := args[k]; v != "" {
				q.Set(k, v)
			}
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/relay-runs", q, nil)
		if err != nil {
			return "", err
		}
		return ndjsonRows(data)
	case "census-add":
		return c.censusAdd(args)
	case "census-add-addr":
		data, err := c.request(http.MethodPost, "/api/v1/ops/census/addresses", nil,
			map[string]any{"name": args["name"], "addresses": strings.Split(args["addrs"], ",")})
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "census-exit-link":
		data, err := c.request(http.MethodPost, "/api/v1/ops/census/exit-link", url.Values{"name": {args["name"]}}, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "census-proto":
		return c.censusProto(args)
	case "census-probe":
		return c.censusProbe(args)
	case "census-report":
		data, err := c.request(http.MethodGet, "/api/v1/ops/census/report", nil, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "rename":
		path, err := installPath(args["who"], "/alias")
		if err != nil {
			return "", err
		}
		data, err := c.request(http.MethodPut, path, nil, map[string]string{"alias": args["alias"], "note": args["note"]})
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "errors":
		q := url.Values{}
		if w := args["window"]; w != "" {
			q.Set("window", w)
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/errors", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "tickets":
		q := url.Values{}
		if s := args["status"]; s != "" {
			q.Set("status", s)
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/tickets", q, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "ticket":
		if args["id"] == "" {
			return "", errors.New("id is required")
		}
		data, err := c.request(http.MethodGet, "/api/v1/ops/tickets/"+url.PathEscape(args["id"]), nil, nil)
		if err != nil {
			return "", err
		}
		return prettyJSON(data), nil
	case "ci":
		return ciStatus(args)
	case "release":
		return c.releaseStatus()
	}
	return "", fmt.Errorf("unknown command %q", cmd)
}

// parseWhen reads unix seconds or RFC 3339.
func parseWhen(v string) (time.Time, error) {
	if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(sec, 0), nil
	}
	return time.Parse(time.RFC3339, v)
}

// parseLast reads a Go duration, with «d» for days on top.
func parseLast(v string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("invalid last %q", v)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid last %q: use 30m, 2h, 3d", v)
	}
	return d, nil
}

func (c *client) logs(args map[string]string) (string, error) {
	path, err := installPath(args["who"], "/export")
	if err != nil {
		return "", err
	}
	to := time.Now()
	if v := args["to"]; v != "" {
		if to, err = parseWhen(v); err != nil {
			return "", fmt.Errorf("invalid to: %w", err)
		}
	}
	from := to.Add(-defaultLast)
	if v := args["last"]; v != "" {
		d, err := parseLast(v)
		if err != nil {
			return "", err
		}
		from = to.Add(-d)
	}
	if v := args["from"]; v != "" {
		if from, err = parseWhen(v); err != nil {
			return "", fmt.Errorf("invalid from: %w", err)
		}
	}
	tail := defaultTail
	if v := args["tail"]; v != "" {
		if tail, err = strconv.Atoi(v); err != nil || tail <= 0 {
			return "", fmt.Errorf("invalid tail %q", v)
		}
	}
	return c.export(path, from, to, args["src"], tail)
}

// export reads the NDJSON and keeps its head (meta, summary) and its last tail
// records: the end of a window is where a complaint lives, and the summary still
// counts what was cut. A cut leaves a line saying how much.
func (c *client) export(path string, from, to time.Time, src string, tail int) (string, error) {
	// Whole seconds on the wire: round the end up, or the lines of its last
	// second, the newest ones, fall out of the window.
	q := url.Values{"from": {strconv.FormatInt(from.Unix(), 10)}, "to": {strconv.FormatInt(to.Unix()+1, 10)}}
	if src != "" {
		q.Set("src", src)
	}
	data, err := c.request(http.MethodGet, path, q, nil)
	if err != nil {
		return "", err
	}
	return trimExport(data, tail), nil
}

func trimExport(data []byte, tail int) string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	const head = 2 // meta, summary
	if len(lines) <= head+tail {
		return strings.Join(lines, "\n")
	}
	omitted := len(lines) - head - tail
	out := append([]string{}, lines[:head]...)
	out = append(out, fmt.Sprintf(`{"type":"truncated","omitted_records":%d,"hint":"сузь окно (from/to, last), отбери src или подними tail"}`, omitted))
	out = append(out, lines[len(lines)-tail:]...)
	return strings.Join(out, "\n")
}

type diagAnswer struct {
	Nonce     string    `json:"nonce"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	Alias     string    `json:"alias"`
	Version   string    `json:"version"`
}

// diag asks for a diagnostic and waits for the result. A tester off the line
// fails at once with the hub's own words; a client that took the command and
// stayed silent, or never took it, gets its own message, so a timeout is not a
// riddle.
func (c *client) diag(args map[string]string) (string, error) {
	kind := args["kind"]
	if kind == "" {
		kind = "run_doctor"
	}
	// The doctor's lines carry the nonce. A snapshot writes [diag:*] lines into
	// the stream as usual, so it is read as the window since the request.
	source := "doctor"
	if kind == "snapshot" {
		source = ""
	}
	return c.askAndRead(args["who"], map[string]any{"kind": kind}, source)
}

// askAndRead queues a command, waits for it, and reads the lines of source
// that answer it; an empty source reads everything since the request.
func (c *client) askAndRead(who string, body map[string]any, source string) (string, error) {
	path, err := installPath(who, "/commands")
	if err != nil {
		return "", err
	}
	data, err := c.request(http.MethodPost, path, nil, body)
	if err != nil {
		return "", err
	}
	var asked diagAnswer
	if err := json.Unmarshal(data, &asked); err != nil {
		return "", fmt.Errorf("hub answer: %w", err)
	}
	return c.waitAndRead(who, path, asked, source)
}

// waitAndRead polls a queued command, then reads its lines.
func (c *client) waitAndRead(who, path string, asked diagAnswer, source string) (string, error) {
	if err := c.waitDone(path, asked); err != nil {
		return "", err
	}
	return c.readAnswer(who, asked, source)
}

// waitDone polls a queued command until its client has answered it.
func (c *client) waitDone(path string, asked diagAnswer) error {
	return c.waitDoneWithin(path, asked, diagWait)
}

// waitDoneWithin is waitDone for a command that runs longer than a diagnostic.
func (c *client) waitDoneWithin(path string, asked diagAnswer, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	state := asked.State
	for state != "done" {
		if state == "expired" || time.Now().After(deadline) {
			return diagTimeout(asked, state, wait)
		}
		time.Sleep(diagPoll)
		data, err := c.request(http.MethodGet, path+"/"+url.PathEscape(asked.Nonce), nil, nil)
		if err != nil {
			return err
		}
		var now diagAnswer
		if err := json.Unmarshal(data, &now); err != nil {
			return fmt.Errorf("hub answer: %w", err)
		}
		state = now.State
	}
	return nil
}

// loadPoll is how often probe_load reads the round's lines; a test shortens it.
var loadPoll = 10 * time.Second

// loadRefused is the client's «error=…» in answer to load_profiles; a profile's
// own load_error= is part of the round, not a refusal.
var loadRefused = regexp.MustCompile(`(^|[^\w])error=`)

// load asks a tester for a load round over every profile now and waits for it
// to close. The command is answered as soon as the round starts, so the wait
// goes on in the [diag:load] lines, until «on request: done» or a refusal.
func (c *client) load(args map[string]string) (string, error) {
	who := args["who"]
	path, err := installPath(who, "/commands")
	if err != nil {
		return "", err
	}
	data, err := c.request(http.MethodPost, path, nil, map[string]any{"kind": "load_profiles"})
	if err != nil {
		return "", err
	}
	var asked diagAnswer
	if err := json.Unmarshal(data, &asked); err != nil {
		return "", fmt.Errorf("hub answer: %w", err)
	}
	if err := c.waitDone(path, asked); err != nil {
		return "", err
	}
	exportPath, err := installPath(who, "/export")
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(loadWait)
	for {
		out, err := c.export(exportPath, asked.CreatedAt.Add(-time.Minute), time.Now().Add(time.Minute), "diag:load", defaultTail)
		if err != nil {
			return "", err
		}
		if roundClosed(out, asked.Nonce) || loadRefused.MatchString(answerLines(out, asked.Nonce)) {
			return out, nil
		}
		if time.Now().After(deadline) {
			return out, fmt.Errorf("%s начал раунд, но не закрыл его за %s: строки выше, остальное в logs --src diag:load", asked.Alias, loadWait)
		}
		time.Sleep(loadPoll)
	}
}

// roundClosed says whether a record after the command's own answer closes the
// round. Only log records count, since the export's meta.guide names the
// closing line too, and only after the answer, since the window opens a
// minute before the request and may hold the close of an earlier round.
func roundClosed(export, nonce string) bool {
	answered := false
	for _, line := range strings.Split(export, "\n") {
		var rec struct {
			Type string `json:"type"`
			Src  string `json:"src"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil || rec.Type != "log" {
			continue
		}
		if strings.Contains(rec.Src, "nonce="+nonce) {
			answered = true
			continue
		}
		if answered && rec.Msg == "on request: done" {
			return true
		}
	}
	return false
}

// answerLines keeps the records of the command's own answer.
func answerLines(export, nonce string) string {
	var out []string
	for _, line := range strings.Split(export, "\n") {
		if strings.Contains(line, "nonce="+nonce) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func (c *client) readAnswer(who string, asked diagAnswer, source string) (string, error) {
	exportPath, err := installPath(who, "/export")
	if err != nil {
		return "", err
	}
	src := ""
	if source != "" {
		src = source + " nonce=" + asked.Nonce
	}
	return c.export(exportPath, asked.CreatedAt.Add(-time.Minute), time.Now().Add(time.Minute), src, defaultTail)
}

// probe asks a tester to open TCP connections to the named addresses from its
// physical card, past the tunnel, and prints the counts.
func (c *client) probe(args map[string]string) (string, error) {
	targets, err := parseProbeTargets(args["targets"])
	if err != nil {
		return "", err
	}
	return c.askAndRead(args["who"], map[string]any{"kind": "probe_targets", "targets": targets}, "probe")
}

// provokeWait bounds provoke_targets: up to a minute until the client takes the
// command, its two minutes of stimuli, and the batch that ships the answer.
const provokeWait = 4 * time.Minute

// provoke asks a tester to send the опыт 0 stimuli from its physical card to up
// to three provoke boxes (edge EDGE_PROTO=provoke) at once, and prints what the
// client saw beside what each box says reached it.
func (c *client) provoke(args map[string]string) (string, error) {
	targets, err := parseProbeTargets(args["targets"])
	if err != nil {
		return "", err
	}
	who := args["who"]
	path, err := installPath(who, "/commands")
	if err != nil {
		return "", err
	}
	data, err := c.request(http.MethodPost, path, nil, map[string]any{"kind": "provoke_targets", "targets": targets})
	if err != nil {
		return "", err
	}
	var asked diagAnswer
	if err := json.Unmarshal(data, &asked); err != nil {
		return "", fmt.Errorf("hub answer: %w", err)
	}
	if err := c.waitDoneWithin(path, asked, provokeWait); err != nil {
		return "", err
	}
	return c.readAnswer(who, asked, "provoke")
}

// parseProbeTargets reads label=ip:port,label=ip:port. The hub checks the
// values; this only splits them.
func parseProbeTargets(s string) ([]map[string]string, error) {
	var out []map[string]string
	for _, part := range strings.Split(s, ",") {
		label, addr, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return nil, fmt.Errorf("targets: %q is not label=ip:port", part)
		}
		out = append(out, map[string]string{"label": label, "addr": addr})
	}
	return out, nil
}

func diagTimeout(a diagAnswer, state string, wait time.Duration) error {
	if state == "delivered" {
		return fmt.Errorf("%s забрал команду, но не ответил за %s: версия %s могла не уметь %s, или проверка ещё идёт. Логи: tool logs", a.Alias, wait, a.Version, a.Kind)
	}
	return fmt.Errorf("%s не забрал команду за %s (состояние %s): версия %s может быть без heartbeat, или клиент только что закрыли", a.Alias, wait, state, a.Version)
}

// voiceWait bounds probe_voice: up to a minute until the client takes the
// command, two series of ~31 s, and up to a minute until the batch with its
// answer ships.
const voiceWait = 4 * time.Minute

// probeVoice asks a tester for a voice round now: a call's datagrams through
// the tunnel for 30 s and, on Windows, from the card beside it. The answer is
// the [diag:voice] lines; the hub keeps them in voice_runs as well.
func (c *client) probeVoice(args map[string]string) (string, error) {
	who := args["who"]
	path, err := installPath(who, "/commands")
	if err != nil {
		return "", err
	}
	data, err := c.request(http.MethodPost, path, nil, map[string]any{"kind": "probe_voice"})
	if err != nil {
		return "", err
	}
	var asked diagAnswer
	if err := json.Unmarshal(data, &asked); err != nil {
		return "", fmt.Errorf("hub answer: %w", err)
	}
	if err := c.waitDoneWithin(path, asked, voiceWait); err != nil {
		return "", err
	}
	return c.readAnswer(who, asked, "diag:voice")
}

// ciStatus reads GitHub Actions through gh, which already holds the login.
func ciStatus(args map[string]string) (string, error) {
	repo := args["repo"]
	if repo == "" {
		repo = "client"
	}
	switch repo {
	case "client", "control":
		repo = "m1d1x/hikkari-" + repo
	default:
		return "", fmt.Errorf("repo is client or control, got %q", repo)
	}
	limit := args["limit"]
	if limit == "" {
		limit = "10"
	}
	cmdArgs := []string{"run", "list", "-R", repo, "-L", limit,
		"--json", "workflowName,headBranch,headSha,displayTitle,status,conclusion,createdAt,url"}
	if ref := args["ref"]; ref != "" {
		cmdArgs = append(cmdArgs, "-b", ref)
	}
	var stderr bytes.Buffer
	cmd := exec.Command("gh", cmdArgs...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return prettyJSON(out), nil
}

// releaseStatus reads the probes the clients themselves poll, from the public
// edge, so it says what a client would be told.
func (c *client) releaseStatus() (string, error) {
	out := map[string]any{}
	for _, channel := range []string{"stable", "beta"} {
		for _, platform := range []string{"macos", "windows"} {
			key := channel + "/" + platform
			resp, err := c.http.Get(updateURL + "/" + key + "/latest.json")
			if err != nil {
				out[key] = "error: " + err.Error()
				continue
			}
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			var probe map[string]any
			switch {
			case resp.StatusCode != http.StatusOK:
				out[key] = fmt.Sprintf("HTTP %d", resp.StatusCode)
			case json.Unmarshal(data, &probe) != nil:
				out[key] = "not a probe"
			default:
				out[key] = probe["version"]
			}
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ndjsonRows prints a JSON array one element a line, the way the dataset is
// read and grepped.
func ndjsonRows(data []byte) (string, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return "", errors.New("unreadable answer")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d строк\n", len(rows))
	for _, r := range rows {
		b.Write(r)
		b.WriteByte('\n')
	}
	return b.String(), nil
}
