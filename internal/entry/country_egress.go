package entry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/m1d1x/hikkari-control/internal/applog"
)

var countryDEUnit = trunkUnit{
	name: "nekko-trunk-de", dir: "/usr/local/lib/nekko-trunk-de", env: "/etc/nekko-trunk-de.env",
	desc: "the DE country route", title: "trunk entry DE", reloadEnv: "/etc/nekko-trunk-de.env",
}

var countryAutoUnit = trunkUnit{
	name: "nekko-trunk-auto", dir: "/usr/local/lib/nekko-trunk-auto", env: "/etc/nekko-trunk-auto.env",
	desc: "the Auto country route", title: "trunk entry Auto",
}

// CountryEgress is the second country and Auto on an existing NL entry.
// The existing trunk owns NL and keeps its listener, session and points.
type CountryEgress struct {
	Entry, DE                string
	NLPort, DEPort, AutoPort int
}

// SetCountryEgress records desired configuration. The deployed hub stages
// the binaries through its fleet jobs after CI/CD ships the image.
func (p *Provisioner) SetCountryEgress(ctx context.Context, c CountryEgress) error {
	if _, err := p.countryPlan(ctx, c); err != nil {
		return err
	}
	var stored string
	err := p.svc.pool.QueryRow(ctx, `
		INSERT INTO entry_country_egress (entry_box,de_box,nl_port,de_port,auto_port)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (entry_box) DO UPDATE SET entry_box = excluded.entry_box
		WHERE (entry_country_egress.de_box, entry_country_egress.nl_port, entry_country_egress.de_port, entry_country_egress.auto_port)
		    = (excluded.de_box, excluded.nl_port, excluded.de_port, excluded.auto_port)
		RETURNING entry_box`, c.Entry, c.DE, c.NLPort, c.DEPort, c.AutoPort).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: конфигурация стран входа %s уже задана", ErrRejected, c.Entry)
	}
	return err
}

type countryPlan struct {
	CountryEgress
	points         []string
	deEnv, autoEnv string
	borderSHA      string
	sni            string
	pipe           pipeBrain
}

