package entry

// playbooks.go is the dispatcher (roadmap 42 П9.5): which repair fits a
// cause, and whether the hub may run it without asking.
//
// # The owner's rule, 28.10 (it replaced the one of 22.10)
//
// Whatever fits under the money caps the hub does by itself (spend.go,
// П9.19): the repairs that spend start on `auto` like the free ones, and the
// caps are what stands between them and the owner's card. `ask` is left for
// the causes the hub has no repair for, and `off` is the owner's brake. This
// file is where that rule is written down once, instead of being spread
// across the call sites that act on it.
//
// # Why a mode per cause, not a global switch
//
// "Autopilot on/off" is the wrong knob. Replacing a burnt domain is cheap,
// reversible and boring; retiring a machine costs money and cannot be undone.
// They do not belong behind one switch, and an owner who wants the first
// unattended should not have to accept the second.
//
// # The dispatcher proposes, it never decides on its own
//
// Even on `auto` what runs is the playbook this table names. A cause with no
// playbook is surfaced to a person and nothing else — an honest "I do not
// know what to do about this" beats running the nearest-looking repair.

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

// Playbooks: the repairs the hub knows how to ask for. The ones that exist as
// code today are marked; the rest are П9.6, and until then they are what the
// console tells a person to go and do.
const (
	// PlaybookEvictDomain: the blocked name off the box at once — burned,
	// suspect, with its second chance — and the box re-provisioned with a
	// free name from the pool. Free: it never buys (П9.11б, owner's decision
	// 27.10). It replaced replace_domain, which bought a name when the pool
	// was dry, so one `ask` held both the eviction people needed at once and
	// a purchase that can wait — the same split as replace_box → evict_box.
	PlaybookEvictDomain = "evict_domain"
	// PlaybookBuyDomain: one more name in the domain pool, which holds fewer
	// free ones than the owner's target (domainpool.go). The one way the hub
	// buys a domain by itself.
	PlaybookBuyDomain = "buy_domain"
	// PlaybookRotateReality: new Reality key and short id, same machine.
	PlaybookRotateReality = "rotate_reality"
	// PlaybookEvictBox: the machine out of the pool at once, on suspicion —
	// its relays `suspect`, their holders refilled from the rest of the pool,
	// the machine kept rented for its second chance (П9.11,
	// secondchance.go). Free: buying the replacement is the fleet's
	// (scale_up), under its own mode. It replaced replace_box, which bought
	// the new machine and retired the old one as one repair — so one `ask`
	// held both the eviction people needed at once and a purchase that can
	// wait, and the old machine was written off on the eviction threshold.
	PlaybookEvictBox = "evict_box"
	// PlaybookRetirePorts: take the banned relays out of the pool and leave
	// the box its working ones.
	PlaybookRetirePorts = "retire_ports"
	// PlaybookRebootBox: restart the machine through the hoster's API and
	// wait for it to come back. Free, so it runs unattended. Its cause is
	// `provider_down`, which the escalated grid of П9.4 made nameable; the
	// repair itself exists from П9.2 and is what the console's reboot button
	// runs.
	PlaybookRebootBox = "reboot_box"
	// PlaybookRestartEdge: the relay units the grid found stopped, restarted
	// on a box that is otherwise fine. The cheapest repair there is — it
	// changes no address, no key and no name — and the one the grid of П9.4
	// exists to make possible. Exists (П9.6б).
	PlaybookRestartEdge = "restart_edge"
	// PlaybookRotateDest: the relays moved off a dead cover site onto the
	// ones still answering. Exists (П9.6б).
	PlaybookRotateDest = "rotate_dest"
	// PlaybookAddPorts: fresh relays on fresh ports until the box holds as
	// many as its protocol keeps (ports.go). Deploys only the new ones.
	PlaybookAddPorts = "add_ports"
	// PlaybookAddCovers: the proven cover sites a relay dropped while they
	// were dead, written back once they answer again (covers.go).
	PlaybookAddCovers = "add_covers"
	// PlaybookScaleUp: the machines a pool is short of, against the target
	// its load asks for, now or by the time a new one could be ready
	// (fleet.go) — which is also how an evicted machine is replaced: idle
	// machines first, orders for the rest.
	PlaybookScaleUp = "scale_up"
)

// Modes a playbook can be in.
const (
	// ModeAuto: the hub runs it without asking.
	ModeAuto = "auto"
	// ModeAsk: the hub proposes it and waits for a person.
	ModeAsk = "ask"
	// ModeOff: the hub does not even propose it.
	ModeOff = "off"
)

// Playbook is what the dispatcher proposes for a cause.
type Playbook struct {
	// Name is empty when the hub knows no repair for this cause. The console
	// then shows the diagnosis and asks a person to look, which is the honest
	// answer and not a failure.
	Name string `json:"name,omitempty"`
	// Mode is how far the hub may go on its own.
	Mode string `json:"mode"`
	// Spends says this repair costs money: it passes its spend group's caps
	// (spend.go), and the console says so beside it. Since П9.19 it no longer
	// starts at `ask` — whatever fits under the caps the hub does by itself
	// (owner's decision 28.10).
	Spends bool `json:"spends"`
	// Why is the one sentence explaining the choice, for the console and for
	// the incident record.
	Why string `json:"why"`
}

