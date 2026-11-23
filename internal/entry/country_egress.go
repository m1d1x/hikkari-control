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

var legacyCountryAutoUnit = trunkUnit{
	name: "nekko-trunk-auto", dir: "/usr/local/lib/nekko-trunk-auto", env: "/etc/nekko-trunk-auto.env",
	desc: "the Auto country route", title: "trunk entry Auto",
}

var countryGatewayUnit = trunkUnit{
	name: "nekko-trunk-countries", dir: "/usr/local/lib/nekko-trunk-countries", env: "/etc/nekko-trunk-countries.env",
	desc: "the country gateways", title: "country gateways",
}

const (
	countryNLBackendPort = 8343
	countryDEBackendPort = 8344
)

// CountryEgress attaches two country gateways to an existing NL stand.
type CountryEgress struct {
	Entry, DE      string
	NLPort, DEPort int
}

// SetCountryEgress records desired configuration. The deployed hub stages
// the binaries through its fleet jobs after CI/CD ships the image.
func (p *Provisioner) SetCountryEgress(ctx context.Context, c CountryEgress) error {
	if _, err := p.countryPlan(ctx, c); err != nil {
		return err
	}
	var stored string
	err := p.svc.pool.QueryRow(ctx, `
		INSERT INTO entry_country_egress (entry_box,de_box,nl_port,de_port)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (entry_box) DO UPDATE SET entry_box = excluded.entry_box
		WHERE (entry_country_egress.de_box, entry_country_egress.nl_port, entry_country_egress.de_port)
		    = (excluded.de_box, excluded.nl_port, excluded.de_port)
		RETURNING entry_box`, c.Entry, c.DE, c.NLPort, c.DEPort).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: конфигурация стран входа %s уже задана", ErrRejected, c.Entry)
	}
	return err
}

type countryPlan struct {
	CountryEgress
	points                   []string
	nlEnv, deEnv, gatewayEnv string
	borderSHA                string
	sni                      string
	nlPipe, dePipe           pipeBrain
}

