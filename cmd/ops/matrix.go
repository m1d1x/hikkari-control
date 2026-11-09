package main

// ops matrix runs the stand (internal/stand) by hand from this machine: the
// profile is parsed here by the client's own parser, the core is built here
// and copied to the vantages, and the script runs on each over SSH. The hub
// runs the same stand on its schedule through the vantages' probers
// (internal/stand schedule.go); this is for an experiment of one's own.
//
// The profile's outbound holds its secrets. It goes to our own vantages on SSH
// stdin, into a private temporary directory, and never into a row.

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-control/internal/stand"
)

const (
	// matrixUploadPart is one part of the core's upload: small enough to pass
	// a link that cuts long flows.
	matrixUploadPart = 4 << 20
	// matrixCoreDir is where a vantage keeps the core between runs.
	matrixCoreDir = "/tmp/hikkari-matrix"
)

// matrixVantages are the vantages by name: one in Russia, one outside as the
// control that tells the censor from the server or the config.
var matrixVantages = map[string]string{"spb": "nekko-spb", "lv": "hikkari-main"}

// matrix runs the stand and prints a summary, then the rows as NDJSON.
func (c *client) matrix(args map[string]string) (string, error) {
	plan, err := matrixPlanOf(args)
	if err != nil {
		return "", err
	}
	vantages, err := matrixVantageList(args["vantage"])
	if err != nil {
		return "", err
	}
	core, err := matrixCore()
	if err != nil {
		return "", err
	}
	plan.Core = core.Label
	script, err := stand.Script(plan, matrixCoreDir+"/"+stand.KitCore)
	if err != nil {
		return "", err
	}
	var (
		mu   sync.Mutex
		rows []stand.Row
		errs []string
		wg   sync.WaitGroup
	)
	for _, v := range vantages {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			got, err := matrixRun(name, matrixVantages[name], core, script, plan)
			mu.Lock()
			defer mu.Unlock()
			rows = append(rows, got...)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			}
		}(v)
	}
	wg.Wait()
	out := stand.Summary(rows)
	if plan.Base != "" {
		out = "база: " + plan.Base + "\n" + out
	}
	if args["profile"] != plan.Profile {
		out = "профиль " + args["profile"] + " в датасете: " + plan.Profile + "\n" + out
	}
	if len(rows) > 0 {
		// Every run is history: the rows go to the hub's dataset, so the next
		// question about this knob starts from them, not from scrollback.
		// A dataset the hub refused does not undo the run: its rows are below
		// and can be sent again; the failure is said, not raised.
		if n, err := c.recordMatrix(rows); err != nil {
			out += "\nдатасет на hub: не записан: " + err.Error()
		} else {
			out += fmt.Sprintf("\nдатасет на hub: %d новых строк из %d", n, len(rows))
		}
	}
	for _, r := range rows {
		line, _ := json.Marshal(r)
		out += "\n" + string(line)
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return out, errors.New(strings.Join(errs, "; "))
	}
	return out, nil
}

// recordMatrix sends the run's rows to the hub's relay dataset.
func (c *client) recordMatrix(rows []stand.Row) (int, error) {
	out, err := stand.DatasetRows(rows)
	if err != nil {
		return 0, err
	}
	data, err := c.request(http.MethodPost, "/api/v1/ops/relay-runs", nil, out)
	if err != nil {
		return 0, err
	}
	var ans struct {
		Added int `json:"added"`
	}
	if err := json.Unmarshal(data, &ans); err != nil {
		return 0, errors.New("unreadable answer")
	}
	return ans.Added, nil
}

// runsRecord sends the rows of a saved matrix output (--file) to the dataset:
// for a run whose rows the hub refused at the time.
func (c *client) runsRecord(args map[string]string) (string, error) {
	data, err := os.ReadFile(args["file"])
	if err != nil {
		return "", err
	}
	var rows []stand.Row
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r stand.Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return "", fmt.Errorf("row %q: %w", line, err)
		}
		if r.BurstTotal == 0 && !r.LoadError {
			r.BurstTotal = stand.Burst // rows printed before burst_total was written
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return "", errors.New("no rows in the file")
	}
	n, err := c.recordMatrix(rows)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("датасет на hub: %d новых строк из %d", n, len(rows)), nil
}

// matrixPlanOf reads the arguments and the profile. The subscription is parsed
// by the client's own parser (census-parse), as census add does.
func matrixPlanOf(args map[string]string) (stand.Plan, error) {
	sub, label, knob := args["sub"], args["profile"], args["knob"]
	if sub == "" || label == "" || knob == "" {
		return stand.Plan{}, errors.New("matrix needs --sub, --profile and --knob name=v1,v2")
	}
	name, list, ok := strings.Cut(knob, "=")
	if !ok {
		return stand.Plan{}, fmt.Errorf("knob %q: want one of %s as name=v1,v2", knob, strings.Join(stand.KnobNames(), ", "))
	}
	spec := stand.Spec{Profile: label, Knob: name, Values: strings.Split(list, ","), Base: args["base"], BasePath: args["baseline_path"]}
	if v := args["rounds"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > stand.MaxRounds {
			return stand.Plan{}, fmt.Errorf("rounds: 1 to %d", stand.MaxRounds)
		}
		spec.Rounds = n
	}
	outbound, name, err := matrixOutbound(sub, label)
	if err != nil {
		return stand.Plan{}, err
	}
	// Rows name the profile as the hub's stand does: the hub refuses the
	// subscription's own label when it is not ASCII.
	spec.Profile = name
	return stand.NewPlan(outbound, spec)
}

