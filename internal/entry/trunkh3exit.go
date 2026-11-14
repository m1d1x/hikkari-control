package entry

import (
	"context"
	"fmt"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The stand's former exit end of Chromium's QUIC wire (R6.3): hk-exit as a
// unit of its own beside the relay, without a brain. Since R6.4.4 the wire
// ends in hk-exit under the relay (trunkpipeexit.go), so what is left here
// takes that unit off a box that still has it: entry-box trunk-pipe-exit
// does it before the relay binds the wire's port.
//
//	cmd/server entry-box trunk-h3-exit-off <exit-box>

const (
	trunkH3ExitUnit = "nekko-trunk-h3exit"
	trunkH3ExitDir  = "/usr/local/lib/nekko-trunk-h3exit"
	trunkH3ExitEnv  = "/etc/nekko-trunk-h3exit.env"
	trunkH3ExitKey  = "/etc/nekko-trunk-h3exit.key"
)

// trunkH3ExitOff takes the unit off, if the box has it, and closes its port
// in ufw: the relay's hk-exit opens its own (OpenTrunkWirePorts).
const trunkH3ExitOff = `set -eu
port=$(sed -n 's/^EDGE_TRUNK_H3_PORT=//p' ` + trunkH3ExitEnv + ` 2>/dev/null || true)
if [ ! -f /etc/systemd/system/` + trunkH3ExitUnit + `.service ] && [ -z "$port" ]; then
	echo "` + trunkH3ExitUnit + ` нет"
	exit 0
fi
systemctl disable --now ` + trunkH3ExitUnit + ` >/dev/null 2>&1 || true
if [ -n "$port" ] && command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
	ufw delete allow "$port/udp" >/dev/null 2>&1 || true
fi
rm -f /etc/systemd/system/` + trunkH3ExitUnit + `.service ` + trunkH3ExitEnv + ` ` + trunkH3ExitKey + `
rm -rf ` + trunkH3ExitDir + ` ` + trunkH3ExitDir + `.new
systemctl daemon-reload
echo "` + trunkH3ExitUnit + ` снят${port:+, порт $port/udp закрыт}"
`

// StopTrunkH3Exit takes the former unit off exitBox; the relay stays.
func (p *Provisioner) StopTrunkH3Exit(ctx context.Context, exitBox string) error {
	r, _, err := p.provokeRemote(ctx, exitBox)
	if err != nil {
		return err
	}
	defer r.Close()
	out, err := r.Run(ctx, "bash -s", strings.NewReader(trunkH3ExitOff))
	if err != nil {
		return fmt.Errorf("снятие %s: %w", trunkH3ExitUnit, err)
	}
	applog.Info(ctx, "entry", strings.TrimSpace(out), applog.F("box", exitBox))
	return nil
}