func (p *Provisioner) countryPlan(ctx context.Context, c CountryEgress) (countryPlan, error) {
	plan := countryPlan{CountryEgress: c}
	for _, port := range []int{c.NLPort, c.DEPort} {
		if port < 1024 || port > 65535 {
			return plan, fmt.Errorf("%w: порт %d вне диапазона 1024–65535", ErrRejected, port)
		}
	}
	if c.NLPort == c.DEPort {
		return plan, fmt.Errorf("%w: для NL и DE нужны разные порты входа", ErrRejected)
	}
	pair, err := p.svc.trunkPairOf(ctx, c.Entry)
	if err != nil {
		return plan, err
	}
	if pair.Exit == c.DE || len(pair.Points) != 2 || slices.Contains(pair.Points, c.DE) {
		return plan, fmt.Errorf("%w: нужны два выхода через две общие точки", ErrRejected)
	}
	// Translate only the single-route stand. The production trunk's listeners
	// remain owned by the production unit.
	nlRelayPort := pair.Pipe[c.NLPort]
	if nlRelayPort == 0 || len(pair.Pipe) != 1 || pair.PipeExit() != pair.Exit ||
		!slices.Equal(pair.Points, pair.PipePoints) {
		return plan, fmt.Errorf("%w: нужен стенд NL с одним портом и обеими точками", ErrRejected)
	}
	for _, port := range []int{c.NLPort, c.DEPort, countryNLBackendPort, countryDEBackendPort} {
		if pair.Forward[port] != 0 || (port != c.NLPort && pair.Pipe[port] != 0) {
			return plan, fmt.Errorf("%w: порт входа %d уже используется", ErrRejected, port)
		}
	}
	for _, port := range []int{c.NLPort, c.DEPort} {
		if port == countryNLBackendPort || port == countryDEBackendPort {
			return plan, fmt.Errorf("%w: порт %d зарезервирован для loopback", ErrRejected, port)
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
		return plan, fmt.Errorf("%w: нужны два ядра с общим TLS-именем", ErrRejected)
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
	plan.nlPipe, err = p.pipeBrainFor(ctx, pair.Exit, nlh.TrunkPipeWires["h3dgram"], "", "", points)
	if err != nil {
		return plan, err
	}
	plan.nlPipe.wires, err = p.svc.pointTLSWires(ctx, plan.nlPipe.wires, c.Entry, pair.Exit, true, plan.points, points)
	if err != nil {
		return plan, err
	}
	plan.dePipe, err = p.pipeBrainFor(ctx, c.DE, deh.TrunkPipeWires["h3dgram"], "", "", points)
	if err != nil {
		return plan, err
	}
	if !plan.nlPipe.open[nlRelayPort] || !plan.dePipe.open[de.id.Port] {
		return plan, fmt.Errorf("%w: порты релеев NL и DE должны быть открыты для магистрали", ErrRejected)
	}
	entryIP, err := p.svc.boxIPv4(ctx, c.Entry)
	if err != nil {
		return plan, err
	}
	// countryBorderVersion changes the hash when the border job itself
	// gains a step, so an already applied DE route runs it once more.
	border := fmt.Sprintf("%s %s %s %v %v %v %s", entryIP, c.DE, plan.dePipe.addr, plan.points, points, deh.TrunkPipeWires, countryBorderVersion)
	plan.borderSHA = fmt.Sprintf("%x", sha256.Sum256([]byte(border)))
	plan.nlEnv = trunkPipeEnvOf(plan.nlPipe.addr, plan.nlPipe.sni, plan.nlPipe.key, TrunkPipeOptions{
		Forward: map[int]int{countryNLBackendPort: nlRelayPort}, Members: pair.Members,
	}) + countryBackendEnv
	plan.deEnv = trunkEntryEnv(plan.dePipe.addr, plan.dePipe.sni, plan.dePipe.key, TrunkEntryOptions{
		Forward: map[int]int{countryDEBackendPort: de.id.Port}, Members: pair.Members,
	}) + pipeBrainEnv(countryDEUnit, "4000-7999") + countryBackendEnv
	plan.sni = nlh.Host
	plan.gatewayEnv = countryGatewayEnv(c, nlh, deh)
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

const countryBackendEnv = "EDGE_TRUNK_REQUIRE_POINTS=1\nEDGE_TRUNK_LOOPBACK=1\n"

func countryGatewayEnv(c CountryEgress, nl, de *HikkariParams) string {
	return fmt.Sprintf("EDGE_COUNTRY_NL_LISTEN=0.0.0.0:%d\nEDGE_COUNTRY_DE_LISTEN=0.0.0.0:%d\n"+
		"EDGE_COUNTRY_NL_BACKEND=127.0.0.1:%d\nEDGE_COUNTRY_DE_BACKEND=127.0.0.1:%d\n"+
		"EDGE_COUNTRY_SNI=%s\nEDGE_COUNTRY_NL_TRUNK_KEY=%s\nEDGE_COUNTRY_DE_TRUNK_KEY=%s\n",
		c.NLPort, c.DEPort, countryNLBackendPort, countryDEBackendPort, nl.Host, nl.TrunkPipeKey, de.TrunkPipeKey)
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
	rows, err := p.svc.pool.Query(ctx, `SELECT entry_box,de_box,nl_port,de_port,border_sha,staged_sha,applied_sha FROM entry_country_egress`)
	if err != nil {
		return err
	}
	type row struct {
		CountryEgress
		border, staged, applied string
	}
	configs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (x row, err error) {
		err = r.Scan(&x.Entry, &x.DE, &x.NLPort, &x.DEPort, &x.border, &x.staged, &x.applied)
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
	fmt.Fprintln(h, c.nlEnv, c.nlPipe.wires, c.deEnv, c.dePipe.wires, c.gatewayEnv, trunkPipeUnitSpec.unitFile(), countryDEUnit.unitFile(), countryGatewayUnit.unitFile())
	for _, builds := range []map[string]EdgeBuild{p.trunkBuilds, p.pipeBuilds, c.nlPipe.cronet, c.dePipe.cronet} {
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
		probe := "set -a\n. " + countryGatewayUnit.env + "\nexec " + countryGatewayUnit.binary() + " country-probe"
		if out, err := r.Run(j.ctx, "bash -s", strings.NewReader(probe)); err != nil {
			return fmt.Errorf("проверка выходов: %w: %s", err, out)
		}
		_, err = p.svc.pool.Exec(j.ctx, `UPDATE entry_country_egress SET applied_sha = $2, sni = $3 WHERE entry_box = $1`, plan.Entry, sha, plan.sni)
		return err
	})
}

// countryBorderVersion: noru1, the DE core cuts its own new connections to
// Russia (hikkari_noru), as every box abroad does.
const countryBorderVersion = "noru1"

func (p *Provisioner) applyCountryBorder(j *jobRun, plan countryPlan) error {
	j.step("country-border")
	if err := p.execJob(j.ctx, JobCountryEgress, plan.DE, uuid.Nil, func(core *jobRun) error {
		// The DE core has no egress tunnel and so no hikkari_egress table:
		// its own cut of Russia comes from the same script as a point's.
		core.step("country-noru")
		ruNets, err := fetchRUNets(core.ctx, ripeDelegatedURL)
		if err != nil {
			return fmt.Errorf("сети РФ для запрета на %s: %w", plan.DE, err)
		}
		r, err := core.loginByKey()
		if err != nil {
			return err
		}
		out, err := r.Run(core.ctx, "bash -s", strings.NewReader(noRUScript(ruNets)))
		r.Close()
		if err != nil {
			return fmt.Errorf("%s: запрет РФ: %w: %s", plan.DE, err, out)
		}
		core.log("запрет РФ на %s: %s", plan.DE, strings.TrimSpace(out))
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
	trunk := trunkFile{"nekko-trunk-entry", p.trunkBuilds}
	for _, backend := range []struct {
		unit trunkUnit
		pipe pipeBrain
		env  string
		port int
	}{
		{countryDEUnit, plan.dePipe, plan.deEnv, countryDEBackendPort},
		{trunkPipeUnitSpec, plan.nlPipe, plan.nlEnv, countryNLBackendPort},
	} {
		j.step(backend.unit.name)
		files := []trunkFile{trunk, {"libcronet.so", backend.pipe.cronet}, {"hk-pipe", p.pipeBuilds}}
		if err := standTrunkUnit(j.ctx, r, backend.unit, files, backend.env, map[string]string{"wires.json": backend.pipe.wires}, []string{strconv.Itoa(backend.port)}, j.log); err != nil {
			return err
		}
	}
	j.step("country-gateways")
	if err := standTrunkUnit(j.ctx, r, countryGatewayUnit, []trunkFile{trunk}, plan.gatewayEnv, nil, []string{strconv.Itoa(plan.NLPort), strconv.Itoa(plan.DEPort)}, j.log); err != nil {
		return err
	}
	j.step("remove-country-auto")
	if out, err := r.Run(j.ctx, "bash -s", strings.NewReader(legacyCountryAutoUnit.offScript())); err != nil {
		return fmt.Errorf("снятие старого шлюза: %w: %s", err, out)
	}
	_, err := p.svc.pool.Exec(j.ctx, `UPDATE entry_country_egress SET staged_sha = $2 WHERE entry_box = $1`, plan.Entry, sha)
	return err
}
