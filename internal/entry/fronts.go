package entry

// fronts.go is the РФ-каскад (hikkari-client plans/
// 2026-11-01-rf-censorship-bypass-research.md §2.4): a cheap machine inside
// Russia in front of one XHTTP exit box abroad.
//
//	client ─TLS, exit's domain─► front (RU) :443 ─TCP─► exit :443 ─► internet
//
// A client in Russia then talks to a Russian address and never crosses the
// border itself: the cross-border hop is the front's, datacenter to
// datacenter. The front terminates nothing — nekko-edge in cascade mode
// (edge/cascade.go) splices TCP to the exit — so TLS, the certificate and its
// renewals, the secret path and the subscribers' keys all stay on the exit,
// and a front carries no credential worth taking.
//
// While a front is active (migration 011):
//
//   - the exit's domain points at the front (AttachFront re-aims it; a fresh
//     exit's provisioning aims it there from the start, dialledAddress);
//   - the exit's relays are signed with the front's address (lockRelayByName),
//     so every subscription dials the front.
//
// A front is not an entry_boxes row: it has no relay, so the pools, the
// autopilot, the rollouts and the detector never see it. For now the
// operator attaches and detaches fronts by hand (`hub entry-front`); a
// burnt front is replaced by attaching another to the same exit.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// NewFront is a Russian machine the operator rented to stand in front of an
// XHTTP exit box.
type NewFront struct {
	Name     string
	ExitBox  string
	Address  string
	Provider string
	// Password is the provider's root password, for the first login only.
	Password string
}

// frontNameRe is entry_fronts.name's check: the same shape as a box's.
var frontNameRe = boxNameRe

// dialledAddress is the address clients dial for box: its active front's, or
// its own (address, as the caller already holds it). via names it for a job
// log, which never carries an address (migration 039).
func (s *Service) dialledAddress(ctx context.Context, box, address string) (dialled, via string, err error) {
	name, front, ok, err := s.activeFront(ctx, s.pool, box)
	if err != nil || !ok {
		return address, box, err
	}
	return front, "вход " + name, nil
}

// activeFront reads box's active front on q: its name and opened address.
func (s *Service) activeFront(ctx context.Context, q db.DBTX, box string) (name, address string, ok bool, err error) {
	var ct []byte
	err = q.QueryRow(ctx, `
		SELECT name, address_ct FROM entry_fronts WHERE exit_box = $1 AND retired_at IS NULL
	`, box).Scan(&name, &ct)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("entry: front of %s: %w", box, err)
	}
	addr, err := cryptobox.Decrypt(ct)
	if err != nil {
		return "", "", false, fmt.Errorf("entry: front %s: %w", name, err)
	}
	return name, string(addr), true, nil
}

// exitForFront checks box can take a front: an XHTTP box in service with its
// domain. Its relays' ports are what the front forwards.
func (s *Service) exitForFront(ctx context.Context, box string) (boxAccess, []int, error) {
	acc, err := s.boxAccess(ctx, box)
	if err != nil {
		return boxAccess{}, nil, err
	}
	switch {
	case acc.retired:
		return boxAccess{}, nil, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	case acc.proto != ProtoVlessXhttp:
		return boxAccess{}, nil, fmt.Errorf("%w: вход ставится только перед XHTTP-машиной, а %s говорит %s", ErrRejected, box, acc.proto)
	case acc.domain == "":
		return boxAccess{}, nil, fmt.Errorf("%w: у %s ещё нет домена — сначала разверни её саму", ErrRejected, box)
	}
	relays, err := s.relayIdentities(ctx, box)
	if err != nil {
		return boxAccess{}, nil, err
	}
	seen := map[int]bool{}
	var ports []int
	for _, r := range relays {
		if !seen[r.id.Port] {
			seen[r.id.Port] = true
			ports = append(ports, r.id.Port)
		}
	}
	if len(ports) == 0 {
		return boxAccess{}, nil, fmt.Errorf("%w: на %s нет живых релеев — пробрасывать нечего", ErrRejected, box)
	}
	sort.Ints(ports)
	return acc, ports, nil
}

