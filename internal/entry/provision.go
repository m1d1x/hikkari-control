package entry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/sshexec"
)

// The hub provisions relay boxes itself (roadmap 42 П6.15 Ф3). The operator
// gives an address and the provider's root password; one job then:
//
//  1. logs in — with the hub's key if it is already on the box (a re-run, or
//     the key pasted into the provider's panel at order time), else with the
//     password, puts the hub's key in, and logs in again with the key to prove
//     it works BEFORE anything is locked down;
//  2. hardens: password login off (the drop-in sorts before cloud-init's
//     50-, whose PasswordAuthentication yes wins otherwise — sshd keeps the
//     first value), ufw with ssh and the relay ports;
//  3. mints a relay if the box has none and deploys every relay that is not
//     dead, from the binaries in the hub's image;
//  4. asks a vantage for an admission probe: the box enters the pool only if
//     every relay is clean — the censor may cut an address from birth, and a
//     dirty address goes back to the provider, not in front of users;
//  5. signs: pending relays are admitted, alive ones re-signed.
//
// Running it again is the update: same names, same identities, newer binary,
// fresh records.

// DefaultArtifactDir is where the hub image carries the relay binaries,
// linux-<arch>/{nekko-edge,nekko-entry-prober} (Dockerfile).
const DefaultArtifactDir = "/usr/local/share/nekko/relay"

// Remote is one logged-in ssh connection (sshexec.Client in production).
type Remote interface {
	Run(ctx context.Context, cmd string, stdin io.Reader) (string, error)
	HostKey() string
	Close() error
}

// Dialer opens a Remote.
type Dialer func(ctx context.Context, t sshexec.Target, auth ...ssh.AuthMethod) (Remote, error)

// SSHDialer is the production Dialer.
func SSHDialer(ctx context.Context, t sshexec.Target, auth ...ssh.AuthMethod) (Remote, error) {
	return sshexec.Dial(ctx, t, auth...)
}

// ErrHostKeyUnknown: the box has no host key on record, and the login asking
// is not one that may take the first key it is shown (see jobRun.firstUse).
// A refusal like any other (ErrRejected): nothing was done to the box.
var ErrHostKeyUnknown = fmt.Errorf("%w: no host key on record", ErrRejected)

// firstUse says whether a job's login may take the host key a box presents
// when none is on record — trust on first use — and when.
type firstUse int

const (
	// trustNever: everything automatic that works on a box already in the
	// fleet — the grid, rollouts, repairs, wipes. After "forget the host key"
	// the first thing to reach :22 must not be a background job that would
	// record a man in the middle's key and hand him the relays' env (audit 42
	// С6).
	trustNever firstUse = iota
	// trustIfNeverMet: the hub standing up a machine it rented itself (a fresh
	// order, an idle one off the shelf). It may meet such a machine for the
	// first time, but not one whose key the operator forgot.
	trustIfNeverMet
	// trustOperator: the operator's own provisioning job. Forgetting a key is
	// the operator saying "I reinstalled it, I will run the job" — this one.
	trustOperator
)

// dialBox is the only way into a box. The target names the box, and whatever
// the dialer or the box says back reaches the caller with the address taken
// out of its text: errors end up in the hub's log and in the job's, and the
// address is the secret the hub keeps sealed (migration 039, audit 42 С3).
// sshexec already says nothing of the address; this holds for any Dialer, and
// for what a command on the box prints into its error.
func (p *Provisioner) dialBox(ctx context.Context, box string, acc boxAccess, auth ...ssh.AuthMethod) (Remote, error) {
	t := sshexec.Target{Addr: net.JoinHostPort(acc.address, "22"), Name: box, User: acc.user, HostKey: acc.hostKey}
	r, err := p.dial(ctx, t, auth...)
	if err != nil {
		return nil, sshexec.Redact(err, acc.address, box)
	}
	return sealedRemote{Remote: r, host: acc.address, box: box}, nil
}

// sealedRemote is a Remote whose errors never quote the box's address.
type sealedRemote struct {
	Remote
	host, box string
}

func (r sealedRemote) Run(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	out, err := r.Remote.Run(ctx, cmd, stdin)
	return out, sshexec.Redact(err, r.host, r.box)
}

// MediaSource says where a call's media arrives: the SFU media addresses a
// relay may forward datagrams to (the shard registry, sfu_nodes.media_*). A
// voice relay asks for its region's; a stream relay, which no region owns,
// asks with region "" for every shard's (its TCP rung, voice plan Ф5). Read
// at every deploy, so a re-run of the job is what moves a relay onto new
// shards.
type MediaSource interface {
	Media(ctx context.Context, region string) ([]MediaTarget, error)
}

// StreamSource supplies the current named endpoints for reliable streams.
// Read at deploy time so SFU registration changes reach new relay configs.
type StreamSource interface {
	Streams(ctx context.Context) ([]string, error)
}

// Provisioner runs provisioning jobs.
type Provisioner struct {
	svc          *Service
	dial         Dialer
	media        MediaSource          // nil: voice boxes cannot be deployed, stream boxes carry no calls
	streams      StreamSource         // current SFU signaling destinations
	builds       map[string]EdgeBuild // by arch, hashed once at start
	upstream     string               // what relays forward to: EDGE_UPSTREAMS, see Upstreams
	probeTimeout time.Duration
	probePoll    time.Duration
	drainPoll    time.Duration
	jobTimeout   time.Duration
	// bootWait is how long a machine the hub just ordered has to let it in
	// over ssh (hosting.go, bootWait).
	bootWait time.Duration
	// dnsResolvers are the public resolvers an XHTTP box's domain must resolve
	// to the box on before anything is deployed (Ф6). Empty skips the wait (in
	// tests, or on a hub that has no public DNS egress).
	dnsResolvers []string
	dnsPoll      time.Duration
	// pulse is where a relay reports its own counters (П9.3). Empty leaves
	// the relays silent, which is what a hub without analytics wants: the
	// edge simply does not start its reporting loop.
	pulseURL   string
	pulseToken string
	// proberBuilds are the linux-<arch>/nekko-entry-prober binaries in the
	// hub's image; proberAPIURL renders the prober's env when a box is put
	// into a vantage slot (К3′ / П9.12). Its token is the slot's own, issued
	// at deploy time.
	proberBuilds map[string]EdgeBuild
	proberAPIURL string
}

// SetPulse tells the provisioner where relays should report; token is the
// hub's EDGE_PULSE_TOKEN, and each box is given its own [PulseToken] of it,
// never the secret itself. Called once at startup; set after NewProvisioner so a hub without analytics needs to say
// nothing. Takes effect on the next provisioning of a box — the env lives on
// the box, and this is what writes it.
func (p *Provisioner) SetPulse(url, token string) {
	p.pulseURL, p.pulseToken = url, token
}

// SetProber tells the provisioner how a vantage prober reaches this hub
// (roadmap 42 К3′ / П9.12). apiURL is the public base URL of the API
// (config.Default().APIURL()).
func (p *Provisioner) SetProber(apiURL string) {
	p.proberAPIURL = strings.TrimRight(apiURL, "/")
}

