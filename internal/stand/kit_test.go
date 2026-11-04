package stand

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
	"sync/atomic"
	"testing"
)

type kitServer struct {
	blobs    map[string][]byte
	manifest []byte
	gets     atomic.Int32
	down     atomic.Bool
}

func newKitServer(t *testing.T, core, parser []byte, label string) (*kitServer, *httptest.Server) {
	ks := &kitServer{blobs: map[string][]byte{}}
	files := map[string]KitFile{}
	for name, data := range map[string][]byte{KitCore: core, KitParser: parser} {
		s := sha256.Sum256(data)
		sum := hex.EncodeToString(s[:])
		ks.blobs[sum] = data
		files[name] = KitFile{SHA256: sum, Size: int64(len(data))}
	}
	ks.manifest, _ = json.Marshal(Kit{Version: "1.0.36", Core: label, Files: files})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ks.down.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		switch {
		case r.URL.Path == "/stand/linux-amd64.json":
			_, _ = w.Write(ks.manifest)
		case strings.HasPrefix(r.URL.Path, "/blobs/"):
			ks.gets.Add(1)
			b, ok := ks.blobs[strings.TrimPrefix(r.URL.Path, "/blobs/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return ks, srv
}

func TestKitFetchKeepsBlobsByHashAndFallsBackToTheLastOne(t *testing.T) {
	ks, srv := newKitServer(t, []byte("core bytes"), []byte("parser bytes"), "sing-box-lx v1.14.3-lx.21+489ac0f3")
	dir := filepath.Join(t.TempDir(), "kit")
	manifest := srv.URL + "/stand/linux-amd64.json"
	got, err := Fetch(context.Background(), srv.Client(), manifest, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "sing-box-lx v1.14.3-lx.21+489ac0f3" {
		t.Fatalf("label %q", got.Label)
	}
	if b, _ := os.ReadFile(got.Core); string(b) != "core bytes" {
		t.Fatalf("core %q", b)
	}
	if st, _ := os.Stat(got.Parser); st.Mode().Perm()&0o100 == 0 {
		t.Fatal("parser not executable")
	}
	if _, err := Fetch(context.Background(), srv.Client(), manifest, dir); err != nil || ks.gets.Load() != 2 {
		t.Fatalf("blobs fetched again (%d gets) err %v", ks.gets.Load(), err)
	}
	ks.down.Store(true)
	last, err := Fetch(context.Background(), srv.Client(), manifest, dir)
	if err == nil || last.Core != got.Core {
		t.Fatalf("no fallback to the last kit: %+v %v", last, err)
	}
}

func TestKitFetchRefusesABlobNotMatchingItsHash(t *testing.T) {
	ks, srv := newKitServer(t, []byte("core bytes"), []byte("parser bytes"), "sing-box-lx v1")
	for sum := range ks.blobs {
		ks.blobs[sum] = []byte("tampered!!")
	}
	dir := t.TempDir()
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/stand/linux-amd64.json", dir); err == nil {
		t.Fatal("a blob that does not match its hash taken")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) == 64 {
			t.Fatalf("a bad blob left under a hash name: %s", e.Name())
		}
	}
}

func TestKitValidateWantsCoreAndParserByHash(t *testing.T) {
	good := Kit{Core: "sing-box-lx v1", Files: map[string]KitFile{
		KitCore:   {SHA256: strings.Repeat("a", 64), Size: 1},
		KitParser: {SHA256: strings.Repeat("b", 64), Size: 1},
	}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.Files = map[string]KitFile{KitCore: good.Files[KitCore], KitParser: {SHA256: "../../etc/passwd", Size: 1}}
	if bad.Validate() == nil {
		t.Fatal("a blob named by a path taken")
	}
	bad = good
	bad.Core = "x; rm -rf /"
	if bad.Validate() == nil {
		t.Fatal("a core label with shell in it taken")
	}
}