func (p *Provisioner) countryPlan(ctx context.Context, c CountryEgress) (countryPlan, error) {
	plan := countryPlan{CountryEgress: c}
	for _, port := range []int{c.NLPort, c.DEPort, c.AutoPort} {
		if port < 1024 || port > 65535 {
			return plan, fmt.Errorf("%w: порт %d вне диапазона 1024–65535", ErrRejected, port)
		}
	}
	if c.NLPort == c.DEPort || c.NLPort == c.AutoPort || c.DEPort == c.AutoPort {
		return plan, fmt.Errorf("%w: для NL, DE и Авто нужны разные порты входа", ErrRejected)
	}
	pair, err := p.svc.trunkPairOf(ctx, c.Entry)
	if err != nil {
		return plan, err
	}
	if pair.Exit == c.DE || len(pair.Points) != 2 || slices.Contains(pair.Points, c.DE) {
		return plan, fmt.Errorf("%w: нужны два выхода через две общие точки", ErrRejected)
	}
	nlRelayPort := pair.Forward[c.NLPort]
	if nlRelayPort == 0 && pair.PipeExit() == pair.Exit && slices.Equal(pair.Points, pair.PipePoints) {
		nlRelayPort = pair.Pipe[c.NLPort]
	}
	for _, port := range []int{c.DEPort, c.AutoPort} {
		if pair.Forward[port] != 0 || pair.Pipe[port] != 0 {
			return plan, fmt.Errorf("%w: порт входа %d уже используется", ErrRejected, port)
		}
	}
	nl, err := p.svc.receiverOf(ctx, pair.Exit)
	if err != nil {
		return plan, err
	}
	de, err := p.svc.receiverOf(ctx, c.DE)
	if err != nil {
		return plan, err
	}
	for _, box := range []string{pair.Exit, c.DE} {
		acc, err := p.svc.boxAccess(ctx, box)
		if err != nil {
			return plan, err
		}
		if acc.retired || acc.role != RoleEgress {
			return plan, fmt.Errorf("%w: %s должен быть действующим выходом", ErrRejected, box)
		}
	}
	nlh, deh := nl.id.Hikkari, de.id.Hikkari
	if nl.id.Port != nlRelayPort || nlh.TrunkPipeKey == "" || deh.TrunkPipeKey == "" ||
		nlh.Host == "" || nlh.Host != deh.Host {
		return plan, fmt.Errorf("%w: нужны два ядра с общим TLS-именем для Авто", ErrRejected)
	}
	for _, core := range []string{pair.Exit, pair.PipeExit()} {
		if core == c.DE {
			continue
		}
		tcp, udp, err := p.svc.trunkBorderPorts(ctx, core)
		if err != nil {
			return plan, err
		}
		if err := distinctCountryWires(core, tcp, udp, c.DE, de.id); err != nil {
			return plan, err
		}
	}
	plan.points = pair.Points
	points, err := p.svc.pointAddresses(ctx, plan.points)
	if err != nil {
		return plan, err
	}
	plan.pipe, err = p.pipeBrainFor(ctx, c.DE, deh.TrunkPipeWires["h3dgram"], "", "", points)
	if err != nil {
		return plan, err
	}
	if !plan.pipe.open[de.id.Port] {
		return plan, fmt.Errorf("%w: порт релея DE %d закрыт для магистрали", ErrRejected, de.id.Port)
	}
	entryIP, err := p.svc.boxIPv4(ctx, c.Entry)
	if err != nil {
		return plan, err
	}
	border := fmt.Sprintf("%s %s %s %v %v %v", entryIP, c.DE, plan.pipe.addr, plan.points, points, deh.TrunkPipeWires)
	plan.borderSHA = fmt.Sprintf("%x", sha256.Sum256([]byte(border)))
	plan.deEnv = trunkEntryEnv(plan.pipe.addr, plan.pipe.sni, plan.pipe.key, TrunkEntryOptions{
		Forward: map[int]int{c.DEPort: de.id.Port}, Members: pair.Members,
	}) + pipeBrainEnv(countryDEUnit, "4000-7999") + "EDGE_TRUNK_REQUIRE_POINTS=1\n"
	plan.sni = nlh.Host
	plan.autoEnv = autoCountryEnv(c, nlh, deh)
	return plan, nil
}

func distinctCountryWires(other string, tcp, udp []int, country string, relay Identity) error {
	if slices.Contains(tcp, relay.Port) {
		return fmt.Errorf("%w: %s и %s используют TCP-порт %d на общей точке", ErrRejected, other, country, relay.Port)
	}
	for _, port := range relay.Hikkari.TrunkPipeWires {
		if slices.Contains(udp, port) {
			return fmt.Errorf("%w: %s и %s используют UDP-порт %d на общей точке", ErrRejected, other, country, port)
		}
	}
	return nil
}

func autoCountryEnv(c CountryEgress, nl, de *HikkariParams) string {
	// The same trunk keys are already on the entry for the two warm routes.
	return fmt.Sprintf("EDGE_AUTO_LISTEN=0.0.0.0:%d\nEDGE_AUTO_NL=127.0.0.1:%d\nEDGE_AUTO_DE=127.0.0.1:%d\n"+
		"EDGE_TRUNK_FORWARD=%d=%d\nEDGE_AUTO_SNI=%s\nEDGE_AUTO_NL_TRUNK_KEY=%s\nEDGE_AUTO_DE_TRUNK_KEY=%s\n",
		c.AutoPort, c.NLPort, c.DEPort, c.AutoPort, c.AutoPort, nl.Host, nl.TrunkPipeKey, de.TrunkPipeKey)
}