// SetStreamSource adds live signaling destinations to the static HTTP hosts.
// Call before starting provisioning jobs.
func (p *Provisioner) SetStreamSource(source StreamSource) {
	p.streams = source
}

// CanDeployProber reports whether this hub knows its own address and has at
// least one compiled nekko-entry-prober binary to ship over ssh.
func (p *Provisioner) CanDeployProber() bool {
	return p != nil && p.proberAPIURL != "" && len(p.proberBuilds) > 0
}

// NewProvisioner builds the runner. upstream is what a stream relay forwards
// to, as Upstreams renders it (the gateway and the client's HTTP hosts);
// artifacts the directory of relay binaries, hashed here once: they are the
// version this hub deploys (П6.14).
func NewProvisioner(svc *Service, dial Dialer, artifacts, upstream string, media MediaSource) *Provisioner {
	return &Provisioner{
		svc: svc, dial: dial, media: media,
		builds:       loadEdgeBuilds(artifacts),
		proberBuilds: loadProberBuilds(artifacts),
		upstream:     upstream,
		probeTimeout: 5 * time.Minute, probePoll: 2 * time.Second, drainPoll: drainPoll,
		jobTimeout:   30 * time.Minute,
		bootWait:     bootWait,
		dnsResolvers: []string{"1.1.1.1:53", "8.8.8.8:53"},
		dnsPoll:      5 * time.Second,
	}
}

// NewBox is a machine the operator just rented.
type NewBox struct {
	BoxSpec
	// Password is the provider's root password, used once to put the hub's
	// key on the box and never stored. Empty when the hub's key was added in
	// the provider's panel at order time.
	Password string
}

// CreateAndProvision records the box and starts its first job.
func (p *Provisioner) CreateAndProvision(ctx context.Context, b NewBox, adminID string) (uuid.UUID, error) {
	if err := p.svc.CreateBox(ctx, b.BoxSpec); err != nil {
		return uuid.Nil, err
	}
	return p.Provision(ctx, b.Name, b.Password, adminID)
}

// Provision starts a job on a recorded box and returns its id at once; the
// job runs on its own and reports through its row and log.
func (p *Provisioner) Provision(ctx context.Context, box, password, adminID string) (uuid.UUID, error) {
	if acc, err := p.svc.boxAccess(ctx, box); err != nil {
		return uuid.Nil, err
	} else if acc.retired {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	id, err := p.svc.startJob(ctx, box, adminID)
	if err != nil {
		return uuid.Nil, err
	}
	p.svc.spawn(func(ctx context.Context) { p.run(ctx, id, box, password) })
	return id, nil
}

// Reprovision runs an ordinary provisioning job on a box already in the pool,
// in this goroutine, and returns its verdict (roadmap 42 П9.6).
//
// It is what the repairs that end in "deploy the box again" are made of: the
// domain replacement of Ф6 claims or buys a fresh name inside provision(),
// mints a relay if the box has none left, deploys and admits. Synchronous,
// unlike [Provisioner.Provision], because a repair has to be able to say
// whether it worked — the console's live job log is the same either way.
func (p *Provisioner) Reprovision(ctx context.Context, box string) error {
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return err
	}
	if acc.retired {
		return fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	return p.execJob(ctx, JobProvision, box, uuid.Nil, func(j *jobRun) error {
		return j.provision("")
	})
}

// RotateReality gives every relay of a Reality box new credentials without
// touching the machine's address (П9.6, playbook rotate_reality): fresh keys
// in the hub, the env rewritten and the units restarted on the box, admission
// re-probed, records re-signed, holders nudged. Returns how many relays were
// rotated.
//
// Synchronous, for the same reason as Reprovision. Refuses a voice or XHTTP
// box: they have no Reality half, and a repair that did nothing must not
// report success.
func (p *Provisioner) RotateReality(ctx context.Context, box string) (int, error) {
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return 0, err
	}
	if acc.retired {
		return 0, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	if acc.proto != ProtoVlessRealityVision {
		return 0, fmt.Errorf("%w: машина %s говорит %s, ключей reality у неё нет", ErrRejected, box, acc.proto)
	}
	rotated := 0
	err = p.execJob(ctx, JobRekey, box, uuid.Nil, func(j *jobRun) error {
		n, err := j.rotateReality()
		rotated = n
		return err
	})
	return rotated, err
}

// rotateReality is the job body: re-key in the hub first, then make the box
// agree with it.
//
// The order is deliberate. The hub is the source of truth for an identity
// (entry_relays.identity_ct), so it is written first and the box is brought to
// it; a box re-keyed first would, on a failure in between, be running
// credentials the hub cannot sign records for — and the hub cannot read them
// back off the box to recover.
func (j *jobRun) rotateReality() (int, error) {
	svc := j.p.svc
	j.step("login")
	r, err := j.loginByKey()
	if err != nil {
		return 0, err
	}
	defer r.Close()

	relays, err := j.liveRelays()
	if err != nil {
		return 0, err
	}
	if len(relays) == 0 {
		return 0, fmt.Errorf("%w: на машине %s нет живых релеев, менять ключи нечему", ErrRejected, j.box)
	}

	j.step("rekey")
	for _, rel := range relays {
		fresh, err := rel.id.rotatedReality()
		if err != nil {
			return 0, fmt.Errorf("релей %s: %w", rel.name, err)
		}
		if err := svc.setRelayIdentity(j.ctx, rel.name, fresh); err != nil {
			return 0, fmt.Errorf("новые ключи %s: %w", rel.name, err)
		}
		j.log("%s: новые ключ reality, short id и uuid", rel.name)
	}

	// Re-read: install renders the env from the identity, and it must render
	// the new one.
	if relays, err = j.liveRelays(); err != nil {
		return 0, err
	}
	for _, rel := range relays {
		j.step("deploy " + rel.name)
		if err := j.install(r, rel); err != nil {
			return 0, fmt.Errorf("разворот %s: %w", rel.name, err)
		}
	}

	j.step("probe")
	if err := j.probe(relays); err != nil {
		return 0, err
	}

	j.step("sign")
	for _, rel := range relays {
		if !recorded(rel.status) {
			continue
		}
		if err := svc.Resign(j.ctx, rel.name); err != nil {
			return 0, fmt.Errorf("переподпись %s: %w", rel.name, err)
		}
	}
	// The records the holders carry name the old keys, so they are now
	// useless: the nudge is not an optimisation here, it is the difference
	// between a client reconnecting in seconds and one waiting out its
	// record's lifetime (П6.11).
	if err := svc.nudgeHolders(j.ctx, relayNames(relays)); err != nil {
		j.log("предупредить держателей не удалось: %v — они дотянут до следующего фетча", err)
	}
	return len(relays), nil
}

// Wipe starts the job that takes the relay off a box retired by
// [Service.Retire] (П6.10): every
// nekko-edge instance stopped and disabled, its env (the relay's private keys)
// and binary deleted, and the hub's ssh key removed last — after it the hub
// cannot log in again, which is the point of handing a machine back.
func (p *Provisioner) Wipe(ctx context.Context, box, adminID string) (uuid.UUID, error) {
	if acc, err := p.svc.boxAccess(ctx, box); err != nil {
		return uuid.Nil, err
	} else if !acc.retired {
		return uuid.Nil, fmt.Errorf("%w: box %s is in service; retire it first", ErrRejected, box)
	}
	id, err := p.svc.startJobOf(ctx, JobRetire, box, adminID, uuid.Nil)
	if err != nil {
		return uuid.Nil, err
	}
	p.svc.spawn(func(ctx context.Context) {
		jctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
		defer cancel()
		j := &jobRun{p: p, ctx: jctx, id: id, box: box}
		j.finish(j.wipe(), "готово: "+box+" стёрт, ключ хаба убран")
	})
	return id, nil
}

func (j *jobRun) wipe() error {
	j.step("login")
	r, err := j.loginByKey()
	if err != nil {
		return fmt.Errorf("%w. релеи уже сняты с пула; если машину забрал провайдер, стирать нечего", err)
	}
	defer r.Close()
	_, hubPub, err := j.p.svc.HubSSHKey(j.ctx)
	if err != nil {
		return fmt.Errorf("ssh-ключ хаба: %w", err)
	}
	j.step("wipe")
	// The key goes in as an argument: it is public, and base64 has no quote.
	out, err := r.Run(j.ctx, "bash -s -- '"+hubPub+"'", strings.NewReader(wipeScript))
	if err != nil {
		return fmt.Errorf("стирание: %w", err)
	}
	// The count is read, not echoed: the job log carries the hub's own
	// words, never what the box printed (migration 043).
	if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
		j.log("остановлено экземпляров релея: %d", n)
	}
	j.log("env с ключами релеев, бинарь и unit удалены, ключ хаба убран из authorized_keys")
	return nil
}

