package entry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// A spare receiver of a group (hikkari-client
// plans/2026-11-16-second-receiver.md, P2): another machine that is, to a
// client, the same server as the primary receiver. Its Hikkari relay has the
// primary's identity whole: the domain, the probe credential, the port, the
// trunk's keys and wires, so the entry's trunk reaches it as it reaches the
// primary and a subscriber's key opens it. The certificate is the group's
// (rxcert.go): the domain's A record stays with the primary, so Caddy on the
// spare could not order its own.
//
//	cmd/server entry-box receiver-spare <box> <primary-box> [replace]
//
// replace: the box's live relays die first (the owner's word of 16.11 for
// ams3: its Reality relays give way to the receiver); without it a box with
// serving relays is refused.

// PrepareReceiverSpare turns box into a spare receiver of primary's group and
// queues its provisioning job.
func (s *Service) PrepareReceiverSpare(ctx context.Context, box, primary string, replace bool, adminID string) (uuid.UUID, error) {
	if box == primary {
		return uuid.Nil, fmt.Errorf("%w: a box is not its own spare", ErrRejected)
	}
	src, err := s.receiverOf(ctx, primary)
	if err != nil {
		return uuid.Nil, err
	}
	domain := src.id.Hikkari.Host
	if env, err := s.receiverCertEnv(ctx, domain); err != nil {
		return uuid.Nil, err
	} else if env == nil {
		return uuid.Nil, fmt.Errorf("%w: у группы %s нет сертификата от хаба, сначала receiver-cert issue %s", ErrRejected, domain, domain)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if err = s.repo.lockBoxes(ctx, tx); err != nil {
		return uuid.Nil, err
	}
	lb, ok, err := s.repo.lockBox(ctx, tx, box)
	if err != nil {
		return uuid.Nil, err
	} else if !ok {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrNoSuchBox, box)
	} else if lb.retired {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	if err := refuseVantageBox(ctx, tx, box); err != nil {
		return uuid.Nil, err
	}
	if lb.role == RoleEgress {
		return uuid.Nil, fmt.Errorf("%w: %s is an egress: no relay, no keys, ever", ErrRejected, box)
	}
	var running int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM entry_jobs WHERE box=$1 AND status='running'`, box).Scan(&running); err != nil {
		return uuid.Nil, err
	}
	if running != 0 {
		return uuid.Nil, fmt.Errorf("%w: box has a running job", ErrBoxBusy)
	}
	existing, err := s.repo.relaysOnBox(ctx, tx, box)
	if err != nil {
		return uuid.Nil, err
	}
	var killed []string
	var holders []uuid.UUID
	for _, r := range existing {
		if r.status == "dead" {
			continue
		}
		if lb.proto == ProtoHikkari {
			if id, err := openIdentity(r.identityCT); err == nil && id.Hikkari != nil && strings.EqualFold(id.Hikkari.Host, domain) {
				return uuid.Nil, fmt.Errorf("%w: %s уже приёмник группы %s (%s)", ErrRejected, box, domain, r.name)
			}
		}
		if !replace {
			return uuid.Nil, fmt.Errorf("%w: на %s живые релеи (%s), без replace не трогаю", ErrRejected, box, r.name)
		}
		h, _, err := s.repo.markDead(ctx, tx, r.name, DeathRotated)
		if err != nil {
			return uuid.Nil, fmt.Errorf("релей %s: %w", r.name, err)
		}
		holders = append(holders, h...)
		killed = append(killed, r.name)
	}
	domainCT, err := cryptobox.Encrypt([]byte(domain))
	if err != nil {
		return uuid.Nil, err
	}
	// The domain is the group's and stays assigned to the primary in
	// entry_domains: the spare only carries it, ensureDomain leaves the A
	// record where it is.
	if _, err = tx.Exec(ctx, `UPDATE entry_boxes SET proto=$2, domain_ct=$3, region=NULL WHERE name=$1`, box, ProtoHikkari, domainCT); err != nil {
		return uuid.Nil, err
	}
	ct, err := sealIdentity(src.id)
	if err != nil {
		return uuid.Nil, err
	}
	name := fmt.Sprintf("%s-%d", box, len(existing)+1)
	if err := s.repo.insertPendingRelay(ctx, tx, name, KindPersonal, box, ct); err != nil {
		return uuid.Nil, err
	}
	var admin any
	if adminID != "" {
		admin = adminID
	}
	var job uuid.UUID
	if err = tx.QueryRow(ctx, insertJob, box, JobProvision, admin, nil).Scan(&job); err != nil {
		return uuid.Nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	if s.notify != nil && len(holders) > 0 {
		s.notify.RelaysChanged(holders)
	}
	applog.Info(ctx, "entry", "receiver spare", applog.F("box", box), applog.F("primary", primary),
		applog.F("relay", name), applog.F("replaced", strings.Join(killed, ",")))
	return job, nil
}

// receiverOf is the Hikkari relay on box that ends a trunk: the one a spare
// copies.
func (s *Service) receiverOf(ctx context.Context, box string) (provRelay, error) {
	relays, err := s.relayIdentities(ctx, box)
	if err != nil {
		return provRelay{}, err
	}
	var found []provRelay
	for _, r := range relays {
		if h := r.id.Hikkari; h != nil && (h.TrunkKey != "" || h.TrunkPipeKey != "") {
			found = append(found, r)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return provRelay{}, fmt.Errorf("%w: на %s нет Hikkari-релея с магистралью", ErrRejected, box)
	default:
		return provRelay{}, fmt.Errorf("%w: на %s %d релея с магистралью, какой копировать, неясно", ErrRejected, box, len(found))
	}
}

// groupDomainHolder is the box the domain's A record belongs to when that is
// another box than this one: this box is a spare of that one's group. ""
// when the domain is this box's own or the hub holds no record of it.
func (s *Service) groupDomainHolder(ctx context.Context, box, domain string) (string, error) {
	var holder *string
	err := s.pool.QueryRow(ctx, `SELECT box FROM entry_domains WHERE domain = $1`, strings.ToLower(domain)).Scan(&holder)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	if holder == nil || *holder == box {
		return "", nil
	}
	return *holder, nil
}

// AddReceiverSpare is PrepareReceiverSpare and the provisioning job that
// follows it.
func (p *Provisioner) AddReceiverSpare(ctx context.Context, box, primary string, replace bool, adminID string) (uuid.UUID, error) {
	id, err := p.svc.PrepareReceiverSpare(ctx, box, primary, replace, adminID)
	if err != nil {
		return uuid.Nil, err
	}
	p.svc.spawn(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
		defer cancel()
		j := &jobRun{p: p, ctx: ctx, id: id, box: box}
		err := j.provision("")
		j.finish(err, "готово: запасной приёмник группы "+primary)
	})
	return id, nil
}
