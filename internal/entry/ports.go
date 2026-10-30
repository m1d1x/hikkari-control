package entry

// ports.go keeps a box at its number of relays (roadmap 42 П9.7, cause
// ports_banned).
//
// # Why a box holds more than one relay
//
// A censor cuts an address port by port as often as whole. A box with one
// relay has nothing between "this port is cut" and "this machine is dead", so
// every port ban reads as ip_banned and is repaired by a new machine — the one
// repair that costs money. Two relays on two ports make a port ban what it is:
// the banned one is retired, the other keeps carrying people, and a fresh
// port takes the dead one's place. All of it free and all of it the hub's own
// doing: nobody adds ports by hand (owner, 25.10), the box is minted at its
// target and brought back to it after every retirement.
//
// # The dead are swept off the box first
//
// Retiring a relay is a row in the hub: the box keeps running its unit, its
// env (the relay's private keys) stays on disk and its port stays open in the
// firewall, which only ever gains rules. The top-up is the first job on the
// box after a retirement, so it is the one that takes them off — a seized box
// should not carry keys of relays nobody serves, and a banned port left open
// is a port the censor keeps a reason to look at.
//
// # Why only the new relays are deployed
//
// The repair runs on a box that is carrying people on the ports that still
// answer. A full re-provision restarts every unit on it, which cuts exactly
// the connections the repair exists to keep. The top-up deploys and admits
// the fresh relays alone; the firewall is the only thing it writes for the
// whole box, because it has to list every port that is meant to be open.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// CauseShortOfPorts: every relay the box has answers, and it has fewer than
// its protocol keeps — a port was retired, killed by hand, or the box was
// built before it had a spare. Read only on a box that is otherwise healthy:
// a box with a real failure is that failure first, and a spare port added to
// a box being repaired would be deployed into the middle of the repair.
const CauseShortOfPorts = "short_of_ports"

// shortOfPorts turns a healthy reading into CauseShortOfPorts when the box
// holds fewer live relays than [relayTarget]. relays are the box's relays as
// the pool holds them.
//
// Counted over relays not dead, not only the alive ones: a relay waiting for
// admission is already the spare, and minting another beside it would leave
// the box over its target once both are in.
func shortOfPorts(d Diagnosis, relays []PoolRelay) Diagnosis {
	if d.Cause != CauseHealthy || len(relays) == 0 {
		return d
	}
	live, proto := 0, ""
	for _, r := range relays {
		if r.Status != StatusDead {
			live++
			proto = r.Proto
		}
	}
	if want := relayTarget(proto); live < want {
		d.Cause = CauseShortOfPorts
		d.Advice = fmt.Sprintf("релеев на машине %d из %d: поднять запасной порт", live, want)
	}
	return d
}

// readsHealthy says every relay the box has got through: healthy, or healthy
// and short of a spare port or of a cover. The detector reads them alike
// wherever the question is "did the box recover" rather than "is there work
// to do".
func readsHealthy(cause string) bool {
	return cause == CauseHealthy || cause == CauseShortOfPorts || cause == CauseShortOfCovers
}

// relayTarget is how many relays a box of this protocol keeps.
//
// Two for Reality: one spare port is what turns a port ban into a free
// repair, and a third would buy nothing a second does not. One for XHTTP,
// whose site and certificate live on :443 of the box's one domain (see
// [Service.AddRelay]), and for Hysteria2, whose relay is a UDP door in front
// of one region's SFUs — a second one would split nothing the censor cuts
// separately.
func relayTarget(proto string) int {
	if proto == ProtoVlessRealityVision {
		return 2
	}
	return 1
}

// TopUp mints, deploys and admits relays on a box in service until it holds
// [relayTarget] of them again, without touching the relays already there.
// Returns the names admitted.
//
// A fresh relay the vantage reads as ip_banned is marked dead on the spot:
// its port is cut on this address, and AddRelay skips a port that died that
// way, so the next top-up takes the next one instead of being born dead on
// the same port forever. Any other refusal leaves it pending — an unjudged
// relay is not a banned one.
func (p *Provisioner) TopUp(ctx context.Context, box string) ([]string, error) {
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return nil, err
	}
	if acc.retired {
		return nil, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	var admitted []string
	err = p.execJob(ctx, JobProvision, box, uuid.Nil, func(j *jobRun) error {
		var err error
		admitted, err = j.topUp()
		return err
	})
	return admitted, err
}

