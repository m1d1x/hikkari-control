package entry

// revive.go holds the two repairs П9.6б adds (roadmap 42): bringing a box's
// own tunnel back without changing its address.
//
// # Why these two sit together
//
// `restart_edge` and `rotate_dest` are the repairs the escalated grid of П9.4
// earned. Before it, a relay unreachable from Russia could only be read as a
// banned address, and the only repair for that is a new machine — the one
// expensive, irreversible thing the hub can do. The grid tells a stopped
// process and a dead cover site apart from a banned address, and both of those
// are fixed on the machine the hub already has: free, reversible, and touching
// nothing that works.
//
// So the pair belongs in one file not because they share code, but because
// they share that claim: the box is innocent, and what broke is on it or in
// front of it.
//
// # Both are jobs
//
// Each runs through execJob, like `rekey` and `reboot`: one at a time per box,
// steps and a live log in the console, and a row somebody can read afterwards
// when they ask what the hub did to a machine. A repair that ssh'd into a box
// without leaving that trail would be the one kind of autopilot action nobody
// could audit.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// RestartEdge restarts the named relay units on a box that is otherwise fine
// (П9.6б, playbook restart_edge), and re-probes admission afterwards.
//
// Returns the relays it actually restarted — which is the names asked for
// narrowed to the box's own live ones, not a count: the console's outcome
// names them, and a number would let it read as if some other relay had been
// repaired.
//
// # Why a list and not "the whole box"
//
// Restarting a relay cuts the connections riding it. A box whose second relay
// is serving people perfectly well has no business losing them because the
// first one died, so the caller passes exactly the units the grid found
// stopped. An empty list is refused rather than widened: "restart everything"
// is a different repair with a different cost, and the hub must not reach for
// it by accident.
func (p *Provisioner) RestartEdge(ctx context.Context, box string, units []string) ([]string, error) {
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return nil, err
	}
	if acc.retired {
		return nil, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("%w: не сказано, какие юниты поднимать", ErrRejected)
	}
	var restarted []string
	err = p.execJob(ctx, JobRestart, box, uuid.Nil, func(j *jobRun) error {
		names, err := j.restartEdge(units)
		restarted = names
		return err
	})
	return restarted, err
}

// restartEdge is the job body: log in, restart the units, read back what
// systemd says, and only then let the vantage decide whether the relay is
// really back.
//
// # The box's own word is not the proof
//
// `systemctl is-active` says the process is running, which is not the same as
// a client in Russia getting through — a relay can come up and still be
// unreachable. So the repair ends with the admission probe, exactly as a
// deploy does: the vantage is what settles it, and a repair that reported
// success on the strength of a local exit code would be the "repair that
// passed without doing anything" the plan forbids.
func (j *jobRun) restartEdge(units []string) ([]string, error) {
	j.step("login")
	r, err := j.loginByKey()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	relays, err := j.liveRelays()
	if err != nil {
		return nil, err
	}
	// Only the box's own live relays, and only the ones asked for. The names
	// come out of an incident's evidence, which the hub wrote itself — but a
	// name is about to be interpolated into a command on a machine, and
	// "trusted because we wrote it" is exactly the assumption that stops
	// being true the first time something else writes it.
	want := map[string]bool{}
	for _, u := range units {
		want[u] = true
	}
	doomed := make([]provRelay, 0, len(units))
	for _, rel := range relays {
		if want[rel.name] {
			doomed = append(doomed, rel)
		}
	}
	if len(doomed) == 0 {
		return nil, fmt.Errorf("%w: ни один из названных юнитов не живой релей этой машины: %s",
			ErrRejected, strings.Join(units, ", "))
	}

	j.step("restart")
	// The names are the hub's own relay names (boxNameRe-shaped, drawn by
	// AddRelay), checked against the pool just above.
	out, err := r.Run(j.ctx, "bash -s -- "+strings.Join(relayNames(doomed), " "),
		strings.NewReader(edgeRestartScript))
	if err != nil {
		return nil, fmt.Errorf("перезапуск юнитов: %w", err)
	}
	states := restartedStates(out)
	var up, dead []string
	for _, rel := range doomed {
		if states[rel.name] {
			up = append(up, rel.name)
		} else {
			dead = append(dead, rel.name)
		}
		j.log("%s: %s", rel.name, stateWord(states[rel.name]))
	}
	if len(dead) > 0 {
		sort.Strings(dead)
		// Partly up is still a failed repair: the incident is about a relay
		// that is down, and one of two still being down does not end it.
		// What came up is named so the next attempt is not a mystery.
		return up, fmt.Errorf("юниты не поднялись: %s", strings.Join(dead, ", "))
	}

	j.step("probe")
	names := relayNames(doomed)
	if err := j.probe(doomed); err != nil {
		return names, err
	}
	return names, nil
}