// DeployProber ships nekko-entry-prober over ssh to `box`, writes its
// /etc/nekko-entry-prober/prober.env with the slot's own token, and starts
// nekko-entry-prober.service (roadmap 42 К3′ / П9.12).
//
// The slot must be active and hold this box, and the box must carry no
// relays (vantages.go: a vantage on a machine of the fleet is forbidden). The
// token is issued here, and the one the slot had stops working: the machine
// that receives it is the slot's voice from now on.
func (p *Provisioner) DeployProber(ctx context.Context, box, slot, adminID string) (uuid.UUID, error) {
	if !p.CanDeployProber() {
		return uuid.Nil, fmt.Errorf("%w: хаб не знает своего адреса или в образе нет бинаря пробника", ErrRejected)
	}
	if acc, err := p.svc.boxAccess(ctx, box); err != nil {
		return uuid.Nil, err
	} else if acc.retired {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	// The token is issued once the job holds the box (audit 42 recheck НН2):
	// issued first, a deploy refused for a busy box had already voided the
	// working prober's token and delivered the new one nowhere. What would
	// refuse the issue is checked before the job, so a refused deploy leaves
	// no prober job behind on the box.
	if err := p.svc.mayIssueBoxVantageToken(ctx, slot, box); err != nil {
		return uuid.Nil, err
	}
	id, err := p.svc.startJobOf(ctx, JobProber, box, adminID, uuid.Nil)
	if err != nil {
		return uuid.Nil, err
	}
	token, err := p.svc.issueBoxVantageToken(ctx, slot, box)
	if err != nil {
		_ = p.svc.finishJob(context.WithoutCancel(ctx), id, err)
		return uuid.Nil, err
	}
	p.svc.spawn(func(ctx context.Context) {
		jctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
		defer cancel()
		// Only the operator starts a prober deploy (admin_vantages.go), onto a
		// machine outside the fleet: it is the operator's own job, like
		// provisioning, and may meet the machine's key for the first time.
		j := &jobRun{p: p, ctx: jctx, id: id, box: box, firstUse: trustOperator}
		j.finish(j.deployProber(slot, token), "готово: пробник слота "+strings.ToUpper(slot)+" запущен на "+box)
	})
	return id, nil
}

func (j *jobRun) deployProber(slot, token string) error {
	j.step("login")
	r, err := j.loginByKey()
	if err != nil {
		return err
	}
	defer r.Close()

	j.step("deploy")
	arch, err := j.arch(r)
	if err != nil {
		return err
	}
	pbuild, ok := j.p.proberBuilds[arch]
	if !ok {
		return fmt.Errorf("в образе хаба нет бинаря пробника под %s", arch)
	}

	out, _ := r.Run(j.ctx, "sha256sum /usr/local/bin/nekko-entry-prober 2>/dev/null | cut -d' ' -f1", nil)
	if strings.TrimSpace(out) != pbuild.SHA {
		binBytes, err := pbuild.read()
		if err != nil {
			return err
		}
		if _, err := r.Run(j.ctx,
			"cat > /usr/local/bin/nekko-entry-prober.new && chmod 755 /usr/local/bin/nekko-entry-prober.new",
			bytes.NewReader(binBytes),
		); err != nil {
			return fmt.Errorf("загрузка бинаря пробника: %w", err)
		}
		j.log("бинарь пробника %s… загружен (%d МБ)", pbuild.SHA[:12], len(binBytes)>>20)
	} else {
		j.log("бинарь пробника %s… уже на месте", pbuild.SHA[:12])
	}

	// Ensure an Xray engine is present on the box: if /usr/local/bin/xray is
	// not installed and /usr/local/bin/nekko-edge is missing, upload nekko-edge
	// (which supports `nekko-edge run -c <config.json>`).
	hasEngine, _ := r.Run(j.ctx,
		"if [ -x /usr/local/bin/xray ]; then echo /usr/local/bin/xray; elif [ -x /usr/local/bin/nekko-edge ]; then echo /usr/local/bin/nekko-edge; fi",
		nil)
	xrayBin := strings.TrimSpace(hasEngine)
	if xrayBin == "" {
		if ebuild, ok := j.p.builds[arch]; ok {
			if ebytes, err := ebuild.read(); err == nil {
				if _, err := r.Run(j.ctx,
					"cat > /usr/local/bin/nekko-edge && chmod 755 /usr/local/bin/nekko-edge",
					bytes.NewReader(ebytes),
				); err == nil {
					xrayBin = "/usr/local/bin/nekko-edge"
					j.log("движок туннеля nekko-edge (%s…) установлен", ebuild.SHA[:12])
				}
			}
		}
	}
	if xrayBin == "" {
		xrayBin = "/usr/local/bin/nekko-edge"
	}

	if _, err := r.Run(j.ctx, "cat > /etc/systemd/system/nekko-entry-prober.service", strings.NewReader(proberUnit)); err != nil {
		return fmt.Errorf("unit-файл пробника: %w", err)
	}

	if _, err := r.Run(j.ctx,
		"umask 077 && mkdir -p /etc/nekko-entry-prober && cat > /etc/nekko-entry-prober/prober.env.new",
		strings.NewReader(proberEnv(j.p.proberAPIURL, slot, token, xrayBin)),
	); err != nil {
		return fmt.Errorf("env пробника: %w", err)
	}
	if _, err := r.Run(j.ctx, "bash -s", strings.NewReader(proberSwapScript)); err != nil {
		return fmt.Errorf("запуск пробника: %w", err)
	}
	j.log("nekko-entry-prober активен (слот %s, движок %s)", strings.ToUpper(slot), xrayBin)
	return nil
}

// StopProber disables nekko-entry-prober on a box that was removed from its
// vantage slot. Best-effort in the background so a dead machine does not block
// reassigning the slot in Postgres.
func (p *Provisioner) StopProber(box string) {
	if p == nil || box == "" {
		return
	}
	p.svc.spawn(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		j := &jobRun{p: p, ctx: ctx, box: box}
		r, err := j.loginByKey()
		if err != nil {
			return
		}
		defer r.Close()
		_, _ = r.Run(ctx, `systemctl disable --now nekko-entry-prober >/dev/null 2>&1 || true
rm -rf /etc/nekko-entry-prober /usr/local/bin/nekko-entry-prober /usr/local/bin/nekko-entry-prober.new /etc/systemd/system/nekko-entry-prober.service
systemctl daemon-reload >/dev/null 2>&1 || true`, nil)
	})
}

