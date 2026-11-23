package entry

// The stream window of an entry over hk-pipe (hikkari-protocol spec/trunk.md,
// "Streams"; hikkari-client
// plans/research/2026-11-23-cascade-speed-bottleneck.md): a person's whole
// session through the entry is one stream, and its speed down is at most the
// window the entry gives over the time a chunk takes to be confirmed.
//
//	cmd/server entry-box trunk-stream-window <box> lab|stand|people|de <MiB>|off
//
// writes the window file beside the wires file of the lab (nekko-trunk-pipe-lab),
// the stand (nekko-trunk-pipe), the people's entry (nekko-trunk-entry) or the
// DE route (nekko-trunk-de). The running brain follows it within 10 s,
// with no new process, for the streams from then (trunkentry
// streamwindow.go). off is the pipe's own 2 MiB.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// TrunkStreamWindow is the window file: MiB 0 is the pipe's own.
type TrunkStreamWindow struct {
	MiB int `json:"stream_window_mib"`
}

// The window's bounds in MiB (pipectl MinStreamWindow, MaxStreamWindow).
const (
	minStreamWindowMiB = 2
	maxStreamWindowMiB = 64
)

// ParseTrunkStreamWindow reads <MiB> or off.
func ParseTrunkStreamWindow(raw string) (TrunkStreamWindow, error) {
	raw = strings.TrimSpace(raw)
	if raw == "off" {
		return TrunkStreamWindow{}, nil
	}
	mib, err := strconv.Atoi(raw)
	if err != nil || mib < minStreamWindowMiB || mib > maxStreamWindowMiB {
		return TrunkStreamWindow{}, fmt.Errorf("%w: %q, нужно МиБ (%d-%d) или off", ErrRejected, raw, minStreamWindowMiB, maxStreamWindowMiB)
	}
	return TrunkStreamWindow{MiB: mib}, nil
}

// trunkStreamWindowUnitEnv is the env each one's unit has; the file is
// beside it, env + ".window.json" (trunkentry streamWindowFilePath).
var trunkStreamWindowUnitEnv = map[string]string{
	"lab":    trunkPipeLabUnitSpec.env,
	"stand":  trunkPipeEnv,
	"people": trunkEntryEnvFile,
	"de":     countryDEUnit.env,
}

// SetTrunkStreamWindow writes the window file for the selected unit on box.
// Off removes it.
func (p *Provisioner) SetTrunkStreamWindow(ctx context.Context, box, who string, w TrunkStreamWindow) error {
	env, ok := trunkStreamWindowUnitEnv[who]
	if !ok {
		return fmt.Errorf("%w: %q, lab, stand, people или de", ErrRejected, who)
	}
	path := env + ".window.json"
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	check := "test -f " + env + " || { echo 'нет " + env + ": вход не стоит' >&2; exit 1; }; "
	script, body := check+"rm -f "+path, ""
	if w.MiB != 0 {
		raw, err := json.Marshal(w)
		if err != nil {
			return err
		}
		script, body = check+"umask 022; cat > "+path+".new && mv "+path+".new "+path, string(raw)+"\n"
	}
	if out, err := r.Run(ctx, script, strings.NewReader(body)); err != nil {
		return fmt.Errorf("файл окна потока на %s: %w: %s", box, err, out)
	}
	applog.Info(ctx, "entry", "operator trunk stream window", applog.F("box", box), applog.F("who", who), applog.F("mib", w.MiB))
	return nil
}