// edgeRestartScript restarts each named instance and prints "<relay>=<state>"
// as systemd sees it afterwards. `restart` on a stopped unit starts it, which
// is the ordinary case here, and the state word rather than the exit code is
// what the hub reads.
const edgeRestartScript = `set -u
for n in "$@"; do
	systemctl restart "nekko-edge@$n" 2>/dev/null || true
	echo "$n=$(systemctl is-active "nekko-edge@$n" 2>/dev/null || true)"
done
`

// restartedStates parses the script's output into "relay → active now". A
// relay the script said nothing about is absent, which reads as not active —
// the right way round: a unit the box did not report on is not one the hub may
// call repaired.
func restartedStates(out string) map[string]bool {
	active := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, state, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		active[name] = state == "active"
	}
	return active
}

func stateWord(active bool) string {
	if active {
		return "юнит поднят"
	}
	return "юнит не поднялся"
}

// narrowCovers splits a relay's cover list into the ones still answering and
// the ones that are not, keeping the race order of what it keeps.
//
// Separated from the dialling because it is the decision and the dialling is
// not: which covers a relay ends up with has to be checkable without a
// network, and the order it keeps them in is what a client races.
//
// A cover with no verdict is kept. The map holds what the job managed to dial,
// and "we did not get to ask" must never read as "dead" — that is the same
// rule the grid of П9.4 follows, and breaking it here would strip a relay of a
// cover because the hub's own network blinked.
func narrowCovers(covers []string, alive map[string]bool) (keep, lost []string) {
	keep = make([]string, 0, len(covers))
	for _, c := range covers {
		if state, asked := alive[c]; asked && !state {
			lost = append(lost, c)
			continue
		}
		keep = append(keep, c)
	}
	return keep, lost
}

// RotateDest moves a box's relays off the cover sites that are dead onto the
// ones still answering (П9.6б, playbook rotate_dest), and re-probes admission.
//
// Returns how many relays were changed and which cover hosts were dropped.
//
// # The covers are probed live, not read off the incident
//
// The incident's evidence is as old as the grid run that named it, and a cover
// site is somebody else's server: it can be back by the time the repair runs.
// So the covers are dialled again here, and a set that turns out to be whole
// makes the repair refuse rather than rewrite a box for nothing. The refusal
// is the right outcome — the next probe round closes the incident on its own.
//
// # Why dropping and not replacing
//
// The cover list is a relay's race order, and the hub's pool of proven names
// is exactly `DefaultCovers`: three sites proven through ТСПУ (plan §9, П4).
// Taking a name from outside that set would mean the hub inventing a cover
// nobody vetted, which is a censorship decision and not a repair. So this
// repair narrows the list to what is alive, which fixes the common case — one
// cover down — completely, and refuses when nothing is left, which is the
// honest answer to "every proven cover is gone at once": that one is a person's
// call about the pool, not a machine's. What was dropped is suspect: it comes
// back by its own repair once its second chance clears it (add_covers,
// covers.go), or is struck from the proven set when it does not.
func (p *Provisioner) RotateDest(ctx context.Context, box string) (int, []string, error) {
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return 0, nil, err
	}
	if acc.retired {
		return 0, nil, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	if acc.proto != ProtoVlessRealityVision {
		return 0, nil, fmt.Errorf("%w: машина %s говорит %s, прикрытий у неё нет",
			ErrRejected, box, acc.proto)
	}
	var (
		changed int
		dropped []string
	)
	err = p.execJob(ctx, JobRedest, box, uuid.Nil, func(j *jobRun) error {
		n, d, err := j.rotateDest()
		changed, dropped = n, d
		return err
	})
	if err != nil {
		return changed, dropped, err
	}
	// What was dropped is suspect, not dead (П9.11): its second chance
	// decides whether it is written back or struck.
	if err := p.svc.suspectCovers(ctx, dropped); err != nil {
		return changed, dropped, fmt.Errorf("прикрытия сняты, но в реестр второго шанса не записаны: %w", err)
	}
	return changed, dropped, nil
}