// recordFront writes the front's row, or takes the one a re-run left: same
// name, same exit. Refuses a name a box already carries, and a second active
// front on the exit (entry_fronts_one_per_exit).
func (s *Service) recordFront(ctx context.Context, f NewFront) (hostKey string, err error) {
	if !frontNameRe.MatchString(f.Name) {
		return "", fmt.Errorf("%w: имя входа %q — строчные буквы и цифры, как у машин", ErrRejected, f.Name)
	}
	if ip := net.ParseIP(f.Address); ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("%w: адрес входа должен быть IPv4", ErrRejected)
	}
	if strings.TrimSpace(f.Provider) == "" {
		return "", fmt.Errorf("%w: у входа нет провайдера", ErrRejected)
	}
	var taken bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM entry_boxes WHERE name = $1)`, f.Name).Scan(&taken); err != nil {
		return "", err
	}
	if taken {
		return "", fmt.Errorf("%w: имя %s уже у машины флота", ErrRejected, f.Name)
	}
	var exit string
	var key *string
	var retired bool
	err = s.pool.QueryRow(ctx, `SELECT exit_box, host_key, retired_at IS NOT NULL FROM entry_fronts WHERE name = $1`, f.Name).
		Scan(&exit, &key, &retired)
	switch {
	case err == nil && (exit != f.ExitBox || retired):
		return "", fmt.Errorf("%w: вход %s уже был (перед %s) — дай новое имя", ErrRejected, f.Name, exit)
	case err == nil:
		if key != nil {
			return *key, nil
		}
		return "", nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	ct, err := cryptobox.Encrypt([]byte(f.Address))
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO entry_fronts (name, exit_box, address_ct, provider) VALUES ($1, $2, $3, $4)
	`, f.Name, f.ExitBox, ct, f.Provider); err != nil {
		if isUniqueViolation(err) {
			return "", fmt.Errorf("%w: у %s уже есть активный вход — сначала сними его", ErrRejected, f.ExitBox)
		}
		return "", fmt.Errorf("entry: record front %s: %w", f.Name, err)
	}
	return "", nil
}

func (s *Service) setFrontHostKey(ctx context.Context, name, key string) error {
	_, err := s.pool.Exec(ctx, `UPDATE entry_fronts SET host_key = $2 WHERE name = $1 AND host_key IS NULL`, name, key)
	return err
}

func (s *Service) setFrontEdge(ctx context.Context, name, sha string) error {
	_, err := s.pool.Exec(ctx, `UPDATE entry_fronts SET edge_sha = $2 WHERE name = $1`, name, sha)
	return err
}

// resignBox re-signs every recorded relay of box: their records take the
// address clients should dial now.
func (s *Service) resignBox(ctx context.Context, box string) ([]string, error) {
	relays, err := s.relayIdentities(ctx, box)
	if err != nil {
		return nil, err
	}
	var done []string
	for _, r := range relays {
		if !recorded(r.status) {
			continue
		}
		if err := s.Resign(ctx, r.name); err != nil {
			return done, fmt.Errorf("переподпись %s: %w", r.name, err)
		}
		done = append(done, r.name)
	}
	return done, nil
}

// aimDomain points box's domain at ip through the saved registrar.
func (s *Service) aimDomain(ctx context.Context, domain, ip string) error {
	b := s.currentBuyer()
	if b == nil || b.reg == nil {
		return fmt.Errorf("%w: регистратор не подключён — A-запись %s не переставить", ErrRejected, domain)
	}
	return b.reg.SetARecord(ctx, domain, ip)
}

// frontCheckTimeout bounds AttachFront's TLS check through the new front.
const frontCheckTimeout = 2 * time.Minute