const proberUnit = `# Installed by the nekko hub (roadmap 42 К3′ / П9.12).
[Unit]
Description=Nekko entry-relay prober
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/nekko-entry-prober/prober.env
ExecStart=/usr/local/bin/nekko-entry-prober
Restart=always
RestartSec=5
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`

const proberSwapScript = `set -eu
bin=/usr/local/bin/nekko-entry-prober
[ -f "$bin.new" ] && mv "$bin.new" "$bin"
chmod 755 "$bin"
chmod 600 /etc/nekko-entry-prober/prober.env.new
rm -f /etc/nekko-entry-prober/edges.json
mv /etc/nekko-entry-prober/prober.env.new /etc/nekko-entry-prober/prober.env
systemctl daemon-reload
systemctl enable nekko-entry-prober >/dev/null 2>&1
systemctl restart nekko-entry-prober
for _ in $(seq 1 20); do
	if systemctl is-active --quiet nekko-entry-prober; then
		exit 0
	fi
	sleep 0.5
done
journalctl -u nekko-entry-prober -n 20 --no-pager >&2
exit 1
`

// wipeScript takes nekko-edge off a box: $1 is the hub's public key, removed
// last. Prints how many instances it stopped.
const wipeScript = `set -eu
# One env file per relay instance (install writes /etc/nekko-edge/<name>.env).
n=0
for f in /etc/nekko-edge/*.env; do
	[ -e "$f" ] || continue
	systemctl disable --now "nekko-edge@$(basename "$f" .env)" >/dev/null 2>&1 || true
	n=$((n + 1))
done
rm -rf /etc/nekko-edge
rm -f /usr/local/bin/nekko-edge /usr/local/bin/nekko-edge.new /usr/local/bin/nekko-edge.prev
rm -f /etc/systemd/system/nekko-edge@.service
systemctl daemon-reload
echo "$n"
if [ -f ~/.ssh/authorized_keys ]; then
	grep -vxF "$1" ~/.ssh/authorized_keys > ~/.ssh/authorized_keys.nekko || true
	mv ~/.ssh/authorized_keys.nekko ~/.ssh/authorized_keys
fi
`

// jobRun is one job's state while it runs.
type jobRun struct {
	p   *Provisioner
	ctx context.Context
	id  uuid.UUID
	box string
	// acc is the box as the job found it at login: its protocol decides the
	// firewall, the listening check, the drain and the relay's env.
	acc boxAccess
	// firstUse is whether this job's login may take the host key of a box
	// with none on record. The zero value refuses.
	firstUse firstUse
}

func (j *jobRun) step(name string) {
	if j.id == uuid.Nil {
		return
	}
	if err := j.p.svc.jobStep(j.ctx, j.id, name); err != nil {
		applog.Error(j.ctx, "entry", "job step not recorded", applog.F("job", j.id.String()), applog.F("err", err.Error()))
	}
	j.log("шаг: %s", name)
}

// log writes a line to the job's log; a run with no job (the rollout's
// survey of box versions) writes to the hub's own log instead.
func (j *jobRun) log(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if j.id == uuid.Nil {
		applog.Info(j.ctx, "entry", line, applog.F("box", j.box))
		return
	}
	if err := j.p.svc.jobLog(j.ctx, j.id, line); err != nil {
		applog.Error(j.ctx, "entry", "job log line not recorded", applog.F("job", j.id.String()), applog.F("err", err.Error()))
	}
}

func (p *Provisioner) run(ctx context.Context, id uuid.UUID, box, password string) {
	ctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
	defer cancel()
	j := &jobRun{p: p, ctx: ctx, id: id, box: box, firstUse: trustOperator}
	err := j.provision(password)
	j.finish(err, "готово: "+box+" в пуле")
}

// finish closes the job with its verdict. The job's own context may be spent;
// the verdict still has to land.
func (j *jobRun) finish(err error, done string) {
	if err != nil {
		j.log("ошибка: %v", err)
	} else {
		j.log("%s", done)
	}
	fin, cancelFin := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFin()
	if ferr := j.p.svc.finishJob(fin, j.id, err); ferr != nil {
		applog.Error(fin, "entry", "job verdict not recorded", applog.F("job", j.id.String()), applog.F("err", ferr.Error()))
	}
}

func (j *jobRun) provision(password string) error {
	svc := j.p.svc
	acc, err := svc.boxAccess(j.ctx, j.box)
	if err != nil {
		return err
	}
	j.acc = acc
	if acc.user != "root" {
		return fmt.Errorf("пока поддерживается только root, а у машины %s логин %s", j.box, acc.user)
	}
	hubKey, hubPub, err := svc.HubSSHKey(j.ctx)
	if err != nil {
		return fmt.Errorf("ssh-ключ хаба: %w", err)
	}

	j.step("login")
	r, err := j.login(acc, hubKey, hubPub, password)
	if err != nil {
		return err
	}
	defer r.Close()

	// An XHTTP box needs its domain live before anything is deployed: relays
	// are minted from the box's sealed domain, and the edge orders its
	// certificate against it at start (Ф6).
	if acc.proto == ProtoVlessXhttp {
		j.step("domain")
		if err := j.ensureDomain(); err != nil {
			return err
		}
	}

	j.step("relays")
	relays, err := j.ensureRelays()
	if err != nil {
		return err
	}
	j.log("релеи: %s", strings.Join(relayNames(relays), ", "))

	j.step("harden")
	if err := j.harden(r, relays); err != nil {
		return err
	}

	arch, err := j.arch(r)
	if err != nil {
		return err
	}
	build, ok := j.p.builds[arch]
	if !ok {
		return fmt.Errorf("в образе хаба нет бинаря релея под %s", arch)
	}
	for _, rel := range relays {
		j.step("deploy " + rel.name)
		if err := j.deploy(r, build, rel); err != nil {
			return fmt.Errorf("разворот %s: %w", rel.name, err)
		}
	}

	j.step("probe")
	if err := j.probe(relays); err != nil {
		return err
	}

	j.step("sign")
	for _, rel := range relays {
		switch rel.status {
		case "pending":
			if err := svc.Admit(j.ctx, rel.name); err != nil {
				return fmt.Errorf("допуск %s: %w", rel.name, err)
			}
			j.log("%s допущен в пул", rel.name)
		case "alive":
			if err := svc.Resign(j.ctx, rel.name); err != nil {
				return fmt.Errorf("переподпись %s: %w", rel.name, err)
			}
			j.log("%s переподписан", rel.name)
		}
	}
	return nil
}