// rotateDest is the job body. Same order as rotateReality: the hub is the
// source of truth for an identity, so it is written first and the box is
// brought to it.
func (j *jobRun) rotateDest() (int, []string, error) {
	svc := j.p.svc
	j.step("login")
	r, err := j.loginByKey()
	if err != nil {
		return 0, nil, err
	}
	defer r.Close()

	relays, err := j.liveRelays()
	if err != nil {
		return 0, nil, err
	}
	if len(relays) == 0 {
		return 0, nil, fmt.Errorf("%w: на машине %s нет живых релеев", ErrRejected, j.box)
	}

	j.step("dests")
	// One verdict per host, not per relay: the relays of a box share covers,
	// and dialling the same site three times says the same thing three times.
	dial := svc.coverDialer()
	alive := map[string]bool{}
	for _, rel := range relays {
		for _, c := range rel.id.Covers {
			if _, seen := alive[c]; seen {
				continue
			}
			alive[c] = dial(c)
			j.log("прикрытие %s: %s", c, yesNo(alive[c], "живо", "мертво"))
		}
	}

	fresh := make([]provRelay, 0, len(relays))
	droppedSet := map[string]bool{}
	for _, rel := range relays {
		keep, lost := narrowCovers(rel.id.Covers, alive)
		if len(lost) == 0 {
			continue
		}
		if len(keep) == 0 {
			return 0, nil, fmt.Errorf(
				"%w: у релея %s не осталось ни одного живого прикрытия (%s) — пул прикрытий надо расширять, это решение человека",
				ErrRejected, rel.name, strings.Join(rel.id.Covers, ", "))
		}
		id, err := rel.id.withCovers(keep)
		if err != nil {
			return 0, nil, fmt.Errorf("релей %s: %w", rel.name, err)
		}
		if err := svc.setRelayIdentity(j.ctx, rel.name, id); err != nil {
			return 0, nil, fmt.Errorf("новые прикрытия %s: %w", rel.name, err)
		}
		for _, c := range lost {
			droppedSet[c] = true
		}
		j.log("%s: прикрытия теперь %s", rel.name, strings.Join(keep, ", "))
		fresh = append(fresh, rel)
	}
	if len(fresh) == 0 {
		return 0, nil, fmt.Errorf("%w: все прикрытия машины живы, менять нечего", ErrRejected)
	}
	changed, err := j.redeployCovers(r, fresh)
	if err != nil {
		return 0, nil, err
	}

	dropped := make([]string, 0, len(droppedSet))
	for c := range droppedSet {
		dropped = append(dropped, c)
	}
	sort.Strings(dropped)
	return len(changed), dropped, nil
}

// redeployCovers brings the box to the cover lists the hub just wrote for
// fresh: the env rendered again and the units restarted, admission
// re-probed, the records re-signed and the holders told to fetch them. The
// tail rotate_dest and add_covers share — the hub is the source of truth for
// an identity, so it is written first and the box is brought to it.
//
// Returns the relays as they now stand.
func (j *jobRun) redeployCovers(r Remote, fresh []provRelay) ([]provRelay, error) {
	svc := j.p.svc
	// Re-read: install renders the env from the identity, and it must render
	// the new cover list.
	live, err := j.liveRelays()
	if err != nil {
		return nil, err
	}
	touched := map[string]bool{}
	for _, rel := range fresh {
		touched[rel.name] = true
	}
	changed := make([]provRelay, 0, len(fresh))
	for _, rel := range live {
		if touched[rel.name] {
			changed = append(changed, rel)
		}
	}
	for _, rel := range changed {
		j.step("deploy " + rel.name)
		if err := j.install(r, rel); err != nil {
			return nil, fmt.Errorf("разворот %s: %w", rel.name, err)
		}
	}

	j.step("probe")
	if err := j.probe(changed); err != nil {
		return nil, err
	}

	// The records the holders carry name the old cover list, and a client
	// racing a dead cover is the very failure being repaired (П6.11).
	j.step("sign")
	for _, rel := range changed {
		if !recorded(rel.status) {
			continue
		}
		if err := svc.Resign(j.ctx, rel.name); err != nil {
			return nil, fmt.Errorf("переподпись %s: %w", rel.name, err)
		}
	}
	if err := svc.nudgeHolders(j.ctx, relayNames(changed)); err != nil {
		j.log("предупредить держателей не удалось: %v — они дотянут до следующего фетча", err)
	}
	return changed, nil
}