// matrixOutbound is the profile's sing-box outbound as the client builds it,
// and the profile's name in the dataset (stand.DatasetLabel).
func matrixOutbound(sub, label string) (map[string]interface{}, string, error) {
	dir, err := clientEngineDir()
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/census-parse", "--with-config")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(sub + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("census-parse: %s", strings.ReplaceAll(strings.TrimSpace(stderr.String()), sub, "[subscription]"))
	}
	return matrixPick(stdout.Bytes(), label)
}

// matrixPick finds the profile by its label in census-parse's answer.
func matrixPick(raw []byte, label string) (map[string]interface{}, string, error) {
	var entries []stand.Profile
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, "", errors.New("census-parse: unreadable answer")
	}
	var labels []string
	for _, e := range entries {
		if e.Label == label && e.Config != nil {
			return e.Config, stand.DatasetLabel(e), nil
		}
		labels = append(labels, e.Label)
	}
	return nil, "", fmt.Errorf("no profile %q in the subscription, it has: %s", label, strings.Join(labels, ", "))
}

func matrixVantageList(s string) ([]string, error) {
	if s == "" {
		s = "spb,lv"
	}
	var out []string
	for _, v := range strings.Split(s, ",") {
		if _, ok := matrixVantages[v]; !ok {
			return nil, fmt.Errorf("vantage %q: want spb or lv", v)
		}
		out = append(out, v)
	}
	return out, nil
}

// matrixRun puts the core and its libcronet.so on the vantage if they are not
// there yet and runs the script over one SSH connection.
func matrixRun(name, host string, core matrixCoreFiles, script string, p stand.Plan) ([]stand.Row, error) {
	// The core loads libcronet.so for Naive and Hikkari from its own
	// directory: without it every such outbound fails to start
	// ("cronet: library not found", load_error=true on every row).
	if err := matrixUpload(host, core.Core, stand.KitCore, 0o755); err != nil {
		return nil, err
	}
	if err := matrixUpload(host, core.Cronet, stand.KitCronet, 0o644); err != nil {
		return nil, err
	}
	wait := p.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", host, "bash -s")
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	rows := stand.Parse(stdout.String(), name, p)
	if err != nil {
		return rows, fmt.Errorf("ssh %s: %v: %s", host, err, strings.TrimSpace(stderr.String()))
	}
	return rows, nil
}

// matrixUpload copies a file of the core into matrixCoreDir under name unless
// the vantage already holds the same one, by SHA-256.
func matrixUpload(host, local, name string, mode os.FileMode) error {
	sum, err := fileSHA256(local)
	if err != nil {
		return err
	}
	remote := matrixCoreDir + "/" + name
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	have, _ := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", host,
		"sha256sum "+remote+" 2>/dev/null | cut -d' ' -f1").Output()
	if strings.TrimSpace(string(have)) == sum {
		return nil
	}
	// The core is 70 MB and the link from here to a vantage can cut a long
	// flow (04.11: 25 MB died at both vantages while 5 MB passed). So it goes
	// gzipped, in parts small enough to pass, each retried on its own; the
	// vantage checks the whole against the hash before it replaces the old
	// core, so a cut upload never leaves a half core for the next run.
	parts, err := gzipParts(local, matrixUploadPart)
	if err != nil {
		return err
	}
	dir := matrixCoreDir + "/up-" + name
	if out, err := sshRun(host, "rm -rf "+dir+" && mkdir -p "+dir, nil, time.Minute); err != nil {
		return fmt.Errorf("upload to %s: %v: %s", host, err, out)
	}
	for i, part := range parts {
		var last error
		for try := 0; try < 3; try++ {
			out, err := sshRun(host, fmt.Sprintf("cat > %s/p%04d", dir, i), part, 3*time.Minute)
			if err == nil {
				last = nil
				break
			}
			last = fmt.Errorf("part %d/%d: %v: %s", i+1, len(parts), err, out)
		}
		if last != nil {
			return fmt.Errorf("upload to %s: %w", host, last)
		}
	}
	// A file of its own: a writer left from a cut upload must not share it, or
	// the core is renamed into place still open for writing ("text file busy").
	join := fmt.Sprintf(`set -e; t=$(mktemp %[2]s.XXXXXX); cat %[1]s/p* | gunzip > $t; [ "$(sha256sum $t | cut -d' ' -f1)" = %[3]s ] || { rm -f $t; echo hash mismatch; exit 1; }; chmod %[4]o $t; mv -f $t %[2]s; rm -rf %[1]s`, dir, remote, sum, mode)
	if out, err := sshRun(host, join, nil, 2*time.Minute); err != nil {
		return fmt.Errorf("upload to %s: %v: %s", host, err, out)
	}
	return nil
}

