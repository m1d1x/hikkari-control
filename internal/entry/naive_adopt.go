package entry

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"strings"
	"time"
)

// prepareAdoption claims the idle laboratory box for proto (Naive or Hikkari,
// both served by Caddy on the box's own domain) and creates its job in one
// transaction. Neither a serving relay nor a concurrent job is replaced.
func (s *Service) prepareAdoption(ctx context.Context, box, proto, domain, adminID string) (uuid.UUID, error) {
	if proto != ProtoNaive && proto != ProtoHikkari {
		return uuid.Nil, fmt.Errorf("%w: adoption of %q", ErrRejected, proto)
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	if err := checkRelayDomain(domain); err != nil {
		return uuid.Nil, err
	}
	if err := s.checkNotExcluded(ctx, domain); err != nil {
		return uuid.Nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	if err = s.repo.lockBoxes(ctx, tx); err != nil {
		return uuid.Nil, err
	}
	var retired bool
	var previousProto string
	var previousDomain []byte
	if err = tx.QueryRow(ctx, `SELECT retired_at IS NOT NULL,proto,domain_ct FROM entry_boxes WHERE name=$1 FOR UPDATE`, box).Scan(&retired, &previousProto, &previousDomain); err != nil {
		return uuid.Nil, err
	}
	if retired {
		return uuid.Nil, fmt.Errorf("%w: cannot adopt a retired box", ErrRejected)
	}
	// A failed attempt may have reserved a pending Naive identity. Reuse it
	// only for the same domain; never change any serving relay.
	retry := false
	if previousProto == proto && len(previousDomain) != 0 {
		plain, decryptErr := cryptobox.Decrypt(previousDomain)
		if decryptErr != nil {
			return uuid.Nil, decryptErr
		}
		retry = string(plain) == domain
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM entry_relays WHERE box=$1 AND status<>'dead' AND NOT ($2 AND status='pending')`, box, retry).Scan(&count); err != nil {
		return uuid.Nil, err
	}
	if count != 0 {
		return uuid.Nil, fmt.Errorf("%w: box has serving relays; conversion refused", ErrRejected)
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM entry_jobs WHERE box=$1 AND status='running'`, box).Scan(&count); err != nil {
		return uuid.Nil, err
	}
	if count != 0 {
		return uuid.Nil, fmt.Errorf("%w: box has a running job", ErrBoxBusy)
	}
	ct, err := cryptobox.Encrypt([]byte(domain))
	if err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE entry_boxes SET proto=$2,domain_ct=$3,region=NULL WHERE name=$1`, box, proto, ct); err != nil {
		return uuid.Nil, err
	}
	var admin any
	if adminID != "" {
		admin = adminID
	}
	var id uuid.UUID
	if err = tx.QueryRow(ctx, insertJob, box, JobProvision, admin, nil).Scan(&id); err != nil {
		return uuid.Nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Adopt moves the recorded experiment into the ordinary deploy/admit/sign
// pipeline as a Naive or a Hikkari box. The laboratory Caddy is restored if its
// replacement cannot deploy.
func (p *Provisioner) Adopt(ctx context.Context, box, proto, domain, adminID string) (uuid.UUID, error) {
	id, err := p.svc.prepareAdoption(ctx, box, proto, domain, adminID)
	if err != nil {
		return uuid.Nil, err
	}
	p.svc.spawn(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
		defer cancel()
		j := &jobRun{p: p, ctx: ctx, id: id, box: box, naiveAdopt: true}
		err := j.provision("")
		if err != nil && j.naiveLegacyStopped {
			recovery, stop := context.WithTimeout(context.Background(), 30*time.Second)
			command := "systemctl stop nekko-edge@" + j.naiveInstance + ".service; systemctl enable --now naive-node-caddy.service"
			_, restoreErr := p.RunOnBox(recovery, box, command, nil)
			stop()
			if restoreErr != nil {
				err = fmt.Errorf("%w; laboratory restore failed: %v", err, restoreErr)
			} else {
				j.log("лабораторный Caddy восстановлен после неудачного разворота")
			}
		}
		j.finish(err, "готово: "+proto+" в штатном пуле")
	})
	return id, nil
}
