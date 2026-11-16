package entry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The entry's group of receivers (hikkari-client
// plans/2026-11-16-second-receiver.md, P5). An entry over hk-pipe holds the
// trunk to its exit and to the group's other receivers (spares made by
// entry-box receiver-spare: the exit's Hikkari relay again on another box),
// moves off a dead one by itself and, by the owner's hand, to the one he
// names:
//
//	cmd/server entry-box trunk-entry <box> <exit-box> <listen=...> ... pipe receivers=<box>[,<box>] [receiver-pool=N]
//	cmd/server entry-box trunk-entry-receiver <entry-box> <box>
//
// The move by hand is a file beside the entry's wires file the brain reads
// every second (trunkentry receivers_linux.go); the brain moves only to a
// receiver it hears, and says so in its journal, which the command waits
// for.

// receiverAddresses are the addresses of the receivers' relays, checked to be
// the exit's relay again: the same trunk key, port and name. A receiver that
// differs would take the entry's members and refuse them.
func (s *Service) receiverAddresses(ctx context.Context, exit string, boxes []string, exitAddr, sni, key string) ([]string, error) {
	_, port, err := net.SplitHostPort(exitAddr)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{exit: true}
	var out []string
	for _, b := range boxes {
		if seen[b] {
			return nil, fmt.Errorf("%w: приёмник %s дважды (%s уже выход входа)", ErrRejected, b, exit)
		}
		seen[b] = true
		addr, bsni, bkey, _, err := s.trunkPipeExitOf(ctx, b)
		if err != nil {
			return nil, fmt.Errorf("приёмник %s: %w", b, err)
		}
		host, bport, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if bkey != key || bport != port || !strings.EqualFold(bsni, sni) {
			return nil, fmt.Errorf("%w: на %s не тот же релей, что на %s (ключ трубы, порт или домен), сначала entry-box receiver-spare %s %s", ErrRejected, b, exit, b, exit)
		}
		out = append(out, host)
	}
	return out, nil
}

// trunkEntryReceivers is the entry's exit and its other receivers as stood.
func (s *Service) trunkEntryReceivers(ctx context.Context, entryBox string) (exit string, receivers []string, err error) {
	err = s.pool.QueryRow(ctx, `SELECT exit_box, receivers FROM trunk_entries WHERE entry_box = $1`, entryBox).Scan(&exit, &receivers)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, fmt.Errorf("%w: на %s хаб не ставил входа", ErrRejected, entryBox)
	}
	return exit, receivers, err
}

// trunkEntryReceiverFile is the brain's file of the receiver the hub names
// (trunkentry receiverFilePath: beside the wires file).
const trunkEntryReceiverFile = trunkEntryEnvFile + ".receiver"

// receiverMoveWait is how long the command waits for the brain's word: the
// brain reads the file every second.
const receiverMoveWait = 15 * time.Second

// MoveTrunkEntry moves the entry on entryBox to the receiver on box by hand
// and returns the brain's line about it: the move, or why it refused.
func (p *Provisioner) MoveTrunkEntry(ctx context.Context, entryBox, box string) (string, error) {
	exit, receivers, err := p.svc.trunkEntryReceivers(ctx, entryBox)
	if err != nil {
		return "", err
	}
	if box != exit && !slices.Contains(receivers, box) {
		return "", fmt.Errorf("%w: %s не приёмник входа %s (%s)", ErrRejected, box, entryBox, strings.Join(append([]string{exit}, receivers...), ","))
	}
	addr, _, _, _, err := p.svc.trunkPipeExitOf(ctx, box)
	if err != nil {
		return "", err
	}
	ip, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	r, _, err := p.provokeRemote(ctx, entryBox)
	if err != nil {
		return "", err
	}
	defer r.Close()
	since := time.Now().Add(-time.Second).Unix()
	if _, err := r.Run(ctx, "umask 022; printf '%s\\n' "+ip+" > "+trunkEntryReceiverFile+".new && mv "+trunkEntryReceiverFile+".new "+trunkEntryReceiverFile, nil); err != nil {
		return "", fmt.Errorf("файл приёмника: %w", err)
	}
	applog.Info(ctx, "entry", "operator trunk entry receiver", applog.F("box", entryBox), applog.F("receiver", box))
	word := regexp.MustCompile(`trunk: (receiver moved from \S+ to ` + regexp.QuoteMeta(ip) + `: by hand|receiver ` + regexp.QuoteMeta(ip) + ` by hand refused: .*)$`)
	deadline := time.Now().Add(receiverMoveWait)
	for time.Now().Before(deadline) {
		out, err := r.Run(ctx, fmt.Sprintf("journalctl -u %s --since @%d --no-pager -o short-iso-precise", trunkEntryUnit, since), nil)
		if err != nil {
			return "", fmt.Errorf("журнал входа: %w", err)
		}
		for _, l := range strings.Split(out, "\n") {
			if m := word.FindStringSubmatch(l); m != nil {
				line := strings.ReplaceAll(l, ip, box)
				if strings.Contains(m[1], "refused") {
					return line, fmt.Errorf("%w: вход не перешёл", ErrRejected)
				}
				return line, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return "", fmt.Errorf("вход не сказал о переходе за %s: на нём мозг без P5 или не над трубой?", receiverMoveWait)
}
