package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/census"
	"github.com/m1d1x/hikkari-control/internal/support"
)

// censusVantage is the prober in St Petersburg. One SSH connection per run: it
// resets SSH under frequent connections.
const (
	censusVantage = "spb"
	spbHost       = "nekko-spb"
	spbFlows      = 12
)

// clientEngineDir finds the client's engine next to this repository. The link
// parser lives there and nowhere else.
func clientEngineDir() (string, error) {
	if d := os.Getenv("HIKKARI_CLIENT_ENGINE"); d != "" {
		return d, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	d := filepath.Join(wd, "..", "hikkari-client", "engine")
	if _, err := os.Stat(filepath.Join(d, "cmd", "census-parse")); err != nil {
		return "", errors.New("no hikkari-client/engine/cmd/census-parse next to this repository: set HIKKARI_CLIENT_ENGINE")
	}
	return d, nil
}

func (c *client) censusAdd(args map[string]string) (string, error) {
	subURL, name := args["url"], args["name"]
	if subURL == "" || name == "" {
		return "", errors.New("census add needs a url and --name")
	}
	dir, err := clientEngineDir()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/census-parse", "--with-config")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(subURL + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.ReplaceAll(strings.TrimSpace(stderr.String()), subURL, "[subscription]")
		return "", fmt.Errorf("census-parse: %s", msg)
	}
	var entries []census.Entry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		return "", errors.New("census-parse: unreadable answer")
	}
	data, err := c.request(http.MethodPost, "/api/v1/ops/census/subscriptions", nil,
		map[string]any{"name": name, "url": subURL, "entries": entries})
	if err != nil {
		return "", err
	}
	return prettyJSON(data), nil
}

// censusProbe measures what is due from each vantage. One tester off the line
// does not lose the others' counts: failures are listed in the answer.
func (c *client) censusProbe(args map[string]string) (string, error) {
	out := map[string]any{}
	if n, err := c.probeSPB(); err != nil {
		out[censusVantage] = "error: " + err.Error()
	} else {
		out[censusVantage] = n
	}
	for _, who := range strings.Split(args["who"], ",") {
		if who = strings.TrimSpace(who); who == "" {
			continue
		}
		if n, err := c.probeTester(who); err != nil {
			out[who] = "error: " + err.Error()
		} else {
			out[who] = n
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	return string(data), err
}

func (c *client) dueTargets(vantage string) ([]census.Target, error) {
	data, err := c.request(http.MethodGet, "/api/v1/ops/census/targets", url.Values{"vantage": {vantage}}, nil)
	if err != nil {
		return nil, err
	}
	var out []census.Target
	return out, json.Unmarshal(data, &out)
}

func (c *client) record(ms []census.Measurement) error {
	if len(ms) == 0 {
		return nil
	}
	_, err := c.request(http.MethodPost, "/api/v1/ops/census/measurements", nil, ms)
	return err
}

func (c *client) probeTester(who string) (int, error) {
	targets, err := c.dueTargets("tester:" + who)
	if err != nil {
		return 0, err
	}
	done := 0
	for len(targets) > 0 {
		n := min(len(targets), support.MaxProbeTargets)
		batch := targets[:n]
		targets = targets[n:]
		text, err := c.askAndRead(who, map[string]any{"kind": "probe_targets", "targets": batch}, "probe")
		if err != nil {
			return done, err
		}
		ms := parseProbeCounts(text, "tester:"+who)
		if err := c.record(ms); err != nil {
			return done, err
		}
		done += len(ms)
	}
	return done, nil
}

// flowsField is the client's «label net=… flows[card]=3/12 med=…», inside a
// JSON log record.
var flowsField = regexp.MustCompile(`([a-z0-9][a-z0-9-]{0,31}) net=\S+ flows\[[^\]]*\]=(\d+)/(\d+)`)

func parseProbeCounts(text, vantage string) []census.Measurement {
	var out []census.Measurement
	for _, m := range flowsField.FindAllStringSubmatch(text, -1) {
		opened, _ := strconv.Atoi(m[2])
		total, _ := strconv.Atoi(m[3])
		out = append(out, census.Measurement{Target: m[1], Vantage: vantage, Kind: "tcp", Opened: opened, Total: total})
	}
	return out
}

// probeSPB opens the flows from the prober over one SSH connection. A script on
// stdin, built only from the hub's checked label and address; ports are the
// kernel's, so each flow has its own source port.
func (c *client) probeSPB() (int, error) {
	targets, err := c.dueTargets(censusVantage)
	if err != nil {
		return 0, err
	}
	if len(targets) == 0 {
		return 0, nil
	}
	script, err := spbScript(targets)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(targets)*6+30)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", spbHost, "bash -s")
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ssh %s: %v: %s", spbHost, err, strings.TrimSpace(stderr.String()))
	}
	ms := parseSPB(stdout.String())
	return len(ms), c.record(ms)
}

