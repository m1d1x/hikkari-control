package stand

// The stand's kit: what a vantage needs from the client to measure as a user
// would — the core users run (sing-box-lx with the client's patches) and the
// client's own link parser (census-parse), both linux-amd64, both built by the
// client's release from the same tag (hikkari-client scripts/publish-stand-kit.sh).
//
// They are published beside the update channel: blobs by SHA-256 under
// blobs/, and one manifest, stand/linux-amd64.json, naming them. A vantage
// reads the manifest, keeps each blob under its hash and checks the hash of
// every byte it runs. A kit it cannot read leaves it on the last one that
// passed: a release that is late does not stop the stand.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
)

// Files of a kit, by the name the manifest gives them.
const (
	KitCore   = "sing-box"
	KitParser = "census-parse"
)

// maxKitFile bounds one blob: the core is about 70 MB.
const maxKitFile = 256 << 20

var (
	reSHA       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reCoreLabel = regexp.MustCompile(`^[A-Za-z0-9._+ -]{1,80}$`)
)

// KitFile is one blob of the kit.
type KitFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Kit is the manifest.
type Kit struct {
	// Version is the client release the kit was built from.
	Version string `json:"version"`
	// Core names the core in every row: "sing-box-lx v1.14.3-lx.21+489ac0f3",
	// the tag and the first 8 hex of SHA-256 over the client's patches in
	// order, as `ops matrix` names the core it builds.
	Core  string             `json:"core"`
	Files map[string]KitFile `json:"files"`
}

// Validate refuses a manifest that names no core or parser, or a blob by
// anything but its hash.
func (k Kit) Validate() error {
	if !reCoreLabel.MatchString(k.Core) {
		return fmt.Errorf("kit core %q", k.Core)
	}
	for _, name := range []string{KitCore, KitParser} {
		f, ok := k.Files[name]
		if !ok {
			return fmt.Errorf("kit has no %s", name)
		}
		if !reSHA.MatchString(f.SHA256) || f.Size <= 0 || f.Size > maxKitFile {
			return fmt.Errorf("kit %s: sha256 %q size %d", name, f.SHA256, f.Size)
		}
	}
	return nil
}

// Local is a kit on disk, ready to run.
type Local struct {
	Core   string // path of the core
	Parser string // path of census-parse
	Label  string // the core's name in rows
}

// Fetch brings the kit at manifest into dir and returns it. Blobs already in
// dir under their hash are not fetched again. When the manifest or a blob
// cannot be had, the last kit that passed is returned with the error that
// kept the new one out; with none, only the error.
func Fetch(ctx context.Context, client *http.Client, manifest, dir string) (Local, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Local{}, err
	}
	kit, err := fetchKit(ctx, client, manifest, dir)
	if err == nil {
		return kit, nil
	}
	if last, lerr := lastKit(dir); lerr == nil {
		return last, fmt.Errorf("kit %s: %w (running the last kit, %s)", manifest, err, last.Label)
	}
	return Local{}, fmt.Errorf("kit %s: %w", manifest, err)
}

func fetchKit(ctx context.Context, client *http.Client, manifest, dir string) (Local, error) {
	base, err := url.Parse(manifest)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") {
		return Local{}, fmt.Errorf("manifest url %q", manifest)
	}
	raw, err := get(ctx, client, manifest, 1<<16)
	if err != nil {
		return Local{}, err
	}
	var kit Kit
	if err := json.Unmarshal(raw, &kit); err != nil {
		return Local{}, fmt.Errorf("manifest: %w", err)
	}
	if err := kit.Validate(); err != nil {
		return Local{}, err
	}
	for _, name := range []string{KitCore, KitParser} {
		f := kit.Files[name]
		blob := base.ResolveReference(&url.URL{Path: "../blobs/" + f.SHA256}).String()
		if err := fetchBlob(ctx, client, blob, dir, f); err != nil {
			return Local{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := writeAtomic(filepath.Join(dir, "kit.json"), raw, 0o600); err != nil {
		return Local{}, err
	}
	return localOf(kit, dir), nil
}

// lastKit is the kit dir last fetched whole.
func lastKit(dir string) (Local, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "kit.json"))
	if err != nil {
		return Local{}, err
	}
	var kit Kit
	if err := json.Unmarshal(raw, &kit); err != nil {
		return Local{}, err
	}
	if err := kit.Validate(); err != nil {
		return Local{}, err
	}
	for _, name := range []string{KitCore, KitParser} {
		if sum, err := fileSum(filepath.Join(dir, kit.Files[name].SHA256)); err != nil || sum != kit.Files[name].SHA256 {
			return Local{}, errors.New("the last kit is not whole on disk")
		}
	}
	return localOf(kit, dir), nil
}

func localOf(kit Kit, dir string) Local {
	return Local{
		Core:   filepath.Join(dir, kit.Files[KitCore].SHA256),
		Parser: filepath.Join(dir, kit.Files[KitParser].SHA256),
		Label:  kit.Core,
	}
}

// fetchBlob puts the blob at dir/<sha256> unless a whole one is there. The
// bytes are checked before the file takes its name, so a cut download never
// passes for the blob.
func fetchBlob(ctx context.Context, client *http.Client, blob, dir string, f KitFile) error {
	path := filepath.Join(dir, f.SHA256)
	if sum, err := fileSum(path); err == nil && sum == f.SHA256 {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blob, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %d", blob, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, ".blob-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, f.Size+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return fmt.Errorf("blob %s… came %d bytes not matching its hash", f.SHA256[:12], n)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func get(ctx context.Context, client *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func fileSum(path string) (string, error) {
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

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".kit-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
