package entry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// Which egress a receiver goes out through, and changing it (H3 of
// hikkari-client plans/2026-11-16-hidden-border-receiver-and-egress.md).
//
// The hub keeps the pair (entry_egress_links, migration 072). A receiver has a
// slot, its own address in the tunnels (10.231.0.0/16, fd77:231::/64), and
// keeps it whatever egress it goes through; an egress carries any number of
// receivers, a WireGuard peer each. So a second receiver can go out through
// the same egress, and the people's address on the internet stays when the
// entry moves to it.
//
// Moving a receiver to another egress is the same command as standing it up:
//
//	cmd/server entry-box egress <receiver-box> <egress-box>
//
// The new egress takes the receiver as a peer beside the ones it carries, the
// receiver points its link at the new egress in one wg call (no relay, no
// unit and no link restarts), the hub checks that the internet sees the new
// egress and moves back if it does not, and the old egress lets the receiver
// go. Connections the receiver's relays had open to the internet change their
// address and break, as the plan accepts for a change of egress; what reopens
// goes out at once. A probe call through the tunnel during the move times the
// pause.

// egressSlotAddrs are the receiver's tunnel addresses of slot.
func egressSlotAddrs(slot int) (v4, v6 string) {
	return fmt.Sprintf("10.231.%d.%d", slot>>8, slot&0xff), fmt.Sprintf("fd77:231::%x", slot)
}

// egressPeer is one receiver an egress carries.
type egressPeer struct {
	box  string
	pub  string
	slot int
	addr netip.Addr // the receiver's own address, the one its packets come from
}

// egressPeersFile is the egress's peers file (egressUpScript, egress_peers).
func egressPeersFile(peers []egressPeer) string {
	var b strings.Builder
	for _, p := range peers {
		v4, v6 := egressSlotAddrs(p.slot)
		fmt.Fprintf(&b, "%s %s %s %s\n", p.pub, v4, v6, p.addr)
	}
	return b.String()
}

func receiverEnv(egBox, egPub string, egAddr netip.Addr, slot int) string {
	v4, v6 := egressSlotAddrs(slot)
	return fmt.Sprintf("# Written by the nekko hub: this box goes to the internet through %s.\nROLE=receiver\nADDR4=%s\nADDR6=%s\nPEER_PUB=%s\nPEER_ENDPOINT=%s\n",
		egBox, v4, v6, egPub, net.JoinHostPort(egAddr.String(), strconv.Itoa(EgressPort)))
}

func egressEnv(peers []egressPeer) string {
	names := make([]string, 0, len(peers))
	for _, p := range peers {
		names = append(names, p.box)
	}
	return fmt.Sprintf("# Written by the nekko hub: this box is the egress of %s.\nROLE=egress\n", strings.Join(names, ", "))
}

// egressLink is the receiver's row: the egress it goes through, its slot.
func (s *Service) egressLink(ctx context.Context, rx string) (egress string, slot int, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT egress, slot FROM entry_egress_links WHERE receiver = $1`, rx).Scan(&egress, &slot)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, nil
	}
	return egress, slot, err == nil, err
}

// freeEgressSlot is the lowest slot no receiver holds. The first receiver
// gets 1, the address 10.231.0.1 the single tunnel of H1 had.
func (s *Service) freeEgressSlot(ctx context.Context) (int, error) {
	var slot int
	err := s.pool.QueryRow(ctx, `SELECT n FROM generate_series(1, 65534) n
		WHERE n <> 2 AND NOT EXISTS (SELECT 1 FROM entry_egress_links WHERE slot = n) ORDER BY n LIMIT 1`).Scan(&slot)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: адреса в туннелях кончились", ErrRejected)
	}
	return slot, err
}

// egressPeersOf are the receivers egBox carries, but except.
func (s *Service) egressPeersOf(ctx context.Context, egBox, except string) ([]egressPeer, error) {
	rows, err := s.pool.Query(ctx, `SELECT receiver, rx_pub, slot FROM entry_egress_links
		WHERE egress = $1 AND receiver <> $2 ORDER BY slot`, egBox, except)
	if err != nil {
		return nil, err
	}
	peers, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (egressPeer, error) {
		var p egressPeer
		return p, r.Scan(&p.box, &p.pub, &p.slot)
	})
	if err != nil {
		return nil, err
	}
	for i := range peers {
		acc, err := s.boxAccess(ctx, peers[i].box)
		if err != nil {
			return nil, err
		}
		if peers[i].addr, err = netip.ParseAddr(acc.address); err != nil {
			return nil, fmt.Errorf("адрес приёмника %s: %w", peers[i].box, err)
		}
	}
	return peers, nil
}

func (s *Service) saveEgressLink(ctx context.Context, rx, eg string, slot int, rxPub string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO entry_egress_links (receiver, egress, slot, rx_pub) VALUES ($1, $2, $3, $4)
		ON CONFLICT (receiver) DO UPDATE SET egress = $2, slot = $3, rx_pub = $4, updated_at = now()`, rx, eg, slot, rxPub)
	return err
}

