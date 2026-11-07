package entry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// RegisterProberUpdates uses the same slot authentication as admission polls.
// Build paths come only from the image inventory, never from the request.
func RegisterProberUpdates(mux *http.ServeMux, s *Service, p *Provisioner) {
	gate := vantageGate(s)
	mux.HandleFunc("GET /api/v1/internal/entry-probes/update", gate(func(w http.ResponseWriter, r *http.Request) {
		arch, sha := r.URL.Query().Get("arch"), r.URL.Query().Get("sha")
		build, ok := p.proberBuilds[arch]
		if !ok || !shaRe.MatchString(sha) {
			writeJSON(w, 400, map[string]string{"error": "unsupported arch or sha"})
			return
		}
		var standalone bool
		err := s.pool.QueryRow(r.Context(), `UPDATE entry_vantages SET prober_arch=$2, prober_sha=$3, prober_poll_at=now() WHERE slot=$1 AND status='active' RETURNING box IS NULL`, vantageOf(r), arch, sha).Scan(&standalone)
		if err != nil {
			writeJSON(w, 503, map[string]string{"error": "poll unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{"sha": build.SHA, "size": build.Size, "selfUpdate": standalone})
	}))
	mux.HandleFunc("GET /api/v1/internal/entry-probes/binary", gate(func(w http.ResponseWriter, r *http.Request) {
		build, ok := p.proberBuilds[r.URL.Query().Get("arch")]
		if !ok || build.SHA != r.URL.Query().Get("sha") {
			http.Error(w, "no such build", 404)
			return
		}
		data, err := build.read()
		if err != nil {
			http.Error(w, "build unavailable", 503)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-SHA256", build.SHA)
		w.Write(data)
	}))
}

func (p *Provisioner) proberUpdateError(ctx context.Context, slot, sha string, err error) {
	why := ""
	failed := ""
	if err != nil {
		why = err.Error()
		failed = sha
		applog.Error(ctx, "entry", "prober rollout failed", applog.F("slot", slot), applog.F("err", why))
	}
	if _, e := p.svc.pool.Exec(ctx, `UPDATE entry_vantages SET prober_update_error=$2,prober_failed_sha=CASE WHEN $3<>'' OR $2='' THEN $3 ELSE prober_failed_sha END WHERE slot=$1`, slot, why, failed); e != nil {
		applog.Error(ctx, "entry", "prober status write failed", applog.F("slot", slot), applog.F("err", e.Error()))
	}
}

// RollProbers runs before relay rollout: admission must speak this image's
// contract. Every machine is inspected via pinned SSH, even on a hub restart.
func (p *Provisioner) RollProbers(ctx context.Context) {
	slots, err := p.svc.VantageSlots(ctx)
	if err != nil {
		applog.Error(ctx, "entry", "prober inventory failed", applog.F("err", err.Error()))
		return
	}
	for _, v := range slots {
		if ctx.Err() != nil {
			return
		}
		if v.Status != VantageActive {
			continue
		}
		if v.Box == "" {
			applog.Error(ctx, "entry", "prober is not hub-managed via SSH; poll update required", applog.F("slot", v.Slot))
			continue
		}
		err = p.rollProber(ctx, v)
		if err != nil {
			p.proberUpdateError(context.WithoutCancel(ctx), v.Slot, "", err)
		}
	}
}
func (p *Provisioner) rollProber(ctx context.Context, v Vantage) error {
	j := &jobRun{p: p, ctx: ctx, box: v.Box}
	r, err := j.loginByKey()
	if err != nil {
		return fmt.Errorf("rollout login: %w", err)
	}
	defer r.Close()
	r, err = p.proberRemote(ctx, proberPrivileges(r, j.acc.user))
	if err != nil {
		return err
	}
	arch, err := j.arch(r)
	if err != nil {
		r.Close()
		return fmt.Errorf("rollout arch: %w", err)
	}
	build, ok := p.proberBuilds[arch]
	if !ok {
		r.Close()
		return fmt.Errorf("rollout artifact: no build for %s", arch)
	}
	out, err := r.Run(ctx, "sha256sum /usr/local/bin/nekko-entry-prober 2>/dev/null | cut -d' ' -f1", nil)
	r.Close()
	if err != nil {
		return fmt.Errorf("rollout sha: %w", err)
	}
	if strings.TrimSpace(out) == build.SHA {
		var healthy bool
		if err = p.svc.pool.QueryRow(ctx, `SELECT COALESCE(prober_sha=$2 AND prober_poll_at > now()-interval '3 minutes' AND last_admission_poll_at > now()-interval '3 minutes',false) FROM entry_vantages WHERE slot=$1`, v.Slot, build.SHA).Scan(&healthy); err != nil {
			return err
		}
		if healthy {
			var pending bool
			if err = p.svc.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entry_prober_deployments WHERE box=$1)`, v.Box).Scan(&pending); err != nil {
				return err
			}
			if !pending {
				return nil
			}
		}
	}
	var failed string
	if err = p.svc.pool.QueryRow(ctx, `SELECT prober_failed_sha FROM entry_vantages WHERE slot=$1`, v.Slot).Scan(&failed); err != nil {
		return err
	}
	if failed == build.SHA {
		return fmt.Errorf("rollout held: build %s already failed; explicit adopt/deploy required", shortSHA(failed))
	}
	id, err := p.deployProberJob(ctx, v.Box, v.Slot, "", trustNever)
	if err != nil {
		return fmt.Errorf("rollout deploy: %w", err)
	}
	return p.svc.WaitProberJob(ctx, id)
}

// waitProber requires both the new executable's self-reported digest and a
// fresh admission poll. A still-running old prober cannot confirm a rollout.
func (j *jobRun) waitProber(slot, sha string, started time.Time) error {
	ctx, cancel := context.WithTimeout(j.ctx, j.p.probeTimeout)
	defer cancel()
	for {
		var ready bool
		err := j.p.svc.pool.QueryRow(ctx, `SELECT COALESCE(prober_sha=$2 AND prober_poll_at >= $3 AND last_admission_poll_at >= $3, false) FROM entry_vantages WHERE slot=$1 AND status='active'`, slot, sha, started).Scan(&ready)
		if err == nil && ready {
			return nil
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("poll confirmation: %w", ctx.Err())
		case <-time.After(j.p.probePoll):
		}
	}
}

func (p *Provisioner) restoreProberToken(ctx context.Context, slot, token string, old []byte) error {
	hash := sha256.Sum256([]byte(token))
	_, err := p.svc.pool.Exec(ctx, `UPDATE entry_vantages SET token_hash=$3 WHERE slot=$1 AND token_hash=$2`, slot, hash[:], old)
	return err
}

// proberVantageStatus keeps silent/unmanaged/failed slots in the admin list.
type proberVantageStatus struct {
	Vantage
	ProberArch        string     `json:"proberArch"`
	ProberSHA         string     `json:"proberSha"`
	ProberPollAt      *time.Time `json:"proberPollAt"`
	ProberUpdateError string     `json:"proberUpdateError"`
}

func (s *Service) readProberStatus(ctx context.Context, v Vantage) (proberVantageStatus, error) {
	out := proberVantageStatus{Vantage: v}
	err := s.pool.QueryRow(ctx, `SELECT prober_arch,prober_sha,prober_poll_at,prober_update_error FROM entry_vantages WHERE slot=$1`, v.Slot).Scan(&out.ProberArch, &out.ProberSHA, &out.ProberPollAt, &out.ProberUpdateError)
	return out, err
}

func (p *Provisioner) proberSHAForSlot(ctx context.Context, slot string) string {
	var arch string
	if err := p.svc.pool.QueryRow(ctx, `SELECT prober_arch FROM entry_vantages WHERE slot=$1`, slot).Scan(&arch); err != nil {
		return ""
	}
	return p.proberBuilds[arch].SHA
}

const proberBackupScript = `set -eu
rm -f /usr/local/bin/nekko-entry-prober.new
if [ "${1:-0}" = 1 ] && [ -f /etc/nekko-entry-prober/update.pending ]; then exit 0; fi
rm -f /usr/local/bin/nekko-entry-prober.old /etc/nekko-entry-prober/prober.env.old
if [ -f /usr/local/bin/nekko-entry-prober ]; then
 cp -p /usr/local/bin/nekko-entry-prober /usr/local/bin/nekko-entry-prober.old
fi
if [ -f /etc/nekko-entry-prober/prober.env ]; then
 cp -p /etc/nekko-entry-prober/prober.env /etc/nekko-entry-prober/prober.env.old
fi
mkdir -p /etc/nekko-entry-prober
touch /etc/nekko-entry-prober/update.pending
`
const proberRollbackScript = `set -eu
systemctl stop nekko-entry-prober || true
if [ -f /usr/local/bin/nekko-entry-prober.old ]; then
 cp -p /usr/local/bin/nekko-entry-prober.old /usr/local/bin/nekko-entry-prober.rollback
 mv /usr/local/bin/nekko-entry-prober.rollback /usr/local/bin/nekko-entry-prober
fi
if [ -f /etc/nekko-entry-prober/prober.env.old ]; then
 cp -p /etc/nekko-entry-prober/prober.env.old /etc/nekko-entry-prober/prober.env
 systemctl restart nekko-entry-prober
fi
rm -f /usr/local/bin/nekko-entry-prober.new /etc/nekko-entry-prober/prober.env.new
`

// A shared Nekko server already has its own nekko-entry-prober. Hikkari's
// hikkari-entry-prober is a separate installation and must never overwrite it.
// Legacy Hikkari-only machines keep their existing nekko installation.
func (p *Provisioner) proberRemote(ctx context.Context, r Remote) (Remote, error) {
	out, err := r.Run(ctx, `if [ -f /etc/systemd/system/hikkari-entry-prober.service ]; then echo hikkari; elif [ -f /etc/nekko-entry-prober/prober.env ]; then echo legacy; fi`, nil)
	if err != nil {
		return nil, fmt.Errorf("prober installation: %w", err)
	}
	name := "nekko-entry-prober"
	if strings.TrimSpace(out) == "hikkari" {
		name = "hikkari-entry-prober"
	}
	// Refuse to overwrite a different project's installation even if the
	// operator accidentally points adopt at its server.
	check := `if [ -f /etc/` + name + `/prober.env ]; then grep -qxF ` + proberShellQuote("PROBE_INGEST_URL="+p.proberAPIURL+"/api/v1/internal/entry-probes") + ` /etc/` + name + `/prober.env; fi`
	if _, err = r.Run(ctx, check, nil); err != nil {
		return nil, fmt.Errorf("prober ownership: existing service belongs to another hub: %w", err)
	}
	if name == "hikkari-entry-prober" {
		return namedProberRemote{Remote: r}, nil
	}
	return r, nil
}
func proberShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

type namedProberRemote struct{ Remote }

func (r namedProberRemote) Run(ctx context.Context, cmd string, in io.Reader) (string, error) {
	cmd = strings.ReplaceAll(cmd, "nekko-entry-prober", "hikkari-entry-prober")
	// Replace only shell/env text, never executable bytes.
	if text, ok := in.(*strings.Reader); ok {
		b, err := io.ReadAll(text)
		if err != nil {
			return "", err
		}
		in = strings.NewReader(strings.ReplaceAll(string(b), "nekko-entry-prober", "hikkari-entry-prober"))
	}
	return r.Remote.Run(ctx, cmd, in)
}

type proberSudoRemote struct{ Remote }

func (r proberSudoRemote) Run(ctx context.Context, cmd string, in io.Reader) (string, error) {
	return r.Remote.Run(ctx, "sudo -n -- sh -c "+proberShellQuote(cmd), in)
}
func proberPrivileges(r Remote, user string) Remote {
	if user != "" && user != "root" {
		return proberSudoRemote{r}
	}
	return r
}

// beginProberToken persists the rollback identity before touching the machine.
// A hub killed mid-deploy resumes with the ORIGINAL token hash and .old files,
// rather than overwriting them with an unconfirmed build on its next attempt.
func (s *Service) beginProberToken(ctx context.Context, slot, box string) (string, []byte, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT 1 FROM entry_boxes WHERE name=$1 FOR UPDATE`, box); err != nil {
		return "", nil, false, err
	}
	if err = refuseFleetBox(ctx, tx, box); err != nil {
		return "", nil, false, err
	}
	var prior []byte
	if err = tx.QueryRow(ctx, `SELECT token_hash FROM entry_vantages WHERE slot=$1 AND box=$2 AND status='active' FOR UPDATE`, slot, box).Scan(&prior); err != nil {
		return "", nil, false, err
	}
	var resume bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entry_prober_deployments WHERE box=$1 AND slot=$2 AND (deployed_token_hash=$3 OR prior_token_hash=$3))`, box, slot, prior).Scan(&resume); err != nil {
		return "", nil, false, err
	}
	token, hash, err := newVantageToken()
	if err != nil {
		return "", nil, false, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO entry_prober_deployments(box,slot,prior_token_hash,deployed_token_hash) VALUES ($1,$2,$3,$4)
 ON CONFLICT (box) DO UPDATE SET slot=EXCLUDED.slot,
 prior_token_hash=CASE WHEN $5 THEN entry_prober_deployments.prior_token_hash ELSE EXCLUDED.prior_token_hash END,
 deployed_token_hash=EXCLUDED.deployed_token_hash RETURNING prior_token_hash`, box, slot, prior, hash, resume).Scan(&prior)
	if err != nil {
		return "", nil, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE entry_vantages SET token_hash=$3,updated_at=now() WHERE slot=$1 AND box=$2 AND status='active'`, slot, box, hash); err != nil {
		return "", nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", nil, false, err
	}
	return token, prior, resume, nil
}

func (p *Provisioner) finishProberToken(ctx context.Context, slot, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := p.svc.pool.Exec(ctx, `DELETE FROM entry_prober_deployments WHERE slot=$1 AND deployed_token_hash=$2`, slot, hash[:])
	return err
}

func resumeArg(resume bool) string {
	if resume {
		return "1"
	}
	return "0"
}