// ensureDomain gives an XHTTP box a domain and waits until public resolvers
// show it pointing at the box, before anything is deployed. A box that already
// carries a domain keeps it; only the resolver wait runs. A fresh box with
// none gets one from the pool, or a purchase when the pool is dry (Ф6).
func (j *jobRun) ensureDomain() error {
	domain := j.acc.domain
	if domain == "" {
		d, bought, err := j.p.svc.AcquireDomain(j.ctx, j.box, j.acc.address)
		if err != nil {
			return fmt.Errorf("домен: %w", err)
		}
		// The address stays out of the log (migration 039): the A record
		// points at "this box", which is what the line needs to say.
		if bought {
			j.log("куплен домен %s, A-запись → %s", d, j.box)
		} else {
			j.log("домен из пула: %s, A-запись → %s", d, j.box)
		}
		j.acc.domain, domain = d, d
	}
	if len(j.p.dnsResolvers) == 0 {
		return nil
	}
	j.log("жду, пока публичные резолверы отдают %s → %s", domain, j.box)
	if err := j.waitDNS(domain, j.acc.address); err != nil {
		return err
	}
	j.log("резолверы отдают %s", domain)
	return nil
}

// waitDNS blocks until every configured public resolver answers domain with
// the box's ip, polling on that condition (not a fixed sleep) until the job's
// deadline. A fresh A record on a cheap TLD usually goes live in under a
// minute; requiring every resolver to agree guards against serving a name only
// half the internet can reach yet.
func (j *jobRun) waitDNS(domain, ip string) error {
	for {
		agreed, lastErr := j.dnsAgrees(domain, ip)
		if agreed {
			return nil
		}
		select {
		case <-j.ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("домен %s не зарезолвился в адрес %s до таймаута: %w", domain, j.box, lastErr)
			}
			return fmt.Errorf("домен %s не зарезолвился в адрес %s до таймаута", domain, j.box)
		case <-time.After(j.p.dnsPoll):
		}
	}
}

// dnsAgrees reports whether every configured resolver maps domain to ip.
func (j *jobRun) dnsAgrees(domain, ip string) (bool, error) {
	want := net.ParseIP(ip)
	var lastErr error
	for _, rs := range j.p.dnsResolvers {
		ok, err := resolvesTo(j.ctx, rs, domain, want)
		if err != nil {
			lastErr = err
		}
		if !ok {
			return false, lastErr
		}
	}
	return true, lastErr
}

// resolvesTo asks one public resolver for domain's addresses and reports
// whether want is among them.
func resolvesTo(ctx context.Context, resolver, domain string, want net.IP) (bool, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", resolver)
		},
	}
	lctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	addrs, err := r.LookupHost(lctx, domain)
	if err != nil {
		return false, err
	}
	for _, a := range addrs {
		if net.ParseIP(a).Equal(want) {
			return true, nil
		}
	}
	return false, nil
}

// login opens the box with the hub's key, putting the key in with the
// password first when it is not there yet. A box with no host key on record
// is opened only by a job allowed to trust on first use (firstUse).
func (j *jobRun) login(acc boxAccess, hubKey ssh.Signer, hubPub, password string) (Remote, error) {
	if acc.hostKey == "" && !j.mayTakeFirstKey(acc) {
		return nil, fmt.Errorf("%w: ключ хоста машины %s не записан, а принять ключ при первом входе может только разворот, запущенный оператором. запусти разворот", ErrHostKeyUnknown, j.box)
	}
	r, err := j.p.dialBox(j.ctx, j.box, acc, ssh.PublicKeys(hubKey))
	switch {
	case err == nil:
		j.log("вошёл ключом хаба")
		if err := j.rememberHostKey(acc, r); err != nil {
			r.Close()
			return nil, err
		}
		return r, nil
	case errors.Is(err, sshexec.ErrHostKeyChanged):
		return nil, fmt.Errorf("машина %s предъявила другой ключ хоста, чем записан: её переустановили или кто-то посередине. если переустанавливали, забудь ключ хоста и запусти разворот: %w", j.box, sshexec.ErrHostKeyChanged)
	case !errors.Is(err, sshexec.ErrAuth):
		return nil, err
	case password == "":
		return nil, fmt.Errorf("ключа хаба на машине нет, а пароль не дан. вставь ключ хаба в панели провайдера или дай пароль root")
	}

	r, err = j.p.dialBox(j.ctx, j.box, acc, ssh.Password(password))
	if err != nil {
		if errors.Is(err, sshexec.ErrAuth) {
			return nil, fmt.Errorf("пароль от провайдера не подошёл")
		}
		return nil, err
	}
	j.log("вошёл паролем")
	if err := j.rememberHostKey(acc, r); err != nil {
		r.Close()
		return nil, err
	}
	acc.hostKey = r.HostKey()
	// The key goes in as an argument: it is public, and base64 has no quote.
	_, err = r.Run(j.ctx, `set -eu
umask 077
mkdir -p ~/.ssh
touch ~/.ssh/authorized_keys
grep -qxF '`+hubPub+`' ~/.ssh/authorized_keys || printf '%s\n' '`+hubPub+`' >> ~/.ssh/authorized_keys`, nil)
	r.Close()
	if err != nil {
		return nil, fmt.Errorf("установка ключа хаба: %w", err)
	}
	// Prove the key works before password login is switched off.
	r, err = j.p.dialBox(j.ctx, j.box, acc, ssh.PublicKeys(hubKey))
	if err != nil {
		return nil, fmt.Errorf("ключ хаба установлен, но не открывает машину: %w", err)
	}
	j.log("ключ хаба установлен, вход по нему работает")
	return r, nil
}

// mayTakeFirstKey says whether this job may record the key a box with none
// on record presents.
func (j *jobRun) mayTakeFirstKey(acc boxAccess) bool {
	switch j.firstUse {
	case trustOperator:
		return true
	case trustIfNeverMet:
		return !acc.hostKeyForgotten
	}
	return false
}

func (j *jobRun) rememberHostKey(acc boxAccess, r Remote) error {
	if acc.hostKey != "" {
		return nil
	}
	if err := j.p.svc.setHostKey(j.ctx, j.box, r.HostKey()); err != nil {
		return fmt.Errorf("запись ключа хоста: %w", err)
	}
	j.log("ключ хоста записан: %s", fingerprint(r.HostKey()))
	return nil
}