func (s *Service) deleteEgressLink(ctx context.Context, rx string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM entry_egress_links WHERE receiver = $1`, rx)
	return err
}

// egressApplyScript installs the program and the unit and brings the link to
// what the files say: a live link is applied in place, anything else comes up
// from nothing.
const egressApplyScript = `set -eu
chmod 755 ` + egressScript + `
chmod 600 ` + egressDir + `/env
systemctl daemon-reload
systemctl enable ` + egressUnit + ` >/dev/null 2>&1
if ip link show hk-egress >/dev/null 2>&1 && systemctl is-active --quiet ` + egressUnit + `; then
	` + egressScript + ` apply
else
	systemctl restart ` + egressUnit + `
fi
systemctl is-active --quiet ` + egressUnit + `
ip -br link show hk-egress
`

// egressInstall writes a box's files and applies them.
func egressInstall(ctx context.Context, r Remote, box string, files map[string]string) error {
	for path, body := range files {
		if _, err := r.Run(ctx, "umask 077; cat > "+path, strings.NewReader(body)); err != nil {
			return fmt.Errorf("%s: %s: %w", box, path, err)
		}
	}
	if _, err := r.Run(ctx, "cat > "+egressScript, strings.NewReader(egressUpScript)); err != nil {
		return fmt.Errorf("%s: программа туннеля: %w", box, err)
	}
	if _, err := r.Run(ctx, "cat > /etc/systemd/system/"+egressUnit+".service", strings.NewReader(egressUnitFile)); err != nil {
		return fmt.Errorf("%s: unit-файл: %w", box, err)
	}
	if _, err := r.Run(ctx, "sh -s", strings.NewReader(egressApplyScript)); err != nil {
		return fmt.Errorf("%s: туннель не встал: %w", box, err)
	}
	return nil
}

// egressSideOff takes the tunnel and every rule of it off one box.
func egressSideOff(ctx context.Context, r Remote) error {
	cmd := "if [ -x " + egressScript + " ]; then " + egressScript + " off; fi; systemctl disable --now " + egressUnit + " >/dev/null 2>&1 || true; rm -f /etc/systemd/system/" + egressUnit + ".service " + egressScript + " " + egressDir + "/peers; systemctl daemon-reload"
	if _, err := r.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("снятие туннеля: %w", err)
	}
	return nil
}

// egressCarry makes egBox carry exactly peers: a peers file and apply, or the
// tunnel off when none are left.
func egressCarry(ctx context.Context, r Remote, egBox string, peers []egressPeer, ruNets string) error {
	if len(peers) == 0 {
		return egressSideOff(ctx, r)
	}
	files := map[string]string{
		egressDir + "/env":   egressEnv(peers),
		egressDir + "/peers": egressPeersFile(peers),
	}
	if ruNets != "" {
		files[egressDir+"/ru.nft"] = ruNets
	}
	return egressInstall(ctx, r, egBox, files)
}

// StandEgress stands up the tunnel from rxBox to egBox, or moves rxBox there
// from the egress it goes through, and then checks it (CheckEgress).
func (p *Provisioner) StandEgress(ctx context.Context, rxBox, egBox string) error {
	ends, err := p.openEgress(ctx, rxBox, egBox)
	if err != nil {
		return err
	}
	defer ends.closeRemotes()
	old, slot, linked, err := p.svc.egressLink(ctx, rxBox)
	if err != nil {
		return err
	}
	if !linked {
		if slot, err = p.svc.freeEgressSlot(ctx); err != nil {
			return err
		}
	}
	applog.Info(ctx, "entry", "operator egress", applog.F("receiver", rxBox), applog.F("egress", egBox), applog.F("from", old))
	if ends.ruNets, err = fetchRUNets(ctx, ripeDelegatedURL); err != nil {
		return fmt.Errorf("сети РФ для запрета на выходе: %w", err)
	}
	others, err := p.svc.egressPeersOf(ctx, egBox, rxBox)
	if err != nil {
		return err
	}
	moving := linked && old != egBox
	var leave func(context.Context) error
	if moving {
		oldR, l, err := p.leaveEgress(ctx, rxBox, old, ends)
		if err != nil {
			return err
		}
		defer oldR.Close()
		leave = l
	}
	rxPub, err := standEgress(ctx, ends, slot, others, moving)
	if err != nil {
		return err
	}
	if err := p.svc.saveEgressLink(ctx, rxBox, egBox, slot, rxPub); err != nil {
		return fmt.Errorf("пара в базе: %w", err)
	}
	if moving {
		if err := leave(ctx); err != nil {
			return fmt.Errorf("приёмник ходит через %s, но старый выход %s его не отпустил: %w", egBox, old, err)
		}
	}
	return checkEgress(ctx, ends)
}

// leaveEgress readies the move from old: back, if the new egress does not
// carry, and leave, once it does.
func (p *Provisioner) leaveEgress(ctx context.Context, rxBox, old string, ends *egressEnds) (Remote, func(context.Context) error, error) {
	oldR, oldAcc, err := p.provokeRemote(ctx, old)
	if err != nil {
		return nil, nil, fmt.Errorf("старый выход %s: %w", old, err)
	}
	fail := func(err error) (Remote, func(context.Context) error, error) {
		oldR.Close()
		return nil, nil, fmt.Errorf("старый выход %s: %w", old, err)
	}
	oldAddr, err := netip.ParseAddr(oldAcc.address)
	if err != nil {
		return fail(err)
	}
	oldPub, err := egressPrepare(ctx, oldR, "egress")
	if err != nil {
		return fail(err)
	}
	stay, err := p.svc.egressPeersOf(ctx, old, rxBox)
	if err != nil {
		return fail(err)
	}
	ends.back = func(ctx context.Context, slot int) error {
		return egressInstall(ctx, ends.rx, ends.rxBox, map[string]string{egressDir + "/env": receiverEnv(old, oldPub, oldAddr, slot)})
	}
	return oldR, func(ctx context.Context) error {
		if err := egressCarry(ctx, oldR, old, stay, ""); err != nil {
			return err
		}
		applog.Info(ctx, "entry", fmt.Sprintf("старый выход %s отпустил приёмник %s", old, rxBox), applog.F("box", old))
		return nil
	}, nil
}

// standEgress is StandEgress's work on open boxes: keys on both ends, the
// egress first (it only listens), then the receiver. On a move it times a
// probe call through the tunnel and, if the internet does not see the new
// egress, points the receiver back (ends.back) and lets it go from the new
// egress. It returns the receiver's public key.
func standEgress(ctx context.Context, e *egressEnds, slot int, others []egressPeer, moving bool) (string, error) {
	rxPub, err := egressPrepare(ctx, e.rx, "receiver")
	if err != nil {
		return "", fmt.Errorf("%s: %w", e.rxBox, err)
	}
	egPub, err := egressPrepare(ctx, e.eg, "egress")
	if err != nil {
		return "", fmt.Errorf("%s: %w", e.egBox, err)
	}
	peers := append(append([]egressPeer(nil), others...), egressPeer{box: e.rxBox, pub: rxPub, slot: slot, addr: e.rxAddr})
	if err := egressCarry(ctx, e.eg, e.egBox, peers, e.ruNets); err != nil {
		return "", err
	}
	e.egLog("выход везёт приёмники: %d", len(peers))
	var call *egressCall
	if moving {
		call = startEgressCall(ctx, e)
	}
	err = egressInstall(ctx, e.rx, e.rxBox, map[string]string{egressDir + "/env": receiverEnv(e.egBox, egPub, e.egAddr, slot)})
	if err == nil {
		e.rxLog("приёмник смотрит в выход %s", e.egBox)
		err = checkEgressAddress(ctx, e)
	}
	if call != nil {
		call.finish(ctx, e)
	}
	if err != nil && moving {
		if backErr := e.back(ctx, slot); backErr != nil {
			return "", fmt.Errorf("%w; назад на старый выход не вернулся: %v", err, backErr)
		}
		e.rxLog("новый выход %s не повёз, приёмник вернулся на старый", e.egBox)
		if leaveErr := egressCarry(ctx, e.eg, e.egBox, others, ""); leaveErr != nil {
			e.egLog("новый выход не отпустил приёмник: %v", leaveErr)
		}
	}
	return rxPub, err
}

// StopEgress takes the receiver off egBox: the tunnel and every rule of it
// off the receiver, and the receiver's peer off the egress, which keeps the
// others it carries or goes off with the last.
func (p *Provisioner) StopEgress(ctx context.Context, rxBox, egBox string) error {
	ends, err := p.openEgress(ctx, rxBox, egBox)
	if err != nil {
		return err
	}
	defer ends.closeRemotes()
	if cur, _, linked, err := p.svc.egressLink(ctx, rxBox); err != nil {
		return err
	} else if linked && cur != egBox {
		return fmt.Errorf("%w: %s ходит через %s, а не через %s", ErrRejected, rxBox, cur, egBox)
	}
	applog.Info(ctx, "entry", "operator egress off", applog.F("receiver", rxBox), applog.F("egress", egBox))
	// Without the tunnel's rules a relay in it would go out from the
	// receiver's own address.
	if attached, err := egressRelaysAttached(ctx, ends.rx); err != nil {
		return err
	} else if attached {
		return fmt.Errorf("%w: релеи %s ходят через выход, сначала egress-relays %s %s off", ErrRejected, rxBox, rxBox, egBox)
	}
	if err := egressSideOff(ctx, ends.rx); err != nil {
		return fmt.Errorf("%s: %w", rxBox, err)
	}
	ends.rxLog("туннель выхода снят")
	stay, err := p.svc.egressPeersOf(ctx, egBox, rxBox)
	if err != nil {
		return err
	}
	if err := egressCarry(ctx, ends.eg, egBox, stay, ""); err != nil {
		return fmt.Errorf("%s: %w", egBox, err)
	}
	ends.egLog("выход отпустил приёмник %s, везёт ещё %d", rxBox, len(stay))
	return p.svc.deleteEgressLink(ctx, rxBox)
}