// RollCountryEgress operates only on entries with explicit desired config.
// Run after the image's ordinary fleet rollout, and retry in-progress work.
func (p *Provisioner) RollCountryEgress(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := p.rollCountryEgress(ctx); err != nil && ctx.Err() == nil {
			applog.Error(ctx, "entry", "country egress", applog.F("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Provisioner) rollCountryEgress(ctx context.Context) error {
	rows, err := p.svc.pool.Query(ctx, `SELECT entry_box,de_box,nl_port,de_port,auto_port,border_sha,staged_sha,applied_sha FROM entry_country_egress`)
	if err != nil {
		return err
	}
	type row struct {
		CountryEgress
		border, staged, applied string
	}
	configs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (x row, err error) {
		err = r.Scan(&x.Entry, &x.DE, &x.NLPort, &x.DEPort, &x.AutoPort, &x.border, &x.staged, &x.applied)
		return
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, x := range configs {
		plan, err := p.countryPlan(ctx, x.CountryEgress)
		if err == nil {
			sha := plan.sha(p)
			if sha != x.applied || plan.borderSHA != x.border {
				err = p.applyCountryEgress(ctx, plan, sha, x.border, x.staged)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", x.Entry, err))
		}
	}
	return errors.Join(errs...)
}

func (c countryPlan) sha(p *Provisioner) string {
	h := sha256.New()
	fmt.Fprintln(h, c.deEnv, c.pipe.wires, c.autoEnv, countryDEUnit.unitFile(), countryAutoUnit.unitFile())
	for _, builds := range []map[string]EdgeBuild{p.trunkBuilds, p.pipeBuilds, c.pipe.cronet} {
		for _, arch := range slices.Sorted(maps.Keys(builds)) {
			fmt.Fprintln(h, arch, builds[arch].SHA)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (p *Provisioner) applyCountryEgress(ctx context.Context, plan countryPlan, sha, border, staged string) error {
	return p.execJob(ctx, JobCountryEgress, plan.Entry, uuid.Nil, func(j *jobRun) error {
		if border != plan.borderSHA {
			if err := p.applyCountryBorder(j, plan); err != nil {
				return err
			}
		}
		r, err := j.loginByKey()
		if err != nil {
			return err
		}
		defer r.Close()
		if sha != staged {
			if err := p.stageCountryEntry(j, r, plan, sha); err != nil {
				return err
			}
		}
		j.step("country-probes")
		probe := "set -a\n. " + countryAutoUnit.env + "\nexec " + countryAutoUnit.binary() + " auto-probe"
		if out, err := r.Run(j.ctx, "bash -s", strings.NewReader(probe)); err != nil {
			return fmt.Errorf("проверка выходов: %w: %s", err, out)
		}
		_, err = p.svc.pool.Exec(j.ctx, `UPDATE entry_country_egress SET applied_sha = $2, sni = $3 WHERE entry_box = $1`, plan.Entry, sha, plan.sni)
		return err
	})
}

func (p *Provisioner) applyCountryBorder(j *jobRun, plan countryPlan) error {
	j.step("country-border")
	if err := p.execJob(j.ctx, JobCountryEgress, plan.DE, uuid.Nil, func(core *jobRun) error {
		for _, point := range plan.points {
			if err := p.execJob(core.ctx, JobCountryEgress, point, uuid.Nil, func(pt *jobRun) error {
				pt.step("country-forward")
				return p.StandBorder(pt.ctx, pt.box, plan.DE, false)
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	_, err := p.svc.pool.Exec(j.ctx, `UPDATE entry_country_egress SET border_sha = $2 WHERE entry_box = $1`, plan.Entry, plan.borderSHA)
	return err
}

func (p *Provisioner) stageCountryEntry(j *jobRun, r Remote, plan countryPlan, sha string) error {
	j.step("country-entry")
	trunk := trunkFile{"nekko-trunk-entry", p.trunkBuilds}
	files := []trunkFile{trunk, {"libcronet.so", plan.pipe.cronet}, {"hk-pipe", p.pipeBuilds}}
	if err := standTrunkUnit(j.ctx, r, countryDEUnit, files, plan.deEnv, map[string]string{"wires.json": plan.pipe.wires}, []string{strconv.Itoa(plan.DEPort)}, j.log); err != nil {
		return err
	}
	j.step("country-auto")
	if err := standTrunkUnit(j.ctx, r, countryAutoUnit, []trunkFile{trunk}, plan.autoEnv, nil, []string{strconv.Itoa(plan.AutoPort)}, j.log); err != nil {
		return err
	}
	_, err := p.svc.pool.Exec(j.ctx, `UPDATE entry_country_egress SET staged_sha = $2 WHERE entry_box = $1`, plan.Entry, sha)
	return err
}