// provRelay is a relay of the box this job deploys.
type provRelay struct {
	name   string
	status string
	id     Identity
}

func relayNames(rs []provRelay) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.name
	}
	return out
}

// ensureRelays returns the box's relays that are not dead, minting new ones
// until the box holds as many as its protocol keeps (relayTarget).
func (j *jobRun) ensureRelays() ([]provRelay, error) {
	relays, err := j.liveRelays()
	if err != nil {
		return nil, err
	}
	for len(relays) < relayTarget(j.acc.proto) {
		name, err := j.p.svc.AddRelay(j.ctx, j.box, KindPersonal)
		if err != nil {
			return nil, err
		}
		j.log("создан релей %s", name)
		if relays, err = j.liveRelays(); err != nil {
			return nil, err
		}
	}
	return relays, nil
}

// liveRelays returns the box's relays that are not dead, identities opened.
func (j *jobRun) liveRelays() ([]provRelay, error) {
	return j.p.svc.relayIdentities(j.ctx, j.box)
}

// relayIdentities returns the box's relays that are not dead, identities
// opened.
func (s *Service) relayIdentities(ctx context.Context, box string) ([]provRelay, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only
	rows, err := s.repo.relaysOnBox(ctx, tx, box)
	if err != nil {
		return nil, err
	}
	var out []provRelay
	for _, r := range rows {
		if r.status == "dead" {
			continue
		}
		id, err := openIdentity(r.identityCT)
		if err != nil {
			return nil, fmt.Errorf("релей %s: %w", r.name, err)
		}
		out = append(out, provRelay{name: r.name, status: r.status, id: id})
	}
	return out, nil
}

func (j *jobRun) harden(r Remote, relays []provRelay) error {
	ports := make([]string, 0, len(relays))
	for _, rel := range relays {
		ports = append(ports, strconv.Itoa(rel.id.Port)+"/"+j.transport())
	}
	sort.Strings(ports)
	out, err := r.Run(j.ctx, "bash -s -- "+strings.Join(ports, " "), strings.NewReader(hardenScript))
	if err != nil {
		return fmt.Errorf("закрытие машины: %w", err)
	}
	j.log("вход по паролю выключен, ufw: 22 %s", strings.Join(ports, " "))
	_ = out
	return nil
}

const hardenScript = `set -eu
# 00- sorts before cloud-init's 50-: sshd keeps the FIRST value of a keyword.
cat >/etc/ssh/sshd_config.d/00-nekko-hardening.conf <<'CONF'
# Written by the nekko hub (roadmap 42 П6.15). Key login only.
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
CONF
sshd -t
systemctl reload ssh 2>/dev/null || systemctl reload sshd
effective="$(sshd -T)"
case "$effective" in
	*"passwordauthentication no"*) ;;
	*) echo "sshd still allows passwords" >&2; exit 1 ;;
esac
if ! command -v ufw >/dev/null; then
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq >/dev/null && apt-get install -y -qq ufw >/dev/null
fi
ufw allow 22/tcp >/dev/null
for p in "$@"; do ufw allow "$p" >/dev/null; done
ufw --force enable >/dev/null
`

// transport is what the box's relays listen on: a Reality relay on TCP, a
// voice relay (QUIC) on UDP.
func (j *jobRun) transport() string {
	if j.acc.voice() {
		return "udp"
	}
	return "tcp"
}

// media is where the box's relays forward a call's datagrams, as the
// registry has them now: a voice box its region's SFU media addresses, a
// stream box every shard's. A voice box with none cannot be deployed; a stream
// box without them still carries messaging, only no call through its TCP rung.
func (j *jobRun) media() ([]MediaTarget, error) {
	if j.acc.proto == ProtoVlessXhttp {
		return nil, nil // streams only (identity.go EdgeEnv)
	}
	if j.p.media == nil {
		if j.acc.voice() {
			return nil, errors.New("хаб запущен без реестра SFU, голосовой вход не развернуть")
		}
		return nil, nil
	}
	m, err := j.p.media.Media(j.ctx, j.acc.region)
	if err != nil {
		if !j.acc.voice() {
			return nil, fmt.Errorf("медиа-адреса SFU: %w", err)
		}
		return nil, fmt.Errorf("медиа-адреса региона %s: %w", j.acc.region, err)
	}
	if len(m) == 0 && j.acc.voice() {
		return nil, fmt.Errorf("регион %s: %w", j.acc.region, errNoMedia)
	}
	return m, nil
}