// playbookFor is the whole table, in one place. Values, not behaviour: the
// running of a playbook is П9.6's, and keeping the choice separate from the
// doing is what lets the choice be tested without any machine existing.
var playbookFor = map[string]Playbook{
	// The domain is split like the machine (П9.11б): taking the blocked name
	// off is free and immediate, buying a new one is the pool's target's.
	CauseDomainBlocked: {
		Name:   PlaybookEvictDomain,
		Mode:   ModeAuto,
		Spends: false,
		Why:    "имя заблокировано, адрес жив: снять имя сразу и дать ему второй шанс, взять свободное из пула; новые имена докупает цель пула доменов",
	},
	CauseFingerprint: {
		Name:   PlaybookRotateReality,
		Mode:   ModeAuto,
		Spends: false,
		Why:    "протокол спалили, адрес жив: смена ключа ничего не стоит и машину не трогает",
	},
	CausePortsBanned: {
		Name:   PlaybookRetirePorts,
		Mode:   ModeAuto,
		Spends: false,
		Why:    "часть портов мертва, адрес жив: снять их с выдачи, машину оставить",
	},
	// Eviction is free and immediate: people on a banned address are already
	// cut off, and the pool covers for it. Whether the address is gone for
	// good is the second chance's to answer, and the replacement machine is
	// the fleet's to buy, under scale_up's mode.
	CauseIPBanned: {
		Name:   PlaybookEvictBox,
		Mode:   ModeAuto,
		Spends: false,
		Why:    "адрес мёртв целиком: снять машину с выдачи сразу и дать ей второй шанс; замену докупает парк",
	},
	// The three the escalated grid of П9.4 made nameable. All free, all
	// reversible, none of them touches an address — which is the whole point
	// of the grid: before it, every one of these failures could only be read
	// as a banned address, whose repair is the expensive one above.
	CauseEdgeDown: {
		Name:   PlaybookRestartEdge,
		Mode:   ModeAuto,
		Spends: false,
		Why:    "процесс мёртв, машина и адрес живы: поднять юнит — дешевле ремонта нет",
	},
	CauseProviderDown: {
		Name:   PlaybookRebootBox,
		Mode:   ModeAuto,
		Spends: false,
		Why:    "машина не отвечает ни хабу, ни по ssh: перезагрузка у хостера ничего не стоит и адрес сохраняет",
	},
	CauseDestination: {
		Name:   PlaybookRotateDest,
		Mode:   ModeAuto,
		Spends: false,
		Why:    "прикрытие мертво, машина и адрес живы: увести релеи на живые, машину не трогать",
	},
	// Not a failure anybody sees: the box carries people on what it has. It
	// is the box having lost the spare that makes its next port ban free, and
	// putting the spare back is free too.
	CauseShortOfPorts: {
		Name:   PlaybookAddPorts,
		Mode:   ModeAuto,
		Spends: false,
		Why:    "машина работает, но без запасного порта: поднять новые порты на ней же, работающие не трогать",
	},
	// The same shape for covers: rotate_dest narrowed the list while a cover
	// was dead, and once it answers again the relay goes back to the full set.
	CauseShortOfCovers: {
		Name:   PlaybookAddCovers,
		Mode:   ModeAuto,
		Spends: false,
		Why:    "машина работает, но релеи прячутся за неполным набором прикрытий: вернуть ожившие проверенные",
	},
	// The fleet's own shortage: nothing broke, the pool is (or soon will be)
	// below what its load asks for. It buys machines, under the hosting caps.
	CauseShortOfFleet: {
		Name:   PlaybookScaleUp,
		Mode:   ModeAuto,
		Spends: true,
		Why:    "живых машин в пуле меньше, чем просит нагрузка (или станет меньше, пока новая проходит допуск): докупить сколько не хватает под потолком хостинга",
	},
	// The hosting caps have no room for the machine a pool needs (П9.18). No
	// repair: raising a cap is the owner's, and the incident closes by
	// itself when room comes back (fleetbudget.go).
	CauseShortOfBudget: {
		Mode: ModeAsk,
		Why:  "пулу нужна машина, а потолок денег на хостинг не пускает: поднять потолок или ждать нового дня или месяца — хаб сам не покупает",
	},
	// The domain pool's own shortage: as for machines, a purchase, under the
	// registrar caps.
	CauseShortOfDomains: {
		Name:   PlaybookBuyDomain,
		Mode:   ModeAuto,
		Spends: true,
		Why:    "свободных доменов меньше, чем уйдёт до следующей покупки: купить ещё одно имя под потолком доменов",
	},
}

