package entry

// The pool's members of copies on an entry over hk-pipe (hikkari-client
// plans/2026-11-15-pool-copy-coalescing.md; hikkari-protocol spec/trunk.md,
// "Copies on the pool"):
//
//	cmd/server entry-box trunk-copies <box> stand|people <members>:<window_ms>|demand:<s>|off
//
// writes the copies file beside the wires file of the stand
// (nekko-trunk-pipe) or of the people's entry (nekko-trunk-entry), and only
// it: the running brain follows it within 10 s, with no new process and no
// flow or stream cut (trunkentry copies.go). off is the copies going round
// every member of the pool, as before the file.
//
// demand:<s> is the copies on demand (hikkari-client
// plans/2026-11-18-copies-on-demand.md; spec/trunk.md, "Copies on demand"):
// the voice class's copy on the pool goes only while a packet wire loses,
// held s seconds after a loss (the owner's 300, 18.11); the copies go round
// the pool. off is a copy of every datagram again.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// TrunkCopies is the copies file: how many of the pool's members carry the
// copies, their window in ms, and the hold of the copies on demand in s (0:
// a copy of every datagram).
type TrunkCopies struct {
	Members   int `json:"members"`
	WindowMS  int `json:"window_ms"`
	OnDemandS int `json:"on_demand_s,omitempty"`
}

// ParseTrunkCopies reads <members>:<window_ms>, demand:<s> or off.
func ParseTrunkCopies(raw string) (TrunkCopies, error) {
	raw = strings.TrimSpace(raw)
	if raw == "off" {
		return TrunkCopies{}, nil
	}
	if s, ok := strings.CutPrefix(raw, "demand:"); ok {
		hold, err := strconv.Atoi(s)
		if err != nil || hold < 1 || hold > 3600 {
			return TrunkCopies{}, fmt.Errorf("%w: %q, нужно demand:секунды (1-3600)", ErrRejected, raw)
		}
		return TrunkCopies{OnDemandS: hold}, nil
	}
	m, w, ok := strings.Cut(raw, ":")
	members, err1 := strconv.Atoi(m)
	window, err2 := strconv.Atoi(w)
	if !ok || err1 != nil || err2 != nil || members < 1 || members > 32 || window < 0 || window > 50 {
		return TrunkCopies{}, fmt.Errorf("%w: %q, нужно члены:окно_мс (1-32, 0-50), demand:секунды или off", ErrRejected, raw)
	}
	return TrunkCopies{Members: members, WindowMS: window}, nil
}

// trunkCopiesFiles are the copies files by who the entry carries, beside
// each one's wires file, env + ".wires.json" (pipeBrainEnv; trunkentry
// copiesFilePath).
var trunkCopiesFiles = map[string]string{
	"stand":  trunkPipeEnv + ".copies.json",
	"people": trunkEntryEnvFile + ".copies.json",
}

// trunkCopiesUnitEnv is the env each one's unit has: no unit, no file.
var trunkCopiesUnitEnv = map[string]string{"stand": trunkPipeEnv, "people": trunkEntryEnvFile}

// SetTrunkCopies writes the copies file of the stand or the people's entry
// on box.
func (p *Provisioner) SetTrunkCopies(ctx context.Context, box, who string, c TrunkCopies) error {
	path, ok := trunkCopiesFiles[who]
	if !ok {
		return fmt.Errorf("%w: %q, stand или people", ErrRejected, who)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	env := trunkCopiesUnitEnv[who]
	script := "test -f " + env + " || { echo 'нет " + env + ": вход не стоит' >&2; exit 1; }; " +
		"umask 022; cat > " + path + ".new && mv " + path + ".new " + path
	if out, err := r.Run(ctx, script, strings.NewReader(string(raw)+"\n")); err != nil {
		return fmt.Errorf("файл копий на %s: %w: %s", box, err, out)
	}
	applog.Info(ctx, "entry", "operator trunk copies", applog.F("box", box), applog.F("who", who), applog.F("copies", string(raw)))
	return nil
}
