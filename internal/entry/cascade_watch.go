package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// What the cascade watch (internal/cascadewatch) needs of the fleet: the
// trunk entries the hub stood, an SSH session to a box, and its own
// measuring binary staged there (cmd/cascade-probe).

// CascadeProbeDir holds nekko-cascade-probe on a box the watch measures from.
const (
	CascadeProbeDir    = "/usr/local/lib/nekko-cascade-probe"
	CascadeProbeBinary = CascadeProbeDir + "/nekko-cascade-probe"
)

// TrunkPair is a trunk entry the hub stood and the exit it carries to.
type TrunkPair struct {
	Entry, Exit string
	// EntryAddress and ExitAddress are the boxes' public addresses.
	EntryAddress, ExitAddress string
	// RelayPort is the exit's Hikkari relay port (the trunk's members dial
	// it); Forward maps the entry's listen ports to the exit's loopback ports.
	RelayPort int
	Forward   map[int]int
	Members   int
}

// ProvokeListen is the entry's listen port that reaches the exit's provoke
// receiver (forwarded to provokePort), 0 when none does.
func (p TrunkPair) ProvokeListen(provokePort int) int {
	best := 0
	for l, to := range p.Forward {
		if to == provokePort && (best == 0 || l < best) {
			best = l
		}
	}
	return best
}

// recordTrunkEntry keeps what StandTrunkEntry stood.
func (s *Service) recordTrunkEntry(ctx context.Context, box string, o TrunkEntryOptions) error {
	fwd := map[string]int{}
	for l, to := range o.Forward {
		fwd[strconv.Itoa(l)] = to
	}
	raw, err := json.Marshal(fwd)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO trunk_entries (entry_box, exit_box, forward, members) VALUES ($1, $2, $3, $4)
		ON CONFLICT (entry_box) DO UPDATE SET exit_box = $2, forward = $3, members = $4, stood_at = now()`,
		box, o.Exit, raw, o.Members)
	return err
}

func (s *Service) forgetTrunkEntry(ctx context.Context, box string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM trunk_entries WHERE entry_box = $1`, box)
	return err
}

// TrunkPairs lists the trunk entries the hub stood, each with its exit.
func (s *Service) TrunkPairs(ctx context.Context) ([]TrunkPair, error) {
	rows, err := s.pool.Query(ctx, `SELECT entry_box, exit_box, forward, members FROM trunk_entries ORDER BY entry_box`)
	if err != nil {
		return nil, err
	}
	type row struct {
		entry, exit string
		fwd         []byte
		members     int
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.entry, &r.exit, &r.fwd, &r.members); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]TrunkPair, 0, len(list))
	for _, r := range list {
		p := TrunkPair{Entry: r.entry, Exit: r.exit, Members: r.members, Forward: map[int]int{}}
		var fwd map[string]int
		if err := json.Unmarshal(r.fwd, &fwd); err != nil {
			return nil, fmt.Errorf("trunk entry %s: forward: %w", r.entry, err)
		}
		for l, to := range fwd {
			n, err := strconv.Atoi(l)
			if err != nil {
				return nil, fmt.Errorf("trunk entry %s: forward port %q", r.entry, l)
			}
			p.Forward[n] = to
		}
		entry, err := s.boxAccess(ctx, r.entry)
		if err != nil {
			return nil, err
		}
		p.EntryAddress = entry.address
		addr, _, _, _, err := s.trunkExitOf(ctx, r.exit)
		if err != nil {
			// The exit lost its trunk: the pair is still watched, by the
			// exit box's own address.
			exit, aerr := s.boxAccess(ctx, r.exit)
			if aerr != nil {
				return nil, aerr
			}
			p.ExitAddress = exit.address
		} else {
			host, port, _ := net.SplitHostPort(addr)
			p.ExitAddress = host
			p.RelayPort, _ = strconv.Atoi(port)
		}
		out = append(out, p)
	}
	return out, nil
}

// BoxRecords are the signed records of every relay on box that has one and
// still stands, in the pool or out of it (testing under a canary, draining,
// suspect): the cascade watch measures the exit whatever the pool makes of
// it, since a relay out of the pool is when the question of blame comes up.
func (s *Service) BoxRecords(ctx context.Context, box string) ([][]byte, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, envelope_ct FROM entry_relays
		WHERE box = $1 AND kind = 'personal' AND status IN ('alive', 'testing', 'draining', 'suspect') AND expires_at > $2
		ORDER BY name`, box, s.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var name string
		var ct []byte
		if err := rows.Scan(&name, &ct); err != nil {
			return nil, err
		}
		raw, err := cryptobox.Decrypt(ct)
		if err != nil {
			return nil, fmt.Errorf("entry: open relay %s: %w", name, err)
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// VantageBox is the box under an observation point's slot, "" when the slot
// has none.
func (s *Service) VantageBox(ctx context.Context, slot string) (string, error) {
	var box *string
	err := s.pool.QueryRow(ctx, `SELECT box FROM entry_vantages WHERE slot = $1 AND status = 'active'`, slot).Scan(&box)
	if errors.Is(err, pgx.ErrNoRows) || box == nil {
		return "", nil
	}
	return *box, err
}

// WatchRemote opens an SSH session to box with the hub's key, for the
// cascade watch's measurements. The caller closes it.
func (p *Provisioner) WatchRemote(ctx context.Context, box string) (Remote, error) {
	r, _, err := p.provokeRemote(ctx, box)
	return r, err
}

// StageCascadeProbe puts the hub's nekko-cascade-probe on the box behind r,
// unless the box has this build already, and returns its path.
func (p *Provisioner) StageCascadeProbe(ctx context.Context, r Remote) (string, error) {
	if len(p.probeBuilds) == 0 {
		return "", fmt.Errorf("%w: в образе хаба нет nekko-cascade-probe", ErrRejected)
	}
	if _, err := r.Run(ctx, "mkdir -p "+CascadeProbeDir, nil); err != nil {
		return "", err
	}
	quiet := func(string, ...any) {}
	if err := stageLabBinary(ctx, r, p.probeBuilds, CascadeProbeBinary, quiet); err != nil {
		return "", err
	}
	out, err := r.Run(ctx, "f="+CascadeProbeBinary+"; if [ -f $f.new ]; then chmod 755 $f.new && mv $f.new $f; fi; test -x $f && echo ok", nil)
	if err != nil || strings.TrimSpace(out) != "ok" {
		return "", fmt.Errorf("nekko-cascade-probe не встал: %v %s", err, remoteWord(out))
	}
	return CascadeProbeBinary, nil
}