// PlaybookForCause answers what to do about a cause, and how far the hub may
// go by itself.
//
// A cause nobody wrote a playbook for comes back empty in `ask`: the console
// shows it to a person and the hub does nothing. That is the right default —
// a dispatcher that reached for the nearest-looking repair would be worse
// than one that admits it does not know.
func PlaybookForCause(cause string) Playbook {
	if p, ok := playbookFor[cause]; ok {
		return p
	}
	return Playbook{Mode: ModeAsk, Why: "хаб не знает ремонта для этой причины — смотреть руками"}
}

// MayRunUnattended says the hub can carry this out on its own.
//
// Written as its own function rather than as `p.Mode == ModeAuto` at each
// call site: there is exactly one place that decides whether the hub spends
// money without a person, and this is it.
func (p Playbook) MayRunUnattended() bool {
	return p.Name != "" && p.Mode == ModeAuto
}

// NeedsAPerson says this incident is one the console must put in front of
// somebody — either because the hub has no repair for it, or because the
// repair is one it may not start alone.
func (p Playbook) NeedsAPerson() bool {
	return p.Mode == ModeAsk || (p.Mode == ModeOff && p.Name != "")
}

// PlaybookOption is one repair the hub can perform, as the console's "another
// action" menu lists it (roadmap 42 К2).
//
// Name is always set here — unlike [Playbook], which carries an empty name for
// a cause the hub knows no repair for. The cause it was designed for rides
// along so the menu can say what each one is normally for: choosing
// `retire_ports` against a dead cover site is allowed, and a person should be
// able to see that they are doing something unusual.
type PlaybookOption struct {
	Playbook
	// Cause is what this repair is the answer to in the dispatcher's table.
	Cause string `json:"cause"`
}

// Playbooks lists every repair the hub can be asked to perform, by name.
//
// The console needs it for the one thing `run` could not do before К2: carry
// out a repair other than the proposed one. A free-text command was rejected
// (К2 §4) — the hub performs the repairs it has code for, and an operator
// choosing from them keeps the attempt in entry_repairs and in the П9.8
// dataset, which an ssh session by hand does not.
func Playbooks() []PlaybookOption {
	out := make([]PlaybookOption, 0, len(playbookFor))
	for cause, p := range playbookFor {
		if p.Name == "" {
			continue
		}
		out = append(out, PlaybookOption{Playbook: p, Cause: cause})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// KnownPlaybook says the hub has code for this repair, and gives back how the
// dispatcher reads it.
func KnownPlaybook(name string) (PlaybookOption, bool) {
	if name == "" {
		return PlaybookOption{}, false
	}
	for _, o := range Playbooks() {
		if o.Name == name {
			return o, true
		}
	}
	return PlaybookOption{}, false
}

// PlaybookModes is the owner's per-cause override of a repair's mode
// (migration 069, П9.8): cause → auto / ask / off. A cause with no entry keeps
// the default written in playbookFor.
type PlaybookModes map[string]string

// For is the playbook for a cause with the owner's mode applied. The repair
// itself is never overridden — only how far the hub may go with it.
func (m PlaybookModes) For(cause string) Playbook {
	p := PlaybookForCause(cause)
	if mode, ok := m[cause]; ok && p.Name != "" {
		p.Mode = mode
	}
	return p
}

// modesQuerier is what LoadPlaybookModes needs of a pool: the hub's and the
// admin plane's both fit.
type modesQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// LoadPlaybookModes reads the overrides. Read on every use rather than
// cached: the mechanic consults it once per standing incident per minute, and
// a mode the owner just turned off must stop the very next pass.
func LoadPlaybookModes(ctx context.Context, q modesQuerier) (PlaybookModes, error) {
	rows, err := q.Query(ctx, `SELECT cause, mode FROM entry_playbook_modes`)
	if err != nil {
		return nil, fmt.Errorf("entry: playbook modes: %w", err)
	}
	defer rows.Close()
	out := PlaybookModes{}
	for rows.Next() {
		var cause, mode string
		if err := rows.Scan(&cause, &mode); err != nil {
			return nil, err
		}
		out[cause] = mode
	}
	return out, rows.Err()
}

// Playbook is the dispatcher's answer for a cause, the owner's mode applied.
func (s *Service) Playbook(ctx context.Context, cause string) (Playbook, error) {
	modes, err := LoadPlaybookModes(ctx, s.pool)
	if err != nil {
		return Playbook{}, err
	}
	return modes.For(cause), nil
}

// SetPlaybookMode records the owner's mode for a cause. Refused for a cause
// the hub has no repair for — a mode with nothing behind it would read as a
// decision that changes nothing — and for a word that is not a mode.
func (s *Service) SetPlaybookMode(ctx context.Context, cause, mode, by string) error {
	if mode != ModeAuto && mode != ModeAsk && mode != ModeOff {
		return fmt.Errorf("%w: режим %q — бывает auto, ask или off", ErrRejected, mode)
	}
	if PlaybookForCause(cause).Name == "" {
		return fmt.Errorf("%w: для причины %q у хаба нет ремонта", ErrRejected, cause)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO entry_playbook_modes (cause, mode, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (cause) DO UPDATE SET mode = $2, updated_by = $3, updated_at = now()
	`, cause, mode, by)
	return err
}
