package main

// A standalone prober needs no hub SSH key. Its root-owned bootstrap runs
// under the service's DynamicUser, and executes verified releases from its
// StateDirectory. No network-delivered code runs as root or writes /usr.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const maxProberSize = 256 << 20
const updateRestartCode = 75

var updateProbation = 3 * time.Minute

type proberRelease struct {
	SHA        string `json:"sha"`
	Size       int64  `json:"size"`
	SelfUpdate bool   `json:"selfUpdate"`
}

type updateState struct {
	SHA       string `json:"sha"`
	FailedSHA string `json:"failedSha,omitempty"`
	Pending   bool   `json:"pending"`
}

func updateDir() string { return os.Getenv("ENTRY_PROBE_UPDATE_DIR") }
func executableSHA() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return fileSHA(path)
}
func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func readUpdateState(dir string) (updateState, error) {
	var s updateState
	data, err := os.ReadFile(filepath.Join(dir, "update.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}
func writeUpdateState(dir string, s updateState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "update.json")
	if err = os.WriteFile(p+".new", b, 0600); err != nil {
		return err
	}
	return os.Rename(p+".new", p)
}

// bootstrap supervises only the update's probation. A crash or lack of a
// successful hub poll within three minutes restores .old before restarting.
func bootstrap(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	state, err := readUpdateState(dir)
	if err != nil {
		return err
	}
	bin := filepath.Join(dir, "nekko-entry-prober")
	candidate := self
	if state.SHA != "" {
		sha, e := fileSHA(bin)
		if e != nil || sha != state.SHA {
			if state.Pending {
				if err := rollbackUpdate(dir, state); err != nil {
					return err
				}
				return fmt.Errorf("bootstrap: staged binary sha mismatch; rolled back")
			}
			return fmt.Errorf("bootstrap: staged binary sha mismatch")
		}
		candidate = bin
	}
	child := exec.CommandContext(ctx, candidate)
	child.Env = append(os.Environ(), "ENTRY_PROBE_UPDATE_CHILD=1")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Stdin = os.Stdin
	child.Cancel = func() error { return child.Process.Signal(syscall.SIGTERM) }
	child.WaitDelay = 10 * time.Second
	if err = child.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	timer := time.NewTimer(updateProbation)
	defer timer.Stop()
	for {
		select {
		case err = <-done:
			current, e := readUpdateState(dir)
			if e != nil {
				return e
			}
			// The old process deliberately exits after staging a new pending build.
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == updateRestartCode {
				return nil
			}
			if state.Pending && current.Pending {
				if e = rollbackUpdate(dir, state); e != nil {
					return e
				}
			}
			return err
		case <-timer.C:
			current, e := readUpdateState(dir)
			if e != nil {
				_ = child.Process.Kill()
				<-done
				return e
			}
			if state.Pending && current.Pending {
				_ = child.Process.Kill()
				<-done
				if e = rollbackUpdate(dir, state); e != nil {
					return e
				}
				return fmt.Errorf("update: no confirmed hub poll within 3 minutes; rolled back")
			}
			// No pending build remains. Continue supervising shutdown/crashes.
		}
	}
}
func rollbackUpdate(dir string, state updateState) error {
	bin := filepath.Join(dir, "nekko-entry-prober")
	old := bin + ".old"
	oldSHA, err := fileSHA(old)
	if err != nil {
		return fmt.Errorf("rollback old binary: %w", err)
	}
	data, err := os.ReadFile(old)
	if err != nil {
		return err
	}
	if err = os.WriteFile(bin+".rollback", data, 0700); err != nil {
		return err
	}
	if err = os.Rename(bin+".rollback", bin); err != nil {
		return err
	}
	return writeUpdateState(dir, updateState{SHA: oldSHA, FailedSHA: state.SHA})
}

// downloadProber accepts only this hub's HTTPS endpoint, disallows redirects
// (the bearer must never follow one), bounds bytes and validates before rename.
func (h hubClient) downloadProber(ctx context.Context, release proberRelease, dest string) error {
	u, err := url.Parse(h.base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("update requires HTTPS hub")
	}
	if len(release.SHA) != 64 || release.Size <= 0 || release.Size > maxProberSize {
		return fmt.Errorf("invalid release manifest")
	}
	if _, err = hex.DecodeString(release.SHA); err != nil {
		return fmt.Errorf("invalid release digest")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/binary"
	u.RawQuery = url.Values{"arch": {runtime.GOARCH}, "sha": {release.SHA}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	client := *h.http
	client.Timeout = 3 * time.Minute
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download prober: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	defer os.Remove(dest)
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, release.Size+1))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != release.Size || hex.EncodeToString(hash.Sum(nil)) != release.SHA {
		return fmt.Errorf("download prober: size or SHA mismatch")
	}
	// The verified temporary stays for the atomic install.
	return os.Rename(dest, dest+".verified")
}

func (h hubClient) pollUpdate(ctx context.Context, sha string) (proberRelease, error) {
	var release proberRelease
	_, err := h.do(ctx, "GET", "/update?"+url.Values{"arch": {runtime.GOARCH}, "sha": {sha}}.Encode(), nil, &release)
	return release, err
}
func (cfg config) serveUpdates(ctx context.Context) {
	sha, err := executableSHA()
	if err != nil {
		log.Printf("update: executable sha: %v", err)
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		release, e := cfg.hub.pollUpdate(ctx, sha)
		if e != nil {
			log.Printf("update poll: %v", e)
		} else if dir := updateDir(); dir != "" {
			e = cfg.applyRelease(ctx, dir, sha, release)
			if e == nil && release.SelfUpdate && release.SHA != sha {
				log.Printf("update: verified build %s staged; restarting", release.SHA)
				// The bootstrap owns the child lifecycle; systemd restarts the bootstrap.
				os.Exit(updateRestartCode)
			}
			if e != nil {
				log.Printf("update: %v", e)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (cfg config) applyRelease(ctx context.Context, dir, sha string, release proberRelease) error {
	state, err := readUpdateState(dir)
	if err != nil {
		return err
	}
	// A valid hub response confirms the running build before any further swap.
	if state.Pending && state.SHA == sha {
		state.Pending = false
		if err = writeUpdateState(dir, state); err != nil {
			return err
		}
	}
	if !release.SelfUpdate || release.SHA == sha {
		return nil
	}
	if state.FailedSHA == release.SHA {
		return fmt.Errorf("build %s previously failed; awaiting a new hub build", release.SHA)
	}
	if os.Getenv("ENTRY_PROBE_UPDATE_CHILD") != "1" {
		return fmt.Errorf("self-update requires supervised systemd bootstrap")
	}
	bin := filepath.Join(dir, "nekko-entry-prober")
	temp := filepath.Join(dir, "download")
	_ = os.Remove(temp)
	_ = os.Remove(temp + ".verified")
	if err = cfg.hub.downloadProber(ctx, release, temp); err != nil {
		return err
	}
	defer os.Remove(temp + ".verified")
	// Save the running executable, including on the first update from /usr.
	self, err := os.Executable()
	if err != nil {
		return err
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	old, err := os.OpenFile(bin+".old.new", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(old, src)
	if err == nil {
		err = old.Sync()
	}
	closeErr := old.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(bin+".old.new", bin+".old"); err != nil {
		return err
	}
	if err = writeUpdateState(dir, updateState{SHA: release.SHA, Pending: true}); err != nil {
		return err
	}
	if err = os.Rename(temp+".verified", bin); err != nil {
		if restore := writeUpdateState(dir, state); restore != nil {
			return errors.Join(err, restore)
		}
		return err
	}
	return nil
}
