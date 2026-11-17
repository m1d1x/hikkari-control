package entry

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The spare egress and the receiver's own watch (H4 of hikkari-client
// plans/2026-11-16-hidden-border-receiver-and-egress.md, step 3 of the
// roadmap's queue).
//
// A receiver's link holds two peers: the egress it goes out through, with the
// default routes, and the spare, warm, with only the spare's own address. The
// watch on the receiver (hikkari-egress-watch, the tunnel program's watch
// verb, egress.go) pings through the tunnel to each egress's own address and,
// through the one that carries, to the internet, every 200 ms. The one that
// carries silent through the tunnel for 1.5 s (pinged every 200 ms), or its
// internet silent for 5 s, and the spare heard within 1 s: the spare takes the
// default routes in one wg call. No relay, no unit, no link restarts; the
// people's connections to the internet change their address and break, as a
// change of egress does (H3), and what reopens goes out at once. The watch
// does not move back on its own: an egress that came back is the spare now.
// It says each move with its reason and each minute how both egresses
// answered; the hub files the lines (internal/cascadewatch egress.go).
//
//	cmd/server entry-box egress-spare <receiver-box> <egress-box>   # the spare: stand up, replace
//	cmd/server entry-box egress-spare <receiver-box> off            # the spare off
//
// Moving a receiver by hand to its spare (entry-box egress <receiver>
// <spare>) swaps the two: the spare carries, the egress it left is the spare.

// egressCarrierCmd prints the key of the peer that holds the receiver's
// default routes.
const egressCarrierCmd = `wg show hk-egress allowed-ips 2>/dev/null | awk '/0\.0\.0\.0\/0/ { print $1; exit }'`

