package entry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AdoptVantage imports an observation machine, never a relay. The password is
// held only on this call's stack and never handed to a job or persisted.
func (p *Provisioner) AdoptVantage(ctx context.Context, slot, address, user, password string) (uuid.UUID, error) {
	if !p.CanDeployProber() {
		return uuid.Nil, fmt.Errorf("adopt preflight: %w: prober artifacts or API URL missing", ErrRejected)
	}
	if !regionRe.MatchString(slot) || net.ParseIP(address) == nil {
		return uuid.Nil, fmt.Errorf("adopt validation: %w: expected slot and IP address", ErrRejected)
	}
	address = net.ParseIP(address).String()
	if user == "" {
		user = "root"
	}
	if !sshUserRe.MatchString(user) {
		return uuid.Nil, fmt.Errorf("adopt validation: %w: invalid SSH user", ErrRejected)
	}
	v, err := p.svc.VantageSlot(ctx, slot)
	if err != nil {
		return uuid.Nil, fmt.Errorf("adopt slot: %w", err)
	}
	if v.Status != VantageActive {
		return uuid.Nil, fmt.Errorf("adopt slot: %w: slot is retired", ErrRejected)
	}
	box := v.Box
	if box == "" {
		boxes, e := p.svc.Boxes(ctx)
		if e != nil {
			return uuid.Nil, fmt.Errorf("adopt inventory: %w", e)
		}
		for _, b := range boxes {
			if b.Address == address && b.holdsAddress() {
				box = b.Name
				break
			}
		}
	}
	if box == "" {
		// Deterministic, within the existing <letters><number> box-name contract.
		sum := sha256.Sum256([]byte(slot))
		name := hex.EncodeToString(sum[:8])
		name = strings.NewReplacer("0", "g", "1", "h", "2", "i", "3", "j", "4", "k", "5", "l", "6", "m", "7", "n", "8", "o", "9", "p").Replace(name)
		box = "vp" + name + "1"
		if err = p.svc.CreateBox(ctx, BoxSpec{Name: box, Address: address, SSHUser: user, Provider: nonempty(v.Provider, "vantage"), Location: slot}); err != nil {
			return uuid.Nil, fmt.Errorf("adopt register box: %w", err)
		}
	}
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return uuid.Nil, fmt.Errorf("adopt box: %w", err)
	}
	if acc.address != address || acc.user != user || acc.retired {
		return uuid.Nil, fmt.Errorf("adopt box: %w: address/user mismatch or retired box", ErrRejected)
	}
	// Bind before logging in, so AddRelay cannot race the deployment.
	if err = p.svc.bindAdoptedVantage(ctx, slot, box); err != nil {
		return uuid.Nil, fmt.Errorf("adopt bind slot: %w", err)
	}

	err = p.execJob(ctx, JobProber, box, uuid.Nil, func(j *jobRun) error {
		j.firstUse = trustOperator
		j.step("adopt login")
		key, pub, e := p.svc.HubSSHKey(ctx)
		if e != nil {
			return fmt.Errorf("adopt hub key: %w", e)
		}
		r, e := j.login(acc, key, pub, password)
		if e != nil {
			return fmt.Errorf("adopt login: %w", e)
		}
		defer r.Close()
		r = proberPrivileges(r, user)
		if _, e = p.proberRemote(ctx, r); e != nil {
			return fmt.Errorf("adopt prober ownership: %w", e)
		}
		j.step("adopt disable password")
		if _, e = r.Run(ctx, "bash -s", strings.NewReader(proberSSHHardening)); e != nil {
			return fmt.Errorf("adopt disable password: %w", e)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	id, err := p.DeployProber(ctx, box, slot, "")
	if err != nil {
		return uuid.Nil, fmt.Errorf("adopt deploy: %w", err)
	}
	return id, nil
}

func nonempty(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// Distro-independent: AlmaLinux needs sshd, Debian may call the unit ssh.
// The Include is prepended because sshd uses the first value, including on
// distributions whose main config has PasswordAuthentication before Include.
const proberSSHHardening = `set -eu
mkdir -p /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/00-nekko-hardening.conf <<'CONF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
CONF
if ! head -n 1 /etc/ssh/sshd_config | grep -qxF 'Include /etc/ssh/sshd_config.d/00-nekko-hardening.conf'; then
 { echo 'Include /etc/ssh/sshd_config.d/00-nekko-hardening.conf'; cat /etc/ssh/sshd_config; } > /etc/ssh/sshd_config.nekko-new
 cp -p /etc/ssh/sshd_config /etc/ssh/sshd_config.nekko-old
 cat /etc/ssh/sshd_config.nekko-new > /etc/ssh/sshd_config
fi
sshd -t
sshd -T | grep -qx 'passwordauthentication no'
sshd -T | grep -qx 'kbdinteractiveauthentication no'
systemctl reload sshd 2>/dev/null || systemctl reload ssh
`

// WaitProberJob is shared by hub commands. Its deadline belongs to the caller.
func (s *Service) WaitProberJob(ctx context.Context, id uuid.UUID) error {
	for {
		j, err := s.Job(ctx, id)
		if err != nil {
			return err
		}
		if j.Status != "running" {
			if j.Status != "done" {
				return fmt.Errorf("prober job %s: %s", id, j.Error)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Binding changes inventory ownership without invalidating the installed
// token. DeployProber rotates it only while holding the machine's job lock.
func (s *Service) bindAdoptedVantage(ctx context.Context, slot, box string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT 1 FROM entry_boxes WHERE name=$1 FOR UPDATE`, box); err != nil {
		return err
	}
	if err = refuseFleetBox(ctx, tx, box); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE entry_vantages SET box=$2,host_ct=NULL,updated_at=now() WHERE slot=$1 AND status='active' AND (box IS NULL OR box=$2)`, slot, box)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: slot changed during adoption", ErrRejected)
	}
	return tx.Commit(ctx)
}
