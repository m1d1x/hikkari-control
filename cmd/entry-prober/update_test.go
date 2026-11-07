package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func releaseOf(b []byte) proberRelease {
	s := sha256.Sum256(b)
	return proberRelease{SHA: hex.EncodeToString(s[:]), Size: int64(len(b)), SelfUpdate: true}
}
func TestDownloadProberChecksDigestSizeAuthAndRedirect(t *testing.T) {
	binary := []byte("new verified prober")
	var mode string
	targetHits := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits++; w.Write(binary) }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer slot-secret" {
			t.Error("no slot auth")
		}
		if r.URL.Query().Get("sha") != releaseOf(binary).SHA {
			t.Error("unbound build")
		}
		switch mode {
		case "redirect":
			http.Redirect(w, r, target.URL, 302)
		case "corrupt":
			w.Write([]byte("wrong build"))
		case "oversize":
			w.Write(append(binary, 'x'))
		default:
			w.Write(binary)
		}
	}))
	defer srv.Close()
	h := hubClient{base: srv.URL, token: "slot-secret", http: srv.Client()}
	for _, testMode := range []string{"ok", "corrupt", "oversize", "redirect"} {
		mode = testMode
		dest := filepath.Join(t.TempDir(), "download")
		err := h.downloadProber(context.Background(), releaseOf(binary), dest)
		if mode == "ok" {
			if err != nil {
				t.Fatal(err)
			}
			got, e := os.ReadFile(dest + ".verified")
			if e != nil || string(got) != string(binary) {
				t.Fatal("verified binary missing")
			}
		} else {
			if err == nil {
				t.Fatalf("%s accepted", mode)
			}
			if _, e := os.Stat(dest + ".verified"); !os.IsNotExist(e) {
				t.Fatal("unverified binary retained")
			}
		}
	}
	if targetHits != 0 {
		t.Fatal("followed redirect")
	}
	h.base = "http://localhost"
	if err := h.downloadProber(context.Background(), releaseOf(binary), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("plaintext update accepted")
	}
}

func TestSelfUpdateStagesVerifiedBinaryAndPreservesOld(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ENTRY_PROBE_UPDATE_CHILD", "1")
	binary := []byte("new build")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(binary) }))
	defer srv.Close()
	cfg := config{hub: hubClient{base: srv.URL, http: srv.Client()}}
	self, _ := os.Executable()
	oldSHA, _ := fileSHA(self)
	if err := cfg.applyRelease(context.Background(), dir, oldSHA, releaseOf(binary)); err != nil {
		t.Fatal(err)
	}
	s, err := readUpdateState(dir)
	if err != nil || !s.Pending || s.SHA != releaseOf(binary).SHA {
		t.Fatalf("state %+v %v", s, err)
	}
	got, _ := fileSHA(filepath.Join(dir, "nekko-entry-prober.old"))
	if got != oldSHA {
		t.Fatal("old executable lost")
	}
	if err = rollbackUpdate(dir, s); err != nil {
		t.Fatal(err)
	}
	s, _ = readUpdateState(dir)
	if s.SHA != oldSHA || s.FailedSHA != releaseOf(binary).SHA || s.Pending {
		t.Fatalf("rollback: %+v", s)
	}
	got, _ = fileSHA(filepath.Join(dir, "nekko-entry-prober.old"))
	if got != oldSHA {
		t.Fatal("rollback consumed .old")
	}
	if err = cfg.applyRelease(context.Background(), dir, oldSHA, releaseOf(binary)); err == nil {
		t.Fatal("failed build retried")
	}
}

func TestSelfUpdateConfirmsSuccessfulPollAndLeavesManagedBoxAlone(t *testing.T) {
	dir := t.TempDir()
	current := strings.Repeat("a", 64)
	if err := writeUpdateState(dir, updateState{SHA: current, Pending: true}); err != nil {
		t.Fatal(err)
	}
	cfg := config{}
	if err := cfg.applyRelease(context.Background(), dir, current, proberRelease{SHA: strings.Repeat("b", 64), SelfUpdate: false}); err != nil {
		t.Fatal(err)
	}
	state, _ := readUpdateState(dir)
	if state.Pending || state.SHA != current {
		t.Fatalf("confirmation %+v", state)
	}
	if _, err := os.Stat(filepath.Join(dir, "nekko-entry-prober")); !os.IsNotExist(err) {
		t.Fatal("managed box replaced itself")
	}
}

func TestBootstrapRollsBackCrashAndMissingPoll(t *testing.T) {
	oldProbation := updateProbation
	updateProbation = 40 * time.Millisecond
	defer func() { updateProbation = oldProbation }()
	for _, script := range []string{"#!/bin/sh\nexit 1\n", "#!/bin/sh\nexec sleep 60\n"} {
		t.Run(strings.TrimSpace(strings.Split(script, "\n")[1]), func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "nekko-entry-prober")
			if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(bin+".old", []byte("old build"), 0700); err != nil {
				t.Fatal(err)
			}
			sha, _ := fileSHA(bin)
			if err := writeUpdateState(dir, updateState{SHA: sha, Pending: true}); err != nil {
				t.Fatal(err)
			}
			if err := bootstrap(context.Background(), dir); err == nil {
				t.Fatal("unconfirmed build accepted")
			}
			state, err := readUpdateState(dir)
			if err != nil || state.Pending || state.FailedSHA != sha {
				t.Fatalf("%+v %v", state, err)
			}
			got, _ := os.ReadFile(bin)
			if string(got) != "old build" {
				t.Fatal("old build not restored")
			}
		})
	}
}

func TestUpdatePollReportsExecutableDigest(t *testing.T) {
	sha := strings.Repeat("a", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sha") != sha {
			t.Error("running sha missing")
		}
		json.NewEncoder(w).Encode(proberRelease{SHA: sha})
	}))
	defer srv.Close()
	h := hubClient{base: srv.URL, http: srv.Client()}
	if _, err := h.pollUpdate(context.Background(), sha); err != nil {
		t.Fatal(err)
	}
}