func egressCarrier(ctx context.Context, r Remote) (string, error) {
	out, err := r.Run(ctx, egressCarrierCmd, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// egressSpareHeard checks from the receiver that the spare answers through
// the tunnel, the way the watch will ask it.
func egressSpareHeard(ctx context.Context, r Remote, addr netip.Addr) error {
	if _, err := r.Run(ctx, "ping -n -q -c3 -W1 -I hk-egress "+addr.String(), nil); err != nil {
		return fmt.Errorf("запасной выход не отвечает через туннель: %w", err)
	}
	return nil
}

// carrying is the egress the receiver goes out through now and its key.
type carrying struct {
	box  string
	pub  string
	addr netip.Addr
}

func (p *Provisioner) egressOf(ctx context.Context, box string) (*carrying, Remote, error) {
	r, acc, err := p.provokeRemote(ctx, box)
	if err != nil {
		return nil, nil, err
	}
	addr, err := netip.ParseAddr(acc.address)
	if err != nil {
		r.Close()
		return nil, nil, fmt.Errorf("адрес выхода %s: %w", box, err)
	}
	pub, err := egressPrepare(ctx, r, "egress")
	if err != nil {
		r.Close()
		return nil, nil, fmt.Errorf("%s: %w", box, err)
	}
	return &carrying{box: box, pub: pub, addr: addr}, r, nil
}

// StandEgressSpare makes spBox the spare egress of rxBox: the spare takes the
// receiver as a peer beside the ones it carries, the receiver's link gets it
// as a second peer without moving off the egress that carries, and the
// receiver hears the spare through the tunnel. A spare it had before lets it
// go.
func (p *Provisioner) StandEgressSpare(ctx context.Context, rxBox, spBox string) error {
	ends, err := p.openEgress(ctx, rxBox, spBox)
	if err != nil {
		return err
	}
	defer ends.closeRemotes()
	cur, slot, linked, err := p.svc.egressLink(ctx, rxBox)
	if err != nil {
		return err
	}
	if !linked {
		return fmt.Errorf("%w: у %s нет выхода, сначала egress %s <выход>", ErrRejected, rxBox, rxBox)
	}
	if cur == spBox {
		return fmt.Errorf("%w: %s уже выход %s", ErrRejected, spBox, rxBox)
	}
	old, err := p.svc.egressSpareOf(ctx, rxBox)
	if err != nil {
		return err
	}
	if old != nil && old.box != spBox {
		if pub, err := egressCarrier(ctx, ends.rx); err != nil {
			return fmt.Errorf("%s: %w", rxBox, err)
		} else if pub == old.pub {
			return fmt.Errorf("%w: %s сейчас ходит через запасной %s; сделайте его выходом (egress %s %s), потом меняйте запасной", ErrRejected, rxBox, old.box, rxBox, old.box)
		}
	}
	applog.Info(ctx, "entry", "operator egress spare", applog.F("receiver", rxBox), applog.F("spare", spBox), applog.F("egress", cur))
	if ends.ruNets, err = fetchRUNets(ctx, ripeDelegatedURL); err != nil {
		return fmt.Errorf("сети РФ для запрета на выходе: %w", err)
	}
	carrier, curR, err := p.egressOf(ctx, cur)
	if err != nil {
		return err
	}
	defer curR.Close()
	others, err := p.svc.egressPeersOf(ctx, spBox, rxBox)
	if err != nil {
		return err
	}
	spare, err := standEgressSpare(ctx, ends, carrier, slot, others, old)
	if err != nil {
		return err
	}
	if err := p.svc.saveEgressSpare(ctx, rxBox, spBox, spare.pub); err != nil {
		return fmt.Errorf("запасной в базе: %w", err)
	}
	if old != nil && old.box != spBox {
		oldR, _, err := p.provokeRemote(ctx, old.box)
		if err != nil {
			return fmt.Errorf("прежний запасной %s: %w", old.box, err)
		}
		defer oldR.Close()
		stay, err := p.svc.egressPeersOf(ctx, old.box, rxBox)
		if err != nil {
			return err
		}
		if err := egressCarry(ctx, oldR, old.box, stay, ""); err != nil {
			return fmt.Errorf("прежний запасной %s не отпустил приёмник: %w", old.box, err)
		}
		applog.Info(ctx, "entry", fmt.Sprintf("прежний запасной %s отпустил приёмник %s", old.box, rxBox), applog.F("box", old.box))
	}
	return nil
}

// standEgressSpare is StandEgressSpare's work on open boxes (ends.eg is the
// spare): the spare first (it only listens), then the receiver's env with the
// spare, applied keeping the egress that carries. A spare the receiver does
// not hear goes back off both ends, and the receiver keeps old.
func standEgressSpare(ctx context.Context, e *egressEnds, carrier *carrying, slot int, others []egressPeer, old *egressSpare) (*egressSpare, error) {
	rxPub, err := egressPrepare(ctx, e.rx, "receiver")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.rxBox, err)
	}
	spPub, err := egressPrepare(ctx, e.eg, "egress")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.egBox, err)
	}
	peers := append(append([]egressPeer(nil), others...), egressPeer{box: e.rxBox, pub: rxPub, slot: slot, addr: e.rxAddr})
	if err := egressCarry(ctx, e.eg, e.egBox, peers, e.ruNets); err != nil {
		return nil, err
	}
	spare := &egressSpare{box: e.egBox, pub: spPub, addr: e.egAddr}
	env := func(s *egressSpare) map[string]string {
		return map[string]string{egressDir + "/env": receiverEnv(carrier.box, carrier.pub, carrier.addr, slot, s)}
	}
	err = egressInstall(ctx, e.rx, e.rxBox, env(spare), true)
	if err == nil {
		err = egressSpareHeard(ctx, e.rx, e.egAddr)
	}
	if err != nil {
		if backErr := egressInstall(ctx, e.rx, e.rxBox, env(old), true); backErr != nil {
			return nil, fmt.Errorf("%w; прежний запасной не вернулся: %v", err, backErr)
		}
		if leaveErr := egressCarry(ctx, e.eg, e.egBox, others, ""); leaveErr != nil {
			e.egLog("запасной не отпустил приёмник: %v", leaveErr)
		}
		return nil, err
	}
	e.rxLog("запасной выход %s слышен через туннель, выход %s везёт", e.egBox, carrier.box)
	return spare, nil
}