func (j *jobRun) arch(r Remote) (string, error) {
	out, err := r.Run(j.ctx, "uname -m", nil)
	if err != nil {
		return "", err
	}
	switch strings.TrimSpace(out) {
	case "x86_64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("архитектура не поддерживается: uname -m ответил %s", remoteWord(out))
}

// remoteWordRe is what a machine's answer may look like to be quoted in an
// error: one short word, as uname or a count prints.
var remoteWordRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

// remoteWord renders a machine's answer for an error that lands in the job
// log and entry_jobs.error, which carry no raw remote output (migration 043,
// audit 42 recheck НН5): a short word is shown, anything else only by length.
func remoteWord(out string) string {
	out = strings.TrimSpace(out)
	if remoteWordRe.MatchString(out) {
		return strconv.Quote(out)
	}
	return fmt.Sprintf("непонятное (%d байт)", len(out))
}

func (j *jobRun) deploy(r Remote, build EdgeBuild, rel provRelay) error {
	want := build.SHA
	have := j.edgeOnBox(r, build.Arch)
	if have != want {
		binary, err := build.read()
		if err != nil {
			return err
		}
		if _, err := r.Run(j.ctx, "cat > /usr/local/bin/nekko-edge.new && chmod 755 /usr/local/bin/nekko-edge.new",
			bytes.NewReader(binary)); err != nil {
			return fmt.Errorf("загрузка бинаря: %w", err)
		}
		j.log("бинарь %s… загружен (%d МБ)", want[:12], len(binary)>>20)
	} else {
		j.log("бинарь %s… уже на месте", want[:12])
	}
	if err := j.install(r, rel); err != nil {
		return err
	}
	// A swap that fails after moving the binary into place leaves the box
	// recorded at its old build; the next run reads the truth off the box.
	j.recordEdge(build.Arch, want)
	return nil
}

// edgeOnBox reads the build the box runs and records it: a fact worth keeping
// even if what follows fails, since the rollout reads versions from here.
// "" when there is no binary yet.
func (j *jobRun) edgeOnBox(r Remote, arch string) string {
	out, _ := r.Run(j.ctx, "sha256sum /usr/local/bin/nekko-edge 2>/dev/null | cut -d' ' -f1", nil)
	have := strings.TrimSpace(out)
	if !shaRe.MatchString(have) {
		return ""
	}
	j.recordEdge(arch, have)
	return have
}

func (j *jobRun) streamUpstreams() (string, error) {
	if j.acc.voice() || j.p.streams == nil {
		return j.p.upstream, nil
	}
	endpoints, err := j.p.streams.Streams(j.ctx)
	if err != nil {
		return "", fmt.Errorf("stream destinations: %w", err)
	}
	base := strings.Split(j.p.upstream, ",")
	return Upstreams(base[0], append(base[1:], endpoints...)...)
}

// install puts one relay on whatever binary is staged at nekko-edge.new (or
// the current one): unit, env, check, swap, restart, listening.
func (j *jobRun) install(r Remote, rel provRelay) error {
	if _, err := r.Run(j.ctx, "cat > /etc/systemd/system/nekko-edge@.service", strings.NewReader(edgeUnit)); err != nil {
		return fmt.Errorf("unit-файл: %w", err)
	}
	// The relay's identity, rendered as the env nekko-edge runs with. Goes on
	// stdin, never into the job log.
	var env strings.Builder
	media, err := j.media()
	if err != nil {
		return err
	}
	upstreams, err := j.streamUpstreams()
	if err != nil {
		return err
	}
	envMap, err := rel.id.EdgeEnv(upstreams, media)
	if err != nil {
		return fmt.Errorf("env релея %s: %w", rel.name, err)
	}
	// Where this relay reports its own counters (П9.3). Its name comes from
	// here rather than from anything on the box, and the token is this box's
	// own (PulseToken): the box must not be able to choose which relay's row
	// its numbers land in, and the hub accepts the token for this box's
	// relays only.
	if j.p.pulseURL != "" && j.p.pulseToken != "" {
		envMap["EDGE_PULSE_URL"] = j.p.pulseURL
		envMap["EDGE_PULSE_TOKEN"] = PulseToken(j.p.pulseToken, j.box)
		envMap["EDGE_RELAY_NAME"] = rel.name
	}
	switch {
	case j.acc.proto == ProtoVlessXhttp:
		j.log("%s: сайт на домене релея, туннель только для потоков", rel.name)
	case j.acc.voice():
		j.log("%s пересылает только в SFU региона %s: %s", rel.name, j.acc.region, mediaList(media))
	case len(media) > 0:
		j.log("%s: датаграммы звонка только в SFU: %s", rel.name, mediaList(media))
	default:
		j.log("%s: ни один SFU не закрепил медиа-адрес, TCP-ступень голоса через релей не поедет", rel.name)
	}
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env.WriteString("# Written by the nekko hub from entry_relays.identity_ct. Not the source of truth.\n")
	for _, k := range keys {
		env.WriteString(k + "=" + envMap[k] + "\n")
	}
	if _, err := r.Run(j.ctx, "umask 077 && mkdir -p /etc/nekko-edge && cat > /etc/nekko-edge/"+rel.name+".env.new",
		strings.NewReader(env.String())); err != nil {
		return fmt.Errorf("env релея: %w", err)
	}
	if _, err := r.Run(j.ctx, "bash -s -- "+rel.name+" "+strconv.Itoa(rel.id.Port)+" "+j.transport(), strings.NewReader(edgeSwapScript)); err != nil {
		return err
	}
	// The hub's own words, not the script's last line (migration 043: the
	// job log never carries raw remote output).
	j.log("nekko-edge@%s запущен на :%d", rel.name, rel.id.Port)
	return nil
}

// rollback puts the build the box ran before the last swap back under every
// relay (П6.14): the hub's image holds only the new build, so the old one
// survives on the box as nekko-edge.prev, kept by the swap.
func (j *jobRun) rollback(r Remote, arch string, relays []provRelay) error {
	if _, err := r.Run(j.ctx, "test -f /usr/local/bin/nekko-edge.prev && cp -p /usr/local/bin/nekko-edge.prev /usr/local/bin/nekko-edge.new", nil); err != nil {
		return fmt.Errorf("прежнего бинаря на машине нет, откатывать не на что: %w", err)
	}
	for _, rel := range relays {
		if err := j.install(r, rel); err != nil {
			return fmt.Errorf("откат %s: %w", rel.name, err)
		}
	}
	if sha := j.edgeOnBox(r, arch); sha != "" {
		j.log("откат: на машине снова %s…", sha[:12])
	}
	return nil
}

// recordEdge notes the build the box runs. A failure to note it is logged and
// not fatal: the next run on this box reads the binary again.
func (j *jobRun) recordEdge(arch, sha string) {
	if err := j.p.svc.setBoxEdge(j.ctx, j.box, arch, sha); err != nil {
		applog.Error(j.ctx, "entry", "box build not recorded", applog.F("job", j.id.String()), applog.F("box", j.box), applog.F("err", err.Error()))
	}
}

const edgeUnit = `# Installed by the nekko hub. One instance per relay: nekko-edge@<name>.
[Unit]
Description=Nekko edge relay %i
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/nekko-edge/%i.env
ExecStart=/usr/local/bin/nekko-edge run
Restart=always
RestartSec=2
DynamicUser=yes
# The only writable place under ProtectSystem=strict: an XHTTP relay keeps its
# ACME account and certificates here across restarts (Let's Encrypt is rate
# limited per domain). Unused by the other protocols.
StateDirectory=nekko-edge-%i
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`

// edgeSwapScript validates the new binary against the new env BEFORE either
// replaces anything — a bad env never takes down a working relay — then
// restarts the instance and waits until it listens.
const edgeSwapScript = `set -eu
name="$1"; port="$2"; net="$3"
case "$net" in tcp) ssf=-tln ;; udp) ssf=-uln ;; *) echo "transport $net" >&2; exit 2 ;; esac
bin=/usr/local/bin/nekko-edge
[ -f "$bin.new" ] || cp "$bin" "$bin.new"
chmod 600 "/etc/nekko-edge/$name.env.new"
( set -a; . "/etc/nekko-edge/$name.env.new"; set +a; "$bin.new" check ) >/dev/null
# The build being replaced is kept: the hub's image carries only the new one,
# and a rollback needs the old one from somewhere (П6.14).
if [ -f "$bin" ] && ! cmp -s "$bin" "$bin.new"; then cp -p "$bin" "$bin.prev"; fi
mv "$bin.new" "$bin"
mv "/etc/nekko-edge/$name.env.new" "/etc/nekko-edge/$name.env"
systemctl daemon-reload
systemctl enable "nekko-edge@$name" >/dev/null 2>&1
systemctl restart "nekko-edge@$name"
for _ in $(seq 1 40); do
	if systemctl is-active --quiet "nekko-edge@$name" && ss $ssf | grep ":$port " >/dev/null; then
		echo "nekko-edge@$name слушает $net/$port"
		exit 0
	fi
	sleep 0.5
done
journalctl -u "nekko-edge@$name" -n 20 --no-pager >&2
exit 1
`

// probe asks a vantage to probe the relays and waits for its verdicts.
//
// # Three readings, not two (П9.7)
//
// `clean` and `reality_wins` are both the tunnel working. The second is the
// better outcome of the two — Reality getting through where bare TLS to the
// address is already dead — and this gate used to refuse it, which would have
// rejected exactly the relays the protocol exists for.
//
// A verdict that NAMES a failure (a banned address, a spotted protocol, a cut
// domain) is a refusal, and that is what this gate is for. Only those three
// words refuse (admissionRefusal) — a list of what refuses, not of what does
// not: a refusal hands a paid machine back, so a word the gate does not know
// must not be one.
//
// And `unknown`, `network_down`, `probe_failed` and a missing verdict are
// neither: they are the absence of evidence. `probe_failed` is the prober's
// own engine not starting (audit 42-autopilot С7) — read as a refusal, a
// broken xray on the vantage handed back every machine bought until the
// strikes ran out. The gate used to read them as "the address is dirty
// from birth, hand the machine back" — a sentence about a machine nobody had
// any evidence against, and the third place to make that mistake after the
// detector and the rollout (commits 60563416, 39a8f2d3). П9.7's chaos run
// caught it on a repair, where the sentence is not even coherent: a relay the
// hub has been running for weeks has no birth to be dirty from, and the box
// had in fact come back up.
//
// So a round that judged nothing is asked again until the deadline, and the
// deadline's error says the vantage could not judge rather than condemning an
// address.
func (j *jobRun) probe(relays []provRelay) error {
	names := relayNames(relays)
	deadline := time.Now().Add(j.p.probeTimeout)
	for {
		v, err := j.askVantage(names, deadline)
		if err != nil {
			return err
		}
		var blind []string
		refused := admissionRefused{}
		for _, n := range names {
			verdict := v[n]
			if verdict == "" {
				verdict = "без вердикта"
			}
			j.log("%s: %s", n, verdict)
			switch {
			case admissionHealthy(verdict):
			case admissionRefusal(verdict):
				refused[n] = verdict
			default:
				blind = append(blind, n+"="+verdict)
			}
		}
		if len(refused) > 0 {
			// Named failures outrank an unjudged relay beside them: one
			// banned address in a batch is a refusal whatever the others did.
			return refused
		}
		if len(blind) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: точка обзора так и не смогла судить за %s: %s. это отсутствие улик, а не приговор адресу",
				errNoVerdict, j.p.probeTimeout, strings.Join(blind, ", "))
		}
		j.log("улик пока нет (%s) — спрашиваю точку обзора заново", strings.Join(blind, ", "))
		select {
		case <-j.ctx.Done():
			return j.ctx.Err()
		case <-time.After(j.p.probePoll):
		}
	}
}

