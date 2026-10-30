package entry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/m1d1x/hikkari-control/internal/applog"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// The relay builds this hub carries (roadmap 42 П6.14 Ф1). The image is the
// artifact: linux-<arch>/nekko-edge under the artifact directory, fixed for
// the life of the process. Their digests are what "the version" means — a box
// runs the current build when its edge_sha equals the one here for its arch.

// Arches are the box architectures the hub deploys to.
var Arches = []string{"amd64", "arm64"}

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// EdgeBuild is one nekko-edge in the hub's image.
type EdgeBuild struct {
	Arch string `json:"arch"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`

	path string
}

// loadEdgeBuilds hashes the relay binaries under dir. An architecture with no
// binary is simply absent (a dev hub has none): a box of that architecture
// cannot be deployed, which the deploy step says when it gets there.
func loadEdgeBuilds(dir string) map[string]EdgeBuild {
	return loadNamedBuilds(dir, "nekko-edge")
}

// loadProberBuilds hashes the nekko-entry-prober binaries under dir so the hub
// can ship the prober over ssh when a fleet box is assigned to a vantage slot.
func loadProberBuilds(dir string) map[string]EdgeBuild {
	return loadNamedBuilds(dir, "nekko-entry-prober")
}

func loadNamedBuilds(dir, binName string) map[string]EdgeBuild {
	out := make(map[string]EdgeBuild, len(Arches))
	for _, arch := range Arches {
		path := filepath.Join(dir, "linux-"+arch, binName)
		sha, size, err := hashFile(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				applog.Error(context.Background(), "entry", "edge build unreadable", applog.F("bin", binName), applog.F("arch", arch), applog.F("err", err.Error()))
			}
			continue
		}
		out[arch] = EdgeBuild{Arch: arch, SHA: sha, Size: size, path: path}
	}
	return out
}

func hashFile(path string) (sha string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err = io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// read returns the build's bytes, refusing a file that is no longer the one
// hashed at start: the version recorded on the box must be what was sent.
func (b EdgeBuild) read() ([]byte, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != b.SHA {
		return nil, fmt.Errorf("бинарь релея %s изменился после старта хаба (%s… вместо %s…)", b.Arch, got[:12], b.SHA[:12])
	}
	return data, nil
}

// EdgeBuilds is the relay build per architecture this hub deploys, in arch
// order.
func (p *Provisioner) EdgeBuilds() []EdgeBuild {
	out := make([]EdgeBuild, 0, len(p.builds))
	for _, b := range p.builds {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Arch < out[j].Arch })
	return out
}