// StopEgressSpare takes the spare off rxBox: off the receiver's link, then
// the receiver off the spare. Refused while the receiver goes out through it.
func (p *Provisioner) StopEgressSpare(ctx context.Context, rxBox string) error {
	spare, err := p.svc.egressSpareOf(ctx, rxBox)
	if err != nil {
		return err
	}
	if spare == nil {
		return fmt.Errorf("%w: у %s нет запасного выхода", ErrRejected, rxBox)
	}
	ends, err := p.openEgress(ctx, rxBox, spare.box)
	if err != nil {
		return err
	}
	defer ends.closeRemotes()
	if pub, err := egressCarrier(ctx, ends.rx); err != nil {
		return fmt.Errorf("%s: %w", rxBox, err)
	} else if pub == spare.pub {
		return fmt.Errorf("%w: %s сейчас ходит через запасной %s; сделайте его выходом (egress %s %s) или верните на выход", ErrRejected, rxBox, spare.box, rxBox, spare.box)
	}
	cur, slot, _, err := p.svc.egressLink(ctx, rxBox)
	if err != nil {
		return err
	}
	applog.Info(ctx, "entry", "operator egress spare off", applog.F("receiver", rxBox), applog.F("spare", spare.box))
	carrier, curR, err := p.egressOf(ctx, cur)
	if err != nil {
		return err
	}
	defer curR.Close()
	if err := egressInstall(ctx, ends.rx, rxBox, map[string]string{egressDir + "/env": receiverEnv(carrier.box, carrier.pub, carrier.addr, slot, nil)}, true); err != nil {
		return err
	}
	if err := p.svc.saveEgressSpare(ctx, rxBox, "", ""); err != nil {
		return err
	}
	stay, err := p.svc.egressPeersOf(ctx, spare.box, rxBox)
	if err != nil {
		return err
	}
	if err := egressCarry(ctx, ends.eg, spare.box, stay, ""); err != nil {
		return fmt.Errorf("%s: %w", spare.box, err)
	}
	ends.egLog("запасной отпустил приёмник %s, везёт ещё %d", rxBox, len(stay))
	return nil
}

// swapEgress moves rxBox by hand to its spare (ends.eg): the spare carries,
// the egress it went through is the spare now. Both are peers of the link
// already: only the env changes, and the hub checks that the internet sees
// the spare, or swaps back.
func (p *Provisioner) swapEgress(ctx context.Context, ends *egressEnds, old string, slot int) error {
	prev, oldR, err := p.egressOf(ctx, old)
	if err != nil {
		return err
	}
	defer oldR.Close()
	sp := ends.spare
	rxPub, err := egressPrepare(ctx, ends.rx, "receiver")
	if err != nil {
		return fmt.Errorf("%s: %w", ends.rxBox, err)
	}
	call := startEgressCall(ctx, ends)
	err = egressInstall(ctx, ends.rx, ends.rxBox, map[string]string{egressDir + "/env": receiverEnv(sp.box, sp.pub, sp.addr, slot, &egressSpare{box: prev.box, pub: prev.pub, addr: prev.addr})}, false)
	if err == nil {
		ends.rxLog("приёмник смотрит в выход %s, запасной теперь %s", sp.box, prev.box)
		err = checkEgressAddress(ctx, ends)
	}
	if call != nil {
		call.finish(ctx, ends)
	}
	if err != nil {
		if backErr := egressInstall(ctx, ends.rx, ends.rxBox, map[string]string{egressDir + "/env": receiverEnv(prev.box, prev.pub, prev.addr, slot, sp)}, false); backErr != nil {
			return fmt.Errorf("%w; назад не вернулся: %v", err, backErr)
		}
		return err
	}
	// One statement: spare and egress may not be one box at any moment.
	if _, err := p.svc.pool.Exec(ctx, `UPDATE entry_egress_links SET egress = $2, spare = $3, spare_pub = $4, rx_pub = $5, updated_at = now()
		WHERE receiver = $1`, ends.rxBox, sp.box, prev.box, prev.pub, rxPub); err != nil {
		return fmt.Errorf("пара в базе: %w", err)
	}
	return checkEgress(ctx, ends)
}

// EgressReceiver is a receiver behind an egress as the hub's watch reads it.
type EgressReceiver struct {
	Box, Egress, Spare string
}

// EgressReceivers are the receivers that go out through an egress.
func (s *Service) EgressReceivers(ctx context.Context) ([]EgressReceiver, error) {
	rows, err := s.pool.Query(ctx, `SELECT receiver, egress, coalesce(spare, '') FROM entry_egress_links ORDER BY receiver`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EgressReceiver
	for rows.Next() {
		var r EgressReceiver
		if err := rows.Scan(&r.Box, &r.Egress, &r.Spare); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