// gzipParts is a file gzipped and cut into parts of at most size bytes.
func gzipParts(path string, size int) ([][]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	all := buf.Bytes()
	var parts [][]byte
	for len(all) > 0 {
		n := min(size, len(all))
		parts = append(parts, all[:n])
		all = all[n:]
	}
	return parts, nil
}

// sshRun runs a command on host with stdin, within wait.
func sshRun(host, command string, stdin []byte, wait time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=10", host, command)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// matrixCoreFiles is the core and the libcronet.so it loads, both
// linux-amd64, from one tag of the fork.
type matrixCoreFiles struct {
	Core, Cronet, Label string
}

// matrixCore is the core users run, built for the vantages: our fork
// m1d1x/sing-box-lx at the tag the client's CI pins (its patches are commits
// there), with the libcronet.so of the cronet-go that tag's go.mod pins, as
// the client's stand kit takes it (hikkari-client scripts/build-stand-kit.sh).
// Both are cached by the tag.
func matrixCore() (matrixCoreFiles, error) {
	var out matrixCoreFiles
	engine, err := clientEngineDir()
	if err != nil {
		return out, err
	}
	repo := filepath.Dir(engine)
	workflow, err := os.ReadFile(filepath.Join(repo, ".github", "workflows", "build-windows.yml"))
	if err != nil {
		return out, err
	}
	m := regexp.MustCompile(`(?m)^\s*SING_BOX_LX_TAG:\s*(\S+)`).FindSubmatch(workflow)
	if m == nil {
		return out, errors.New("no SING_BOX_LX_TAG in the client's build-windows.yml")
	}
	tag := string(m[1])
	cache, err := os.UserCacheDir()
	if err != nil {
		return out, err
	}
	dir := filepath.Join(cache, "hikkari", "matrix")
	out = matrixCoreFiles{
		Core:   filepath.Join(dir, "sing-box-"+tag+"-linux-amd64"),
		Cronet: filepath.Join(dir, "libcronet-"+tag+"-linux-amd64.so"),
		Label:  "sing-box-lx " + tag,
	}
	core, cronet := out.Core, out.Cronet
	if _, err := os.Stat(core); err == nil {
		core = ""
	}
	if _, err := os.Stat(cronet); err == nil {
		cronet = ""
	}
	if core == "" && cronet == "" {
		return out, nil
	}
	if err := buildMatrixCore(tag, core, cronet); err != nil {
		return out, err
	}
	return out, nil
}

// buildMatrixCore follows the client's CI (build-windows.yml, job sing-box):
// the core into core and the tag's libcronet.so into cronet; an empty path is
// left alone.
func buildMatrixCore(tag, core, cronet string) error {
	work, err := os.MkdirTemp("", "hikkari-matrix-core-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	src := filepath.Join(work, "sbx")
	run := func(dir string, env []string, name string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	if _, err := run(work, nil, "git", "clone", "-q", "--depth", "1", "--branch", tag, "https://github.com/m1d1x/sing-box-lx", src); err != nil {
		return err
	}
	if cronet != "" {
		// The module the tag's go.mod pins, as the client's stand kit takes it.
		raw, err := run(src, []string{"GOOS=linux", "GOARCH=amd64"}, "go", "mod", "download", "-json", "github.com/sagernet/cronet-go/lib/linux_amd64")
		if err != nil {
			return err
		}
		var mod struct{ Dir, Error string }
		if err := json.Unmarshal([]byte(raw), &mod); err != nil {
			return fmt.Errorf("go mod download cronet-go/lib/linux_amd64: %w", err)
		}
		if mod.Error != "" || mod.Dir == "" {
			return fmt.Errorf("go mod download cronet-go/lib/linux_amd64: %s", mod.Error)
		}
		lib, err := os.ReadFile(filepath.Join(mod.Dir, "libcronet.so"))
		if err != nil {
			return err
		}
		if err := writeMatrixFile(cronet, lib, 0o644); err != nil {
			return err
		}
	}
	if core == "" {
		return nil
	}
	if _, err := run(src, nil, "git", "submodule", "update", "-q", "--init", "--depth", "1",
		"submodules/gvisor", "submodules/sing-tun", "submodules/utls", "submodules/wireguard-go"); err != nil {
		return err
	}
	tags, err := run(src, nil, "make", "-s", "-f", "Makefile.lx", "lx-print-tags")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(core), 0o755); err != nil {
		return err
	}
	tmp := core + ".tmp"
	if _, err := run(src, []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0"}, "go", "build", "-trimpath", "-tags", tags,
		"-ldflags", "-checklinkname=0 -s -w -buildid=", "-o", tmp, "./cmd/sing-box"); err != nil {
		return err
	}
	return os.Rename(tmp, core)
}

// writeMatrixFile writes data to path whole or not at all.
func writeMatrixFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