// AttachFront stands a rented Russian machine in front of an XHTTP exit box,
// synchronously (the operator's CLI follows it line by line in the hub log):
// login and harden as any box, nekko-edge in cascade mode forwarding the
// exit's relay ports, then — only once the exit's own certificate answers
// through the front — the exit's domain is re-aimed at the front and its
// relays re-signed with the front's address. Running it again is the update.
func (p *Provisioner) AttachFront(ctx context.Context, f NewFront) error {
	svc := p.svc
	exit, ports, err := svc.exitForFront(ctx, f.ExitBox)
	if err != nil {
		return err
	}
	hostKey, err := svc.recordFront(ctx, f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
	defer cancel()
	j := &jobRun{p: p, ctx: ctx, box: f.Name, firstUse: trustOperator, front: true}
	j.acc = boxAccess{address: f.Address, user: "root", hostKey: hostKey, proto: ProtoVlessXhttp}
	hubKey, hubPub, err := svc.HubSSHKey(ctx)
	if err != nil {
		return fmt.Errorf("ssh-ключ хаба: %w", err)
	}

	j.step("login")
	r, err := j.login(j.acc, hubKey, hubPub, f.Password)
	if err != nil {
		return err
	}
	defer r.Close()

	j.step("harden")
	open := make([]string, len(ports))
	forwards := make([]string, len(ports))
	for i, port := range ports {
		open[i] = strconv.Itoa(port) + "/tcp"
		forwards[i] = strconv.Itoa(port) + "=" + net.JoinHostPort(exit.address, strconv.Itoa(port))
	}
	if _, err := r.Run(ctx, "bash -s -- "+strings.Join(open, " "), strings.NewReader(hardenScript)); err != nil {
		return fmt.Errorf("закрытие машины: %w", err)
	}
	j.log("вход по паролю выключен, ufw: 22 %s", strings.Join(open, " "))

	arch, err := j.arch(r)
	if err != nil {
		return err
	}
	build, ok := p.builds[arch]
	if !ok {
		return fmt.Errorf("в образе хаба нет бинаря релея под %s", arch)
	}
	j.step("deploy " + f.Name)
	have, _ := r.Run(ctx, "sha256sum /usr/local/bin/nekko-edge 2>/dev/null | cut -d' ' -f1", nil)
	if err := j.stage(r, build, strings.TrimSpace(have)); err != nil {
		return err
	}
	env := map[string]string{"EDGE_PROTO": "cascade", "EDGE_FORWARD": strings.Join(forwards, ",")}
	if err := j.swapIn(r, f.Name, "entry_fronts", env, ports[0], "tcp"); err != nil {
		return err
	}
	if err := svc.setFrontEdge(ctx, f.Name, build.SHA); err != nil {
		return err
	}
	j.log("nekko-edge@%s пробрасывает %d порт(а) на %s", f.Name, len(ports), f.ExitBox)

	// The proof the cascade works, before any client is sent to it: the
	// exit's own certificate for its domain, verified, answered through the
	// front's address.
	j.step("check")
	if err := p.certCheck(ctx, f.Address, ports[0], exit.domain); err != nil {
		return fmt.Errorf("через вход %s сертификат %s не отвечает: %w", f.Name, exit.domain, err)
	}
	j.log("через %s отвечает %s с действительным сертификатом", f.Name, exit.domain)

	j.step("aim")
	if err := svc.aimDomain(ctx, exit.domain, f.Address); err != nil {
		return fmt.Errorf("A-запись %s: %w", exit.domain, err)
	}
	j.log("A-запись %s → %s", exit.domain, f.Name)
	if len(p.dnsResolvers) > 0 {
		if err := j.waitDNS(exit.domain, f.Address); err != nil {
			return err
		}
		j.log("резолверы отдают %s", exit.domain)
	}

	j.step("sign")
	names, err := svc.resignBox(ctx, f.ExitBox)
	if err != nil {
		return err
	}
	j.log("релеи %s подписаны адресом входа: %s", f.ExitBox, strings.Join(names, ", "))
	return nil
}

// DetachFront retires a front: the exit's domain goes back to the exit and
// its relays are re-signed with the exit's own address. The front machine is
// left as it is; it carries nothing, and is the operator's to delete.
func (p *Provisioner) DetachFront(ctx context.Context, name string) error {
	svc := p.svc
	var exit string
	err := svc.pool.QueryRow(ctx, `
		UPDATE entry_fronts SET retired_at = now() WHERE name = $1 AND retired_at IS NULL RETURNING exit_box
	`, name).Scan(&exit)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: активного входа %s нет", ErrRejected, name)
	}
	if err != nil {
		return err
	}
	acc, err := svc.boxAccess(ctx, exit)
	if err != nil {
		return err
	}
	if acc.domain != "" {
		if err := svc.aimDomain(ctx, acc.domain, acc.address); err != nil {
			return fmt.Errorf("A-запись %s обратно на %s: %w", acc.domain, exit, err)
		}
	}
	_, err = svc.resignBox(ctx, exit)
	return err
}

// checkFront dials address and expects the certificate for domain — through
// a front, the exit's; on an XHTTP box itself, its own —
// verified against the system roots — the same check a client's TLS makes.
// Retried until frontCheckTimeout: the edge may still be coming up.
func checkFront(ctx context.Context, address string, port int, domain string) error {
	deadline := time.Now().Add(frontCheckTimeout)
	var last error
	for {
		d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{ServerName: domain, MinVersion: tls.VersionTLS12}}
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(address, strconv.Itoa(port)))
		if err == nil {
			return c.Close()
		}
		last = err
		if time.Now().After(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(5 * time.Second):
		}
	}
}