// errNoVerdict is an admission nobody could judge: the vantage answered only
// with words that say nothing about the relay (probe_failed, network_down),
// or nobody claimed the task at all. It is the absence of evidence, and every
// caller treats it as such (owner's decision 27.10, audit 42 recheck НС1): a
// machine is not handed back on it, and a repair that ends on it is `held`,
// not a strike.
var errNoVerdict = errors.New("нет улик")

// admissionRefused is the vantage naming what is wrong with relays it was
// asked to admit: relay name → verdict. Typed so a caller can act on which
// relay it was, not only report it (TopUp burns a port born banned).
type admissionRefused map[string]string

func (e admissionRefused) Error() string {
	names := make([]string, 0, len(e))
	for n := range e {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n + "=" + e[n]
	}
	return "не допущен: " + strings.Join(parts, ", ")
}

// admissionHealthy says this verdict is the tunnel working. Both words mean
// it; see [DiagnoseBox], which reads the same pair the same way.
func admissionHealthy(verdict string) bool {
	return verdict == VerdictClean || verdict == VerdictRealityWins
}

// admissionRefusal says this verdict names what is wrong with the relay: its
// address banned, its protocol spotted, its domain cut. Nothing else refuses.
func admissionRefusal(verdict string) bool {
	switch verdict {
	case VerdictIPBanned, VerdictFingerprint, CauseDomainBlocked:
		return true
	}
	return false
}

// askVantage queues one admission probe and waits for the answer. A task that
// nobody claims by the deadline is the vantage being absent, which is a
// different failure from a vantage that looked and saw nothing.
func (j *jobRun) askVantage(names []string, deadline time.Time) (map[string]string, error) {
	task, err := j.p.svc.createProbeTask(j.ctx, j.id, names)
	if err != nil {
		return nil, err
	}
	j.log("проба допуска поставлена в очередь точке обзора")
	for {
		v, err := j.p.svc.probeVerdicts(j.ctx, task)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: ни одна точка обзора не ответила на пробу за %s. пробник запущен?", errNoVerdict, j.p.probeTimeout)
		}
		select {
		case <-j.ctx.Done():
			return nil, j.ctx.Err()
		case <-time.After(j.p.probePoll):
		}
	}
}

func fingerprint(authorizedKey string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return "?"
	}
	return ssh.FingerprintSHA256(k)
}

// Draining a box (roadmap 42 П6.14 Ф2). After [Service.Drain] no new client
// is handed its relays and every holder was told to refetch; a client riding
// one of them opens its session through the replacement and only then drops
// the old link. What the hub watches is the box itself: established TCP on
// the relay ports. Zero means nobody is left to cut; past the deadline the
// rest is cut by the restart, and their sessions come back through the batch
// they already hold.

// drainPoll is how often the box is asked who is still on it.
const drainPoll = 5 * time.Second

// waitEmpty waits until no connection is established on the relays' ports,
// or until `limit` passes; returns how many were left (0 = empty).
func (j *jobRun) waitEmpty(r Remote, relays []provRelay, limit time.Duration) (int, error) {
	if len(relays) == 0 {
		return 0, nil
	}
	if j.acc.voice() {
		// QUIC rides one unconnected UDP socket: the box has no per-client
		// connection to count. A call cut by the restart notices within the
		// carrier's QUIC idle timeout and redials through its batch.
		j.log("голосовой вход: соединения по UDP не считаются, звонки переподключатся сами")
		return 0, nil
	}
	ports := make([]string, 0, len(relays))
	for _, rel := range relays {
		ports = append(ports, strconv.Itoa(rel.id.Port))
	}
	sort.Strings(ports)
	cmd := "bash -s -- " + strings.Join(ports, " ")
	deadline := time.Now().Add(limit)
	last := -1
	for {
		out, err := r.Run(j.ctx, cmd, strings.NewReader(connCountScript))
		if err != nil {
			return 0, fmt.Errorf("счёт соединений: %w", err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			return 0, fmt.Errorf("счёт соединений: ответ %s", remoteWord(out))
		}
		if n != last {
			j.log("на релеях соединений: %d", n)
			last = n
		}
		if n == 0 {
			return 0, nil
		}
		if time.Now().After(deadline) {
			j.log("срок пересадки %s вышел, осталось соединений: %d, их оборвёт перезапуск", limit, n)
			return n, nil
		}
		select {
		case <-j.ctx.Done():
			return n, j.ctx.Err()
		case <-time.After(j.p.drainPoll):
		}
	}
}

// connCountScript prints how many TCP connections are established on any of
// the ports given as arguments, as the relay sees them (local port).
const connCountScript = `set -eu
filter=""
for p in "$@"; do
	[ -n "$filter" ] && filter="$filter or "
	filter="${filter}sport = :$p"
done
ss -Htn state established "( $filter )" | wc -l
`