func (j *jobRun) topUp() ([]string, error) {
	svc := j.p.svc
	j.step("login")
	r, err := j.loginByKey()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	before, err := j.liveRelays()
	if err != nil {
		return nil, err
	}
	had := map[string]bool{}
	for _, rel := range before {
		had[rel.name] = true
	}
	if err := j.sweepDead(r); err != nil {
		return nil, err
	}

	j.step("relays")
	relays, err := j.ensureRelays()
	if err != nil {
		return nil, err
	}
	var fresh []provRelay
	for _, rel := range relays {
		// A relay left pending by an earlier top-up that was never judged is
		// as much in need of admission as one minted now.
		if !had[rel.name] || rel.status == "pending" {
			fresh = append(fresh, rel)
		}
	}
	if len(fresh) == 0 {
		j.log("на машине уже %d релея, добавлять нечего", len(relays))
		return nil, nil
	}
	j.log("новые релеи: %s", strings.Join(relayNames(fresh), ", "))

	j.step("harden")
	if err := j.harden(r, relays); err != nil {
		return nil, err
	}
	arch, err := j.arch(r)
	if err != nil {
		return nil, err
	}
	build, ok := j.p.builds[arch]
	if !ok {
		return nil, fmt.Errorf("в образе хаба нет бинаря релея под %s", arch)
	}
	for _, rel := range fresh {
		j.step("deploy " + rel.name)
		if err := j.deploy(r, build, rel); err != nil {
			return nil, fmt.Errorf("разворот %s: %w", rel.name, err)
		}
	}

	j.step("probe")
	if err := j.probe(fresh); err != nil {
		var refused admissionRefused
		if errors.As(err, &refused) {
			for name, verdict := range refused {
				if verdict != "ip_banned" {
					continue
				}
				if _, derr := svc.MarkDead(j.ctx, name, DeathIPBanned); derr != nil {
					j.log("снять %s не удалось: %v", name, derr)
					continue
				}
				j.log("%s родился забаненным — порт сожжён, следующий подъём возьмёт другой", name)
			}
		}
		return nil, err
	}

	j.step("sign")
	admitted := make([]string, 0, len(fresh))
	for _, rel := range fresh {
		if err := svc.Admit(j.ctx, rel.name); err != nil {
			return admitted, fmt.Errorf("допуск %s: %w", rel.name, err)
		}
		j.log("%s допущен в пул", rel.name)
		admitted = append(admitted, rel.name)
	}
	return admitted, nil
}

// addPorts brings a box back to its number of relays (cause short_of_ports).
func (m *Mechanic) addPorts(ctx context.Context, inc Incident) (string, error) {
	if m.prov == nil {
		return "", fmt.Errorf("%w: у хаба нет провизионера, поднимать порты нечем", ErrRejected)
	}
	added, err := m.prov.TopUp(ctx, inc.Box)
	if err != nil {
		return "", err
	}
	if len(added) == 0 {
		return "портов на машине уже достаточно", nil
	}
	return "подняты и допущены: " + strings.Join(added, ", "), nil
}

// sweepDead stops and removes the units of the box's dead relays and closes
// their ports. Idempotent: a relay already swept has no env, and the script
// does nothing for it.
func (j *jobRun) sweepDead(r Remote) error {
	tx, err := j.p.svc.pool.Begin(j.ctx)
	if err != nil {
		return err
	}
	rows, err := j.p.svc.repo.relaysOnBox(j.ctx, tx, j.box)
	_ = tx.Rollback(j.ctx)
	if err != nil {
		return err
	}
	var args []string
	for _, rel := range rows {
		if rel.status != StatusDead {
			continue
		}
		id, err := openIdentity(rel.identityCT)
		if err != nil {
			// A dead relay whose identity no longer opens cannot say its
			// port; its unit is still swept by name.
			args = append(args, rel.name, "0")
			continue
		}
		args = append(args, rel.name, strconv.Itoa(id.Port))
	}
	if len(args) == 0 {
		return nil
	}
	j.step("sweep")
	out, err := r.Run(j.ctx, "bash -s -- "+j.transport()+" "+strings.Join(args, " "), strings.NewReader(sweepScript))
	if err != nil {
		return fmt.Errorf("снять мёртвые релеи с машины: %w", err)
	}
	if swept := strings.Fields(out); len(swept) > 0 {
		j.log("с машины сняты мёртвые релеи: %s", strings.Join(swept, ", "))
	}
	return nil
}

// sweepScript takes `<transport> (<relay> <port>)...` and, for each relay
// whose env is still on the box, stops its unit, deletes the env and closes
// the port. Prints the names it swept. Names are the hub's own
// (^[a-z0-9-]{1,32}$, entry_relays_name_check), never a box's words.
const sweepScript = `set -u
proto="$1"; shift
while [ $# -ge 2 ]; do
	relay="$1"; port="$2"; shift 2
	[ -f "/etc/nekko-edge/$relay.env" ] || continue
	systemctl disable --now "nekko-edge@$relay" >/dev/null 2>&1 || true
	rm -f "/etc/nekko-edge/$relay.env"
	if [ "$port" != "0" ] && command -v ufw >/dev/null; then
		ufw delete allow "$port/$proto" >/dev/null 2>&1 || true
	fi
	echo "$relay"
done
`