var (
	spbLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	spbAddr  = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):(\d{1,5})$`)
)

func spbScript(targets []census.Target) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "probe() { local n=0 d; d=$(mktemp -d); for i in $(seq %d); do (timeout 5 bash -c \"</dev/tcp/$2/$3\" 2>/dev/null && touch $d/$i) & done; wait; n=$(ls $d | wc -l); rm -rf $d; echo \"$1 $n\"; }\n", spbFlows)
	for _, t := range targets {
		m := spbAddr.FindStringSubmatch(t.Addr)
		if m == nil || !spbLabel.MatchString(t.Label) {
			return "", fmt.Errorf("target %q: bad label or address", t.Label)
		}
		fmt.Fprintf(&b, "probe %s %s %s\n", t.Label, m[1], m[2])
	}
	return b.String(), nil
}

func parseSPB(text string) []census.Measurement {
	var out []census.Measurement
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if n, err := strconv.Atoi(f[1]); err == nil {
			out = append(out, census.Measurement{Target: f[0], Vantage: censusVantage, Kind: "tcp", Opened: n, Total: spbFlows})
		}
	}
	return out
}

// protoField is the client's «label proto=2/3».
var protoField = regexp.MustCompile(`([a-z0-9][a-z0-9-]{0,31}) proto=(\d+)/(\d+)`)

func parseProtoCounts(text, vantage string) []census.Measurement {
	var out []census.Measurement
	for _, m := range protoField.FindAllStringSubmatch(text, -1) {
		opened, _ := strconv.Atoi(m[2])
		total, _ := strconv.Atoi(m[3])
		out = append(out, census.Measurement{Target: m[1], Vantage: vantage, Kind: "proto", Opened: opened, Total: total})
	}
	return out
}

func (c *client) censusProto(args map[string]string) (string, error) {
	out := map[string]any{}
	for _, who := range strings.Split(args["who"], ",") {
		if who = strings.TrimSpace(who); who == "" {
			continue
		}
		if n, err := c.protoTester(who); err != nil {
			out[who] = "error: " + err.Error()
		} else {
			out[who] = n
		}
	}
	if len(out) == 0 {
		return "", errors.New("census proto needs --who")
	}
	data, err := json.MarshalIndent(out, "", "  ")
	return string(data), err
}

func (c *client) protoTester(who string) (int, error) {
	vantage := "tester:" + who
	path, err := installPath(who, "/commands")
	if err != nil {
		return 0, err
	}
	done := 0
	for {
		data, err := c.request(http.MethodPost, "/api/v1/ops/census/proto", nil, map[string]string{"who": who, "vantage": vantage})
		if err != nil {
			return done, err
		}
		var asked struct {
			Targets int `json:"targets"`
			diagAnswer
		}
		if err := json.Unmarshal(data, &asked); err != nil {
			return done, fmt.Errorf("hub answer: %w", err)
		}
		if asked.Targets == 0 {
			return done, nil
		}
		text, err := c.waitAndRead(who, path, asked.diagAnswer, "protoprobe")
		if err != nil {
			return done, err
		}
		ms := parseProtoCounts(text, vantage)
		// Nothing counted means the client refused the whole command; asking
		// again would hand it the same profiles forever.
		if len(ms) == 0 {
			return done, errors.New("the client answered with no counts: see logs --src protoprobe")
		}
		if err := c.record(ms); err != nil {
			return done, err
		}
		done += len(ms)
	}
}

// censusWatch measures on a schedule from this machine: it runs while the
// machine is awake, and the hub's history shows any hole. An hour is the floor
// the hub holds anyway; a shorter tick only finds nothing due.
func (c *client) censusWatch(words []string) error {
	fs := flag.NewFlagSet("census watch", flag.ContinueOnError)
	who := fs.String("who", "", "testers, comma separated")
	every := fs.Duration("every", time.Hour, "time between rounds")
	if err := fs.Parse(words); err != nil {
		return err
	}
	if *every < census.MinInterval {
		return fmt.Errorf("--every is at least %s", census.MinInterval)
	}
	args := map[string]string{"who": *who}
	for {
		for _, step := range []string{"census-probe", "census-proto"} {
			if step == "census-proto" && *who == "" {
				continue
			}
			out, err := c.call(step, args)
			if err != nil {
				out = "error: " + err.Error()
			}
			fmt.Printf("%s %s %s\n", time.Now().Format(time.RFC3339), step, out)
		}
		time.Sleep(*every)
	}
}
