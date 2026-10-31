package entry

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/config"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// Kind of relay in the pool (plan 42 §9).
const (
	// KindBootstrap relays are reachable through public sources without an
	// account (П6.5); they are never part of an account batch.
	KindBootstrap = "bootstrap"
	// KindPersonal relays are handed out only in account batches.
	KindPersonal = "personal"
)

const (
	triggerInitial          = "initial"
	triggerDeathReplacement = "death_replacement"
	// A slot emptied because its relay left the pool for a rollout (П6.14):
	// the relay is fine, so the refill must not read as a death in the dataset.
	triggerDrainReplacement = "drain_replacement"
)

// DefaultBatchSize is how many personal relays an account holds: «a couple of
// working relays», not the pool (plan 42 §9). The age multiplier is П6.7.
const DefaultBatchSize = 2

// batchKeyLabel names the HMAC key the batch choice is keyed with
// (cryptobox.Derive): server-held, so a censor holding account ids cannot
// compute whose batch holds which relay.
const batchKeyLabel = "entry-batch"

// RecordLifetime is how long a signed record is valid. The hub re-signs alive
// relays before it runs out (П6.10); a client holding an expired record
// re-asks.
const RecordLifetime = 30 * 24 * time.Hour

// relayPorts are the ports a box's relays take, in order: 443 first (looks like
// any HTTPS server), then the ports CDNs serve TLS on. One relay per port.
var relayPorts = []int{443, 8443, 2053, 2083, 2087, 2096}

var (
	// ErrRejected wraps every reason an operator request is refused.
	ErrRejected = errors.New("entry: rejected")
	// ErrNoSuchRelay: no relay under that name in the state the call needs.
	ErrNoSuchRelay = errors.New("entry: no such relay")
	// ErrNoSuchBox: no box under that name.
	ErrNoSuchBox = errors.New("entry: no such box")
	// ErrBoxRetired: the box was handed back; it takes no relay and no job but
	// the wipe.
	ErrBoxRetired = errors.New("entry: box is retired")
)

// Why a relay died (entry_relays.died_reason, migration 051): the abuse
// dataset's label, so every death carries one (plan 42 П6.10).
const (
	DeathIPBanned     = "ip_banned"
	DeathFingerprint  = "fingerprint"
	DeathDestination  = "destination"
	DeathProviderDown = "provider_down"
	DeathRotated      = "rotated"
	DeathOther        = "other"
	// DeathDomainBlocked: an XHTTP box's domain is blocked by name while its
	// address lives (migration 052) — cured by a new domain, not a machine.
	DeathDomainBlocked = "domain_blocked"
)

// CensorDeaths are the death reasons the censor causes. Every other reason —
// a machine handed back on purpose, a hoster outage, a broken cover — is the
// pool's own business: it costs a replacement, but it is no ban, and a screen
// that counts blocks must not count it.
var CensorDeaths = []string{DeathIPBanned, DeathFingerprint, DeathDomainBlocked}

func validDeathReason(r string) bool {
	switch r {
	case DeathIPBanned, DeathFingerprint, DeathDestination, DeathProviderDown, DeathRotated, DeathOther, DeathDomainBlocked:
		return true
	}
	return false
}

// Notifier tells live sessions that what a bundle request would answer has
// changed, so they ask again now instead of at their next connect or expiry
// (roadmap 42 П6.11). A signal only: the records still travel in the bundle,
// signed, so the notifier is trusted with nothing but a nudge. Declared here
// on the consumer side; the gateway implements it via internal/entrywire.
//
// Called after the change commits, never inside the transaction: a session
// that refetches on the signal must find the change already visible.
type Notifier interface {
	// RelaysChanged: these accounts each lost a relay of their batch.
	RelaysChanged(accounts []uuid.UUID)
	// SourcesChanged: the source list every bundle carries has changed.
	SourcesChanged()
}

// Service owns the pool and the grant registry.
type Service struct {
	pool      db.Pool
	repo      *Repository
	signer    *Signer
	batchSize int
	// benches takes finished bench runs into the dataset; nil = Postgres only.
	benches BenchSink
	// reach takes clients' reach telemetry into the dataset (П9.9); nil drops
	// it after validation and costs nothing else.
	reach ReachSink
	// pulses takes relays' self-reports into the dataset (П9.3); nil leaves
	// the hub without relay stats.
	pulses PulseSink
	// relayKeys owns subscribers' access keys (pulse.go); nil leaves relays
	// on their shared credential.
	relayKeys RelayKeys
	// subscribers sizes the stream pools by subscriptions (subscribers.go).
	subscribers SubscriberCount
	// traffic takes the gateway's per-socket byte counts into the dataset
	// (П9.9, traffic.go); nil drops them.
	traffic UserTrafficSink
	// doors is the few-minute memory of which address is which box.
	doors doorCache
	// verdicts is where the detector reads what the vantage last saw (П9.5);
	// nil switches incident detection off entirely.
	verdicts    VerdictSource
	nodeSignals NodeSignalSource
	// load is where the fleet reads what the relays carried (П9.14–П9.15,
	// demand.go); nil leaves the fleet without demand or capacity figures.
	load LoadSource
	// sizing is the last reading of every pool's demand and capacity
	// (sizing.go), kept for one bucket.
	sizingMu sync.Mutex
	sizing   *fleetSizing
	// inspect runs the escalated grid against a box that read badly (П9.4);
	// nil leaves the detector naming causes from the probe alone.
	inspect BoxInspector
	// coverAnswers dials a cover site from the hub; the detector asks it
	// only about the proven covers a healthy box is missing (covers.go).
	// Nil is the real TLS handshake; tests put a fake here.
	coverAnswers func(host string) bool
	// vantages counts the observation points the probe dataset has heard
	// from (К3); nil leaves the hub unable to tell, and it refuses to enable
	// two-vantage confirmation on that ground.
	vantages VantageCounter
	// voiceRegions lists the regions a voice pool is kept for (fleet.go);
	// nil leaves voice pools to start with their first machine.
	voiceRegions VoiceRegionSource
	// policy is the few-second memory of the eviction thresholds (К3). The
	// row itself is entry_detection_policy; see policy.go for why reads are
	// cached at all and why the cache is this short.
	policy policyCache
	// benchPolicy is the canary's own few-second memory of its thresholds
	// and sample size (benchpolicy.go), kept apart from the detection policy
	// because the two are turned by different people at different moments.
	benchPolicy benchPolicyCache
	batchKey    []byte
	// reachKey pseudonymises account ids before they reach that dataset.
	reachKey []byte
	now      func() time.Time
	notify   Notifier
	// buyer is the registrar a relay's domain is bought through when the pool
	// is dry (Ф6), with its purchase policy. Swapped whole when the owner
	// saves a provider in the console (П9.1), so a purchase in flight keeps
	// the registrar and caps it started with. A nil registrar leaves the hub
	// pool-only — AcquireDomain fails once the pool runs out.
	buyer atomic.Pointer[domainBuyer]
	// connector knows the built-in provider adapters (П9.1); nil = none.
	connector ProviderConnector
	// activeRegistrar is the kind the current buyer came from; nil = none.
	activeRegistrar atomic.Pointer[string]
	// providerMu serializes provider saves and reloads, so two saves cannot
	// interleave their write and the swap that follows it.
	providerMu sync.Mutex
	// checkSpacing paces candidate quotes under the registrar's rate limit;
	// defaulted in NewService, dropped to zero in tests.
	checkSpacing time.Duration
	// replacer re-provisions a box whose domain was blocked (Ф6); nil leaves
	// a domain_blocked death to be cured by hand.
	replacer DomainReplacer
	// hosting is the hoster machines are ordered from (П9.2), with its order
	// policy and caps. Swapped whole on a provider save, like buyer. A nil
	// provider leaves the hub unable to order or hand back a machine.
	hosting atomic.Pointer[hostingAccount]
	// instancePoll is how often an order asks the hoster whether the machine
	// has an address yet; defaulted in NewService, shortened in tests.
	instancePoll time.Duration
	// bg is the work a call starts and leaves running after it returns: a
	// job, a repair. See [Service.spawn].
	bg background
}

// background tracks the goroutines [Service.spawn] starts, so they can be
// stopped and waited for as one.
type background struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// spawn runs f on its own goroutine with the service's background context —
// the context a job or a repair derives its own deadline from, since the
// request that started it is gone long before a machine answers.
//
// The hub never stops it: a process that exits takes its jobs along, and the
// next one fails their rows at start (AbandonRunningJobs). What stopping is
// for is a caller that outlives a Service and shares its database with the
// next one — a test, whose job left running would keep writing into the
// next test's state (see [Service.stopBackground]).
func (s *Service) spawn(f func(ctx context.Context)) {
	s.bg.wg.Add(1)
	go func() {
		defer s.bg.wg.Done()
		f(s.bg.ctx)
	}()
}

// stopBackground cancels everything [Service.spawn] started and waits until
// it has returned. A job cut off this way still records its verdict (finish
// writes on a context of its own), so before the caller lets go of the pool
// the job's row says it ended, and no vantage can claim a probe task it left
// queued (claimProbeTask).
func (s *Service) stopBackground() {
	s.bg.cancel()
	s.bg.wg.Wait()
}

// NewService builds the service. signer may be nil (no ENTRY_SIGNING_KEY): the
// pool then serves what it holds and refuses to admit, re-sign or publish
// ([ErrNoSigner]). batchSize ≤ 0 means [DefaultBatchSize]. The batch key is
// derived from the content key, so cryptobox must be configured first.
func NewService(pool db.Pool, signer *Signer, batchSize int) (*Service, error) {
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	batchKey, err := cryptobox.Derive(batchKeyLabel)
	if err != nil {
		return nil, fmt.Errorf("entry: batch key: %w", err)
	}
	reachKey, err := cryptobox.Derive(reachKeyLabel)
	if err != nil {
		return nil, fmt.Errorf("entry: reach key: %w", err)
	}
	bgCtx, bgCancel := context.WithCancel(context.Background())
	return &Service{
		bg:           background{ctx: bgCtx, cancel: bgCancel},
		pool:         pool,
		repo:         NewRepository(pool),
		signer:       signer,
		batchSize:    batchSize,
		batchKey:     batchKey,
		reachKey:     reachKey,
		now:          time.Now,
		checkSpacing: checkSpacing,
		instancePoll: instanceWaitPoll,
	}, nil
}

// SetNotifier wires the live nudge. Called once at startup; left unset, a
// change reaches each client at its next connect or expiry, as before П6.11.
func (s *Service) SetNotifier(n Notifier) { s.notify = n }

// BoxSpec is a rented machine as the operator describes it, or as the hub
// ordered it. Provider, location and rental are operator-only; the address is
// sealed at rest.
type BoxSpec struct {
	Name        string
	Address     string
	Provider    string
	Location    string
	RentedUntil *time.Time
	// SSHUser is who the hub logs in as; "" means root.
	SSHUser string
	// Proto is what every relay on the box speaks; "" means a Reality stream
	// box. One protocol per box: a box is one address (owner, 15.10).
	Proto string
	// Region is the voice region a Hysteria2 box forwards to — the id voice
	// tickets name (`ru`, `eu-west`). Required for a voice box, empty otherwise.
	Region string
	// Domain is an XHTTP box's own domain, its A record on Address (П4в).
	// Required for an XHTTP box, empty otherwise. Sealed at rest.
	Domain string
	// InstanceID is the hoster's id for the machine, set when the hub ordered
	// it itself (П9.2); empty on a machine rented by hand, which is what
	// makes it one nothing can reboot or hand back through an API.
	InstanceID string
	// ProviderRegion is the hoster's region id the machine was ordered in,
	// and Plan its size; MonthlyCents the rent it was quoted at. All empty or
	// zero on a hand-rented machine, whose rent the hub does not know.
	ProviderRegion string
	Plan           string
	MonthlyCents   int
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// checkRelayDomain: a plain lower-case domain, and never the brand's — the
// relay's domain is a throwaway that dies with it (plan 42 П4б), and a
// subdomain of ours would hand a censor the brand's whole tree.
func checkRelayDomain(d string) error {
	base := strings.ToLower(config.Default().Base)
	switch {
	case len(d) > 253 || !domainRe.MatchString(d):
		return fmt.Errorf("%w: domain %q is not a domain name", ErrRejected, d)
	case d == base || strings.HasSuffix(d, "."+base):
		return fmt.Errorf("%w: a relay's domain is never the brand's (%s)", ErrRejected, base)
	}
	return nil
}

var sshUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

var boxNameRe = regexp.MustCompile(`^[a-z]+[0-9]+$`)

// CreateBox records a machine. One address, one box: a box IS its address —
// the unit a censor bans — so a second box on it would hide that two relays
// share one fate.
func (s *Service) CreateBox(ctx context.Context, b BoxSpec) error {
	b.Provider, b.Location = strings.TrimSpace(b.Provider), strings.TrimSpace(b.Location)
	b.Address = strings.TrimSpace(b.Address)
	// Stored the way net prints it: the errors from below quote the address
	// canonically, and Redact and the one-address-one-box rule must see the
	// same string (audit 42 recheck НН5).
	if ip := net.ParseIP(b.Address); ip != nil {
		b.Address = ip.String()
	}
	if b.SSHUser == "" {
		b.SSHUser = "root"
	}
	if b.Proto == "" {
		b.Proto = ProtoVlessRealityVision
	}
	b.Region = strings.TrimSpace(b.Region)
	b.Domain = strings.ToLower(strings.TrimSpace(b.Domain))
	switch {
	case b.Proto != ProtoVlessRealityVision && b.Proto != ProtoHysteria2 && b.Proto != ProtoVlessXhttp:
		return fmt.Errorf("%w: box protocol %q", ErrRejected, b.Proto)
	case b.Proto != ProtoVlessXhttp && b.Domain != "":
		return fmt.Errorf("%w: only an XHTTP box has a domain", ErrRejected)
	case b.Proto == ProtoHysteria2 && !regionRe.MatchString(b.Region):
		return fmt.Errorf("%w: a voice box needs the region it serves, got %q", ErrRejected, b.Region)
	case b.Proto != ProtoHysteria2 && b.Region != "":
		return fmt.Errorf("%w: only a voice box serves a region", ErrRejected)
	case !sshUserRe.MatchString(b.SSHUser):
		return fmt.Errorf("%w: ssh user %q", ErrRejected, b.SSHUser)
	case net.ParseIP(b.Address) == nil:
		return fmt.Errorf("%w: box address %q must be an IP — a box is its address", ErrRejected, b.Address)
	case !boxNameRe.MatchString(b.Name) || len(b.Name) > 24:
		return fmt.Errorf("%w: box name %q must be <city letters><number>, e.g. ams1", ErrRejected, b.Name)
	case b.Address == "":
		return fmt.Errorf("%w: box %s has no address", ErrRejected, b.Name)
	case b.Provider == "" || b.Location == "":
		return fmt.Errorf("%w: box %s needs provider and location", ErrRejected, b.Name)
	}
	// An XHTTP box may start without a domain: the hub assigns one during
	// provisioning, from the pool or a purchase (Ф6). A domain given by hand is
	// still checked.
	if b.Proto == ProtoVlessXhttp && b.Domain != "" {
		if err := checkRelayDomain(b.Domain); err != nil {
			return err
		}
		if err := s.checkNotExcluded(ctx, b.Domain); err != nil {
			return err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if err := s.repo.lockBoxes(ctx, tx); err != nil {
		return err
	}
	stored, err := s.openBoxes(ctx, tx)
	if err != nil {
		return err
	}
	for _, st := range stored {
		switch {
		case st.Name == b.Name:
			return fmt.Errorf("%w: box %s exists", ErrRejected, b.Name)
		case !st.Unreadable && st.Address == b.Address:
			return fmt.Errorf("%w: address of %s already belongs to box %s", ErrRejected, b.Name, st.Name)
		case b.Domain != "" && st.Domain == b.Domain:
			// One A record, one certificate, one fate: a domain is one box's.
			return fmt.Errorf("%w: domain of %s already belongs to box %s", ErrRejected, b.Name, st.Name)
		}
	}
	ct, err := cryptobox.Encrypt([]byte(b.Address))
	if err != nil {
		return err
	}
	var domainCT []byte
	if b.Domain != "" {
		if domainCT, err = cryptobox.Encrypt([]byte(b.Domain)); err != nil {
			return err
		}
	}
	if err := s.repo.insertBox(ctx, tx, b, ct, domainCT); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// boxAccess is how the hub reaches a box over ssh, and what the box is.
type boxAccess struct {
	address string
	user    string
	hostKey string
	// hostKeyForgotten: the operator dropped the key on record (ForgetHostKey)
	// and no login has taken a new one yet. Only the operator's own job may
	// (jobRun.firstUse).
	hostKeyForgotten bool
	proto            string
	region           string
	// domain is an XHTTP box's own domain, empty when it has none yet (the hub
	// assigns one during provisioning, Ф6).
	domain string
	// retired: the box was handed back; only its wipe may still log in.
	retired bool
}

// voice reports whether the box is a voice entry (Hysteria2).
func (a boxAccess) voice() bool { return a.proto == ProtoHysteria2 }

func (s *Service) boxAccess(ctx context.Context, box string) (boxAccess, error) {
	var ct, domainCT []byte
	var a boxAccess
	err := s.pool.QueryRow(ctx, selectBoxAccess, box).Scan(&ct, &a.user, &a.hostKey, &a.hostKeyForgotten, &a.proto, &a.region, &a.retired, &domainCT)
	if errors.Is(err, pgx.ErrNoRows) {
		return boxAccess{}, fmt.Errorf("%w: %s", ErrNoSuchBox, box)
	}
	if err != nil {
		return boxAccess{}, err
	}
	addr, err := cryptobox.Decrypt(ct)
	if err != nil {
		return boxAccess{}, fmt.Errorf("entry: box %s: %w", box, err)
	}
	a.address = string(addr)
	if domainCT != nil {
		domain, err := cryptobox.Decrypt(domainCT)
		if err != nil {
			return boxAccess{}, fmt.Errorf("entry: box %s domain: %w", box, err)
		}
		a.domain = string(domain)
	}
	return a, nil
}

// setHostKey records the key a box with none on record presented.
//
// # Meeting a reinstalled machine withdraws the sentence passed on it
//
// A key that replaces a forgotten one is the operator's job meeting the
// machine they reinstalled (ForgetHostKey, then jobRun.firstUse). Whatever
// the second chance concluded while the key was forgotten was concluded on
// the machine before: its relays unreachable through the reinstall read as
// banned, the eviction followed, and the write-off only waited for the
// operator to let go (boxInOperatorsHands) to delete the new machine at the
// hoster (audit 42 third pass, НН-3-1). So, in the same transaction as the
// key, the machine's open or dead second chance is withdrawn and its suspect
// relays go back to admission: the deploy this login opens judges them on a
// fresh verdict, and only a new eviction opens a new second chance (owner's
// decision 27.10).
func (s *Service) setHostKey(ctx context.Context, box, key string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	var reinstalled bool
	switch err := tx.QueryRow(ctx, setBoxHostKey, box, key).Scan(&reinstalled); {
	case errors.Is(err, pgx.ErrNoRows):
		return nil // a key is on record already; it sticks
	case err != nil:
		return err
	}
	var withdrawn int64
	if reinstalled {
		tag, err := tx.Exec(ctx, withdrawBoxSentence, box, "переустановлена оператором: приговор снят, нужен свежий вердикт")
		if err != nil {
			return err
		}
		withdrawn = tag.RowsAffected()
		if _, err := tx.Exec(ctx, readmitBoxRelays, box); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if withdrawn > 0 {
		applog.Info(ctx, "entry", "second chance withdrawn on reinstall", applog.F("box", box))
	}
	return nil
}

// setBoxEdge records the relay build the box runs (П6.14).
func (s *Service) setBoxEdge(ctx context.Context, box, arch, sha string) error {
	_, err := s.pool.Exec(ctx, setBoxEdge, box, arch, sha)
	return err
}

// ForgetHostKey drops the host key on record, so the operator's next
// provisioning job trusts what the box presents — after the operator
// reinstalled the machine. Nothing automatic will log in until that job has
// run (jobRun.firstUse).
func (s *Service) ForgetHostKey(ctx context.Context, box string) error {
	tag, err := s.pool.Exec(ctx, clearBoxHostKey, box)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrNoSuchBox, box)
	}
	return nil
}

// AddRelay mints a relay on a box: a fresh identity, the next free name
// (<box>-<n>, never reused — a dead relay's name stays its own) and the next
// free port. The relay is pending: deployed and probed by the caller, handed
// out only after [Service.Admit].
func (s *Service) AddRelay(ctx context.Context, box, kind string) (string, error) {
	if kind != KindBootstrap && kind != KindPersonal {
		return "", fmt.Errorf("%w: kind %q", ErrRejected, kind)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	lb, ok, err := s.repo.lockBox(ctx, tx, box)
	if err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("%w: %s", ErrNoSuchBox, box)
	} else if lb.retired {
		return "", fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	if err := refuseVantageBox(ctx, tx, box); err != nil {
		return "", err
	}
	proto := lb.proto
	existing, err := s.repo.relaysOnBox(ctx, tx, box)
	if err != nil {
		return "", err
	}
	n, used := len(existing)+1, map[int]bool{}
	for _, r := range existing {
		// A dead relay's port is free again and its name is not — unless it
		// died banned. Then the port is what the censor cut on this address,
		// and a relay minted on it again would be born dead: the repair that
		// retired it (retire_ports) would mint its own failure.
		if r.status == "dead" && r.diedReason != DeathIPBanned {
			continue
		}
		id, err := openIdentity(r.identityCT)
		if err != nil {
			return "", fmt.Errorf("entry: relay %s: %w", r.name, err)
		}
		used[id.Port] = true
	}
	ports := relayPorts
	if proto == ProtoVlessXhttp {
		// One relay at a time: the site and its Let's Encrypt validation
		// (TLS-ALPN-01) live on :443 of the box's one domain. A fresh relay
		// on the box replaces a dead one, it does not stand beside it.
		ports = relayPorts[:1]
	}
	port := 0
	for _, p := range ports {
		if !used[p] {
			port = p
			break
		}
	}
	if port == 0 {
		return "", fmt.Errorf("%w: box %s has a relay on every port", ErrRejected, box)
	}
	// Each relay on a box binds its own block of loopback ports.
	base := 10001 + relayLoopbackBlock*(n-1)
	var id Identity
	switch proto {
	case ProtoHysteria2:
		id, err = newVoiceIdentity(port, base)
	case ProtoVlessXhttp:
		var domain []byte
		if domain, err = cryptobox.Decrypt(lb.domainCT); err != nil {
			return "", fmt.Errorf("entry: box %s domain: %w", box, err)
		}
		id, err = newXhttpIdentity(port, base, string(domain))
	default:
		if id, err = newIdentity(port, base); err == nil {
			// A cover under its second chance, or struck by it, is not
			// borrowed by a new relay (П9.11).
			var covers coverLedger
			if covers, err = readCoverLedger(ctx, tx); err == nil {
				id.Covers = covers.forNewRelay()
			}
		}
	}
	if err != nil {
		return "", err
	}
	ct, err := sealIdentity(id)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%d", box, n)
	if err := s.repo.insertPendingRelay(ctx, tx, name, kind, box, ct); err != nil {
		return "", err
	}
	return name, tx.Commit(ctx)
}

// RelayIdentity opens a relay's identity — what its box runs with. Secrets:
// for the deploy path only, never for a response body.
func (s *Service) RelayIdentity(ctx context.Context, name string) (Identity, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Identity{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only
	st, err := s.repo.lockRelay(ctx, tx, name)
	if err != nil {
		return Identity{}, err
	}
	if st == nil {
		return Identity{}, fmt.Errorf("%w: %s", ErrNoSuchRelay, name)
	}
	return openIdentity(st.identityCT)
}

// RelayEnvelope is the signed record of an alive relay, byte for byte as a
// client receives it in its batch. For the operator's tools (the live voice
// harness, voice plan Ф3): it holds what a client holds — the relay's dialling
// credentials — and goes nowhere a client's batch would not.
func (s *Service) RelayEnvelope(ctx context.Context, name string) ([]byte, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only
	var ct []byte
	err = tx.QueryRow(ctx, selectAliveEnvelope, name).Scan(&ct)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no alive relay %s", ErrNoSuchRelay, name)
	}
	if err != nil {
		return nil, err
	}
	return cryptobox.Decrypt(ct)
}

// Admit signs a pending relay into the pool: from now on it is handed out.
// The caller has deployed it and seen it pass the admission probe.
func (s *Service) Admit(ctx context.Context, name string) error {
	return s.sign(ctx, name, true)
}

// Resign signs a relay that has a record again with a fresh lifetime — the
// box's address and the identity as they are now. Alive, or out of the pool
// for a rollout (testing, draining) or on suspicion (suspect): the record
// outlives the rollout and the suspicion — a machine back from its second
// chance is handed out the moment it returns — and the relay keeps its state. Grants stay: the batch does not change because the
// paperwork did.
func (s *Service) Resign(ctx context.Context, name string) error {
	return s.sign(ctx, name, false)
}

// recorded are the states that keep a relay's record (migration 044): the
// ones a re-sign renews.
func recorded(status string) bool {
	return status == StatusAlive || status == StatusTesting || status == StatusDraining || status == StatusSuspect
}

func (s *Service) sign(ctx context.Context, name string, admit bool) error {
	if s.signer == nil {
		return ErrNoSigner
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	st, err := s.repo.lockRelay(ctx, tx, name)
	if err != nil {
		return err
	}
	if st == nil || (admit && st.status != "pending") || (!admit && !recorded(st.status)) {
		want := "pending"
		if !admit {
			want = "alive, testing, draining or suspect"
		}
		return fmt.Errorf("%w: no %s relay %s", ErrNoSuchRelay, want, name)
	}
	id, err := openIdentity(st.identityCT)
	if err != nil {
		return fmt.Errorf("entry: relay %s: %w", name, err)
	}
	addr, err := cryptobox.Decrypt(st.addressCT)
	if err != nil {
		return fmt.Errorf("entry: box %s: %w", st.box, err)
	}
	now := s.now()
	issued, expires := now.Unix(), now.Add(RecordLifetime).Unix()
	env, err := s.signer.SignRelay(id.record(name, string(addr), st.region, issued, expires))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	ct, err := cryptobox.Encrypt(raw)
	if err != nil {
		return err
	}
	if err := s.repo.setRecord(ctx, tx, st.id, admit, env.KeyID, ct, time.Unix(issued, 0), time.Unix(expires, 0)); err != nil {
		return err
	}
	if admit {
		if err := s.bindNode(ctx, tx, st.id, id.proto(), st.region); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func sealIdentity(id Identity) ([]byte, error) {
	raw, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	return cryptobox.Encrypt(raw)
}

func openIdentity(ct []byte) (Identity, error) {
	raw, err := cryptobox.Decrypt(ct)
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	if err := json.Unmarshal(raw, &id); err != nil {
		return Identity{}, err
	}
	return id, nil
}

// List is the pool as the owner sees it: each relay's address (its box's),
// port and protocol opened from the seals.
func (s *Service) List(ctx context.Context) ([]PoolRelay, error) {
	relays, err := s.repo.list(ctx)
	if err != nil {
		return nil, err
	}
	boxes, err := s.openBoxes(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	addrOf := make(map[string]string, len(boxes))
	for _, b := range boxes {
		addrOf[b.Name] = b.Address
	}
	for i := range relays {
		id, err := openIdentity(relays[i].identityCT)
		if err != nil {
			// One relay the server key cannot open must not blind the owner to
			// the rest of the pool.
			applog.Error(ctx, "entry", "relay identity unreadable", applog.F("relay", relays[i].Name), applog.F("err", err.Error()))
			relays[i].Unreadable = true
			continue
		}
		relays[i].Address, relays[i].Port, relays[i].Proto = addrOf[relays[i].Box], id.Port, id.proto()
		if relays[i].Proto == ProtoVlessRealityVision {
			relays[i].Covers = append([]string(nil), id.Covers...)
		}
	}
	return relays, nil
}

// Boxes is the inventory as the owner sees it: every imported box, address
// opened, with the pool's relays that dial that address.
func (s *Service) Boxes(ctx context.Context) ([]PoolBox, error) {
	boxes, err := s.openBoxes(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	relays, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range boxes {
		boxes[i].Relays = []string{}
		for _, r := range relays {
			if r.Box == boxes[i].Name {
				boxes[i].Relays = append(boxes[i].Relays, r.Name)
			}
		}
	}
	return boxes, nil
}

func (s *Service) openBoxes(ctx context.Context, q db.DBTX) ([]PoolBox, error) {
	boxes, err := s.repo.boxes(ctx, q)
	if err != nil {
		return nil, err
	}
	for i := range boxes {
		addr, err := cryptobox.Decrypt(boxes[i].addressCT)
		if err != nil {
			applog.Error(ctx, "entry", "box address unreadable", applog.F("box", boxes[i].Name), applog.F("err", err.Error()))
			boxes[i].Unreadable = true
			continue
		}
		boxes[i].Address = string(addr)
		if boxes[i].domainCT != nil {
			d, err := cryptobox.Decrypt(boxes[i].domainCT)
			if err != nil {
				applog.Error(ctx, "entry", "box domain unreadable", applog.F("box", boxes[i].Name), applog.F("err", err.Error()))
				boxes[i].Unreadable = true
				continue
			}
			boxes[i].Domain = string(d)
		}
	}
	return boxes, nil
}

// PublishSources signs a source list (roadmap 42 П6.5) with a fresh version
// and makes it the one the hub serves. Newest wins by construction: the hub is
// the only signer, and it stamps now.
func (s *Service) PublishSources(ctx context.Context, sources []Source) (PoolSources, error) {
	if s.signer == nil {
		return PoolSources{}, ErrNoSigner
	}
	now := s.now()
	env, err := s.signer.SignSources(SourceList{
		Sources: sources, IssuedAt: now.Unix(), ExpiresAt: now.Add(RecordLifetime).Unix(),
	})
	if err != nil {
		return PoolSources{}, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return PoolSources{}, err
	}
	issued, expires := time.Unix(now.Unix(), 0), time.Unix(now.Add(RecordLifetime).Unix(), 0)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PoolSources{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if err := s.repo.upsertSources(ctx, tx, env.KeyID, raw, issued, expires); err != nil {
		return PoolSources{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PoolSources{}, err
	}
	// Every bundle carries the list, so every live session's answer moved.
	if s.notify != nil {
		s.notify.SourcesChanged()
	}
	return s.Sources(ctx)
}

// PoolSources is the current source list as the operator sees it: no addresses,
// just the version and lifetime. Present is false when none is published.
type PoolSources struct {
	Present   bool      `json:"present"`
	KeyID     string    `json:"keyId,omitempty"`
	IssuedAt  time.Time `json:"issuedAt,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	SignedAt  time.Time `json:"signedAt,omitempty"`
}

// Sources is the current source list for the operator surface.
func (s *Service) Sources(ctx context.Context) (PoolSources, error) {
	st, err := s.repo.sourcesStatus(ctx)
	if err != nil {
		return PoolSources{}, err
	}
	if st == nil {
		return PoolSources{Present: false}, nil
	}
	return PoolSources{
		Present:   true,
		KeyID:     st.keyID,
		IssuedAt:  st.issuedAt,
		ExpiresAt: st.expiresAt,
		SignedAt:  st.importedAt,
	}, nil
}

// MarkDead records a relay's death and why (a Death* reason, the dataset's
// label), and revokes every live grant of it, in one transaction; the next
// batch request of each holder refills that one slot, and the holders with a
// live session are told to make it now (П6.11). A pending relay can die too
// (a box handed back after a dirty admission probe). Manual until the probes
// of П6.6 call it.
func (s *Service) MarkDead(ctx context.Context, name, reason string) (revoked int64, err error) {
	if !validDeathReason(reason) {
		return 0, fmt.Errorf("%w: death reason %q", ErrRejected, reason)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	holders, ok, err := s.repo.markDead(ctx, tx, name, reason)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrNoSuchRelay
	}
	// A blocked domain is cured by a new domain, not a machine: when this
	// death empties the box of live relays, the box needs re-provisioning with
	// a fresh name (Ф6). Decide inside the transaction, act after it commits.
	var replaceBox string
	if reason == DeathDomainBlocked {
		box, survivors, err := s.repo.boxAndSurvivors(ctx, tx, name)
		if err != nil {
			return 0, err
		}
		if survivors == 0 {
			replaceBox = box
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	applog.Info(ctx, "entry", "relay dead",
		applog.F("relay", name), applog.F("reason", reason), applog.F("holders", len(holders)))
	if s.notify != nil && len(holders) > 0 {
		s.notify.RelaysChanged(holders)
	}
	if replaceBox != "" && s.replacer != nil {
		go s.replacer.ReplaceDomain(replaceBox)
	}
	return int64(len(holders)), nil
}

// Drain takes a box's alive relays out of the pool for a rollout (П6.14):
// status is "testing" on the canary and "draining" on the box next in line.
// Their live grants are revoked as drained in the same transaction, so the
// next batch of every holder fills that slot with another relay, and holders
// with a live session are told to ask now (П6.11). A client riding one of these
// relays moves its session onto the replacement before letting go of the old
// one (nekko-core handover); the box is empty once the last of them left
// (Provisioner.WaitEmpty). Returns how many relays left and how many holders
// were told.
func (s *Service) Drain(ctx context.Context, box, status string) (relays, holders int, err error) {
	return s.drain(ctx, box, status, nil)
}

// drain is Drain with `also` run in the same transaction, before the commit
// (drainForHandback marks the box leaving there).
func (s *Service) drain(ctx context.Context, box, status string, also func(pgx.Tx) error) (relays, holders int, err error) {
	if status != StatusTesting && status != StatusDraining {
		return 0, 0, fmt.Errorf("%w: drain to %q", ErrRejected, status)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if lb, ok, err := s.repo.lockBox(ctx, tx, box); err != nil {
		return 0, 0, err
	} else if !ok {
		return 0, 0, fmt.Errorf("%w: %s", ErrNoSuchBox, box)
	} else if lb.retired {
		return 0, 0, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	moved, held, err := s.repo.drainBox(ctx, tx, box, status)
	if err != nil {
		return 0, 0, err
	}
	if also != nil {
		if err := also(tx); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	if s.notify != nil && len(held) > 0 {
		s.notify.RelaysChanged(held)
	}
	return moved, len(held), nil
}

// Retire hands a box back (П6.10): every relay on it not dead yet dies with
// one reason, their live grants are revoked, and the box is marked retired —
// one transaction, so the pool loses the box at once, before the hub wipes
// the machine ([Provisioner.Retire]). Holders with a live session are told to
// refill now (П6.11). Returns the relays that died and how many holders were
// told.
func (s *Service) Retire(ctx context.Context, box, reason string) (relays []string, holders int, err error) {
	if !validDeathReason(reason) {
		return nil, 0, fmt.Errorf("%w: death reason %q", ErrRejected, reason)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	lb, ok, err := s.repo.lockBox(ctx, tx, box)
	if err != nil {
		return nil, 0, err
	} else if !ok {
		return nil, 0, fmt.Errorf("%w: %s", ErrNoSuchBox, box)
	} else if lb.retired {
		return nil, 0, fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	relays, held, err := s.repo.retireBox(ctx, tx, box, reason)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	applog.Info(ctx, "entry", "machine retired",
		applog.F("box", box), applog.F("reason", reason),
		applog.F("relays", relays), applog.F("holders", len(held)))
	if s.notify != nil && len(held) > 0 {
		s.notify.RelaysChanged(held)
	}
	return relays, len(held), nil
}

// ResignAhead is how long before expiry the hub renews a record. A week: the
// console turns a record amber at the same mark, so amber in the console
// means the renewal is overdue, not merely near.
const ResignAhead = 7 * 24 * time.Hour

// ResignExpiring renews every relay record and the source list that expire
// within [ResignAhead] (П6.10: the key is on the hub since П6.15, so the
// paperwork needs nobody). One failure does not stop the rest; the first error
// is returned after all were tried. Returns the relays renewed and whether
// the source list was.
func (s *Service) ResignExpiring(ctx context.Context) (relays []string, sources bool, err error) {
	if s.signer == nil {
		return nil, false, ErrNoSigner
	}
	before := s.now().Add(ResignAhead)
	names, err := s.repo.expiringRelays(ctx, before)
	if err != nil {
		return nil, false, err
	}
	var first error
	for _, n := range names {
		if rerr := s.Resign(ctx, n); rerr != nil {
			// Died or left between the list and the lock: nothing to renew.
			if !errors.Is(rerr, ErrNoSuchRelay) && first == nil {
				first = fmt.Errorf("entry: re-sign %s: %w", n, rerr)
			}
			continue
		}
		relays = append(relays, n)
	}
	env, expires, ok, err := s.repo.currentSources(ctx)
	switch {
	case err != nil:
		if first == nil {
			first = err
		}
	case ok && expires.Before(before):
		if serr := s.republishSources(ctx, env); serr != nil {
			if first == nil {
				first = fmt.Errorf("entry: re-sign sources: %w", serr)
			}
		} else {
			sources = true
		}
	}
	return relays, sources, first
}

// republishSources signs the stored source list again with a fresh lifetime:
// the same sources, a new version.
func (s *Service) republishSources(ctx context.Context, raw []byte) error {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: stored source list: %v", ErrMalformed, err)
	}
	payload, err := openEnvelope(env, []Key{s.signer.Key()}, sourcesContext)
	if err != nil {
		return err
	}
	var l SourceList
	if err := json.Unmarshal(payload, &l); err != nil {
		return fmt.Errorf("%w: stored source list: %v", ErrMalformed, err)
	}
	_, err = s.PublishSources(ctx, l.Sources)
	return err
}

// ResignInterval is how often the hub looks for records to renew.
const ResignInterval = time.Hour

// RunResigner renews expiring records now and then every [ResignInterval]
// until ctx ends. Without a signing key it does nothing: the console's amber
// says so.
func (s *Service) RunResigner(ctx context.Context) {
	if s.signer == nil {
		return
	}
	t := time.NewTicker(ResignInterval)
	defer t.Stop()
	for {
		relays, sources, err := s.ResignExpiring(ctx)
		if len(relays) > 0 || sources {
			applog.Info(ctx, "entry", "records re-signed", applog.F("relays", relays), applog.F("sources", sources))
		}
		if err != nil && ctx.Err() == nil {
			applog.Error(ctx, "entry", "re-sign failed", applog.F("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Restore puts a drained box's relays back in the pool. Their records were
// kept, so they are handed out again at once; nobody's batch moves for it —
// a relay coming back fills only slots that are empty.
func (s *Service) Restore(ctx context.Context, box string) ([]string, error) {
	return s.repo.restoreBox(ctx, box)
}

// Relay states a rollout moves relays through (migration 044), beside the two
// a relay is in the rest of the time (migration 036).
const (
	StatusAlive    = "alive"
	StatusDead     = "dead"
	StatusTesting  = "testing"
	StatusDraining = "draining"
)

// GrantMeta is what the hub observed when it made a grant — the dataset's
// features (plan 42 §9). Sealed with the server key; erased with the account.
// The caller supplies what only the connection knows; the service adds the
// account's age itself.
type GrantMeta struct {
	IP             string    `json:"ip,omitempty"`
	AccountCreated time.Time `json:"account_created,omitzero"`
}

// servedRecord is one record on its way out: the envelope JSON exactly as it
// was stored, and when it stops being valid.
type servedRecord struct {
	raw       []byte
	expiresAt time.Time
}

// BatchFor returns the account's batch: the signed records of its personal
// relays, filling empty slots first.
//
// The batch IS the set of live grants — the registry, not a formula, is what
// an account holds. The keyed hash only decides what fills a slot, and a slot
// is empty only on the first request or after its relay died. So a relay
// added to the pool reshuffles nobody, and a death changes exactly the one
// slot it emptied: replacement happens on death and on nothing else.
//
// A granted relay whose record has expired (the owner has not re-signed it
// yet) is not served, but its slot is not refilled either: an expiry is
// paperwork, not a death, and refilling would hand out a new address for no
// reason.
func (s *Service) BatchFor(ctx context.Context, account uuid.UUID, meta GrantMeta) ([]Envelope, error) {
	served, err := s.serve(ctx, account, meta)
	if err != nil {
		return nil, err
	}
	out := make([]Envelope, 0, len(served))
	for _, r := range served {
		var env Envelope
		if err := json.Unmarshal(r.raw, &env); err != nil {
			return nil, fmt.Errorf("entry: stored envelope: %w", err)
		}
		out = append(out, env)
	}
	return out, nil
}

// serve is BatchFor down to the stored bytes (see BatchFor).
func (s *Service) serve(ctx context.Context, account uuid.UUID, meta GrantMeta) ([]servedRecord, error) {
	now := s.now()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	if err := s.repo.lockBatch(ctx, tx, account); err != nil {
		return nil, err
	}
	held, err := s.repo.activeGrants(ctx, tx, account)
	if err != nil {
		return nil, err
	}

	// Asked every time, not only when the stream lane is short: a voice
	// region the account holds nothing of yet shows up only among the
	// candidates. One read of the alive pool under the batch lock.
	cands, err := s.repo.candidates(ctx, tx, account, now)
	if err != nil {
		return nil, err
	}
	if picked := s.pick(account, held, cands); len(picked) > 0 {
		triggers, err := s.refillTriggers(ctx, tx, account, picked)
		if err != nil {
			return nil, err
		}
		if meta.AccountCreated.IsZero() {
			if meta.AccountCreated, err = s.repo.accountCreated(ctx, tx, account); err != nil {
				return nil, err
			}
		}
		metaCT, err := sealMeta(meta)
		if err != nil {
			return nil, err
		}
		for i, c := range picked {
			if err := s.repo.insertGrant(ctx, tx, account, c.id, triggers[i], metaCT); err != nil {
				return nil, err
			}
		}
		if held, err = s.repo.activeGrants(ctx, tx, account); err != nil {
			return nil, err
		}
	}

	out := make([]servedRecord, 0, len(held))
	for _, g := range held {
		if !g.expiresAt.After(now) {
			continue
		}
		raw, err := cryptobox.Decrypt(g.envelopeCT)
		if err != nil {
			return nil, fmt.Errorf("entry: open relay %d: %w", g.relayID, err)
		}
		out = append(out, servedRecord{raw: raw, expiresAt: g.expiresAt})
	}
	return out, tx.Commit(ctx)
}

// VoiceSlotsPerRegion is how many voice entries of each region an account
// holds. One: a call in a region rides one entry, and a dead one is replaced
// like any other slot. The stream lane keeps the service's batch size.
const VoiceSlotsPerRegion = 1

// slots is how many relays an account holds in a lane.
func (s *Service) slots(l lane) int {
	if l.proto == ProtoHysteria2 {
		return VoiceSlotsPerRegion
	}
	return s.batchSize
}

// pick fills each lane's empty slots from its candidates, ranked per account
// (see rank): the stream lane first, then the voice lanes in region order —
// a deterministic order, so the i-th pick's trigger means the same thing on
// every run.
func (s *Service) pick(account uuid.UUID, held []grantedRelay, cands []candidate) []candidate {
	have := map[lane]int{}
	for _, g := range held {
		have[g.lane]++
	}
	byLane := map[lane][]candidate{}
	for _, c := range cands {
		byLane[c.lane] = append(byLane[c.lane], c)
	}
	lanes := make([]lane, 0, len(byLane))
	for l := range byLane {
		lanes = append(lanes, l)
	}
	sort.Slice(lanes, func(i, j int) bool {
		a, b := lanes[i], lanes[j]
		if (a.proto == ProtoHysteria2) != (b.proto == ProtoHysteria2) {
			return b.proto == ProtoHysteria2
		}
		if a.proto != b.proto {
			return a.proto < b.proto
		}
		return a.region < b.region
	})
	var out []candidate
	for _, l := range lanes {
		missing := s.slots(l) - have[l]
		if missing <= 0 {
			continue
		}
		// One relay per machine: two relays of one box are one address,
		// and a batch is there to survive an address going.
		boxes := map[string]bool{}
		for _, c := range s.rank(account, byLane[l]) {
			if missing == 0 {
				break
			}
			if boxes[c.box] {
				continue
			}
			boxes[c.box] = true
			out = append(out, c)
			missing--
		}
	}
	return out
}

// refillTriggers says why each slot about to be filled is empty, one trigger
// per pick, lane by lane. A lane's first fill is initial — including a voice
// region that appears after the account's stream relays: the triggers label
// the censor-score dataset (plan 42 §9), and "replaced a dead relay" there
// would be a lie. After that a slot emptied by a drain is a drain replacement
// and every other one a death replacement.
func (s *Service) refillTriggers(ctx context.Context, tx pgx.Tx, account uuid.UUID, picked []candidate) ([]string, error) {
	out := make([]string, len(picked))
	nth := map[lane]int{}
	type history struct {
		ever    bool
		reasons []string
	}
	seen := map[lane]history{}
	for i, c := range picked {
		h, ok := seen[c.lane]
		if !ok {
			ever, err := s.repo.everGranted(ctx, tx, account, c.lane)
			if err != nil {
				return nil, err
			}
			h.ever = ever
			if ever {
				if h.reasons, err = s.repo.unrefilledReasons(ctx, tx, account, c.lane); err != nil {
					return nil, err
				}
			}
			seen[c.lane] = h
		}
		k := nth[c.lane]
		nth[c.lane]++
		switch {
		case !h.ever:
			out[i] = triggerInitial
		case k < len(h.reasons) && h.reasons[k] == revokeDrained:
			out[i] = triggerDrainReplacement
		default:
			out[i] = triggerDeathReplacement
		}
	}
	return out, nil
}

// Bundle is what one `get_entry_bundle` answers (roadmap 42 П6.3).
type Bundle struct {
	// Unchanged: the caller's version is current; nothing else is set.
	Unchanged bool
	Version   string
	// Relays are envelope JSON, byte for byte as stored.
	Relays [][]byte
	// ExpiresAt is the earliest record expiry; zero with no relays.
	ExpiresAt time.Time
	// Sources is the signed source-list envelope the client adopts for its next
	// cold start (roadmap 42 П6.5); nil when the hub holds none or the one it
	// holds has expired. Folded into Version, so re-signing the list re-ships it
	// even when the relay set did not move.
	Sources []byte
}

// BundleFor answers `get_entry_bundle`: the account's batch (BatchFor, slots
// filled as needed) and the current source list, or Unchanged when haveVersion
// already names both.
func (s *Service) BundleFor(ctx context.Context, account uuid.UUID, meta GrantMeta, haveVersion string) (Bundle, error) {
	served, err := s.serve(ctx, account, meta)
	if err != nil {
		return Bundle{}, err
	}
	sources, err := s.currentSources(ctx)
	if err != nil {
		return Bundle{}, err
	}
	version := bundleVersion(served, sources)
	if haveVersion != "" && haveVersion == version {
		return Bundle{Unchanged: true, Version: version}, nil
	}
	b := Bundle{Version: version, Relays: make([][]byte, len(served)), Sources: sources}
	for i, r := range served {
		b.Relays[i] = r.raw
		if b.ExpiresAt.IsZero() || r.expiresAt.Before(b.ExpiresAt) {
			b.ExpiresAt = r.expiresAt
		}
	}
	return b, nil
}

// currentSources is the source-list envelope the hub serves, or nil when it
// holds none or the one it holds has expired — the hub does not ship a list it
// already knows the client would reject.
func (s *Service) currentSources(ctx context.Context) ([]byte, error) {
	envelope, expiresAt, ok, err := s.repo.currentSources(ctx)
	if err != nil || !ok {
		return nil, err
	}
	if !expiresAt.After(s.now()) {
		return nil, nil
	}
	return envelope, nil
}

// bundleVersion names the exact set of records served and the source list that
// rode with them — so it changes when a relay is replaced or re-signed, or when
// the source list is re-signed, and only then. Order-independent over relays:
// the set is what the client holds, not the order the rows came back in.
func bundleVersion(served []servedRecord, sources []byte) string {
	digests := make([][]byte, len(served))
	for i, r := range served {
		d := sha256.Sum256(r.raw)
		digests[i] = d[:]
	}
	sort.Slice(digests, func(i, j int) bool { return bytes.Compare(digests[i], digests[j]) < 0 })
	h := sha256.New()
	h.Write([]byte("nekko-entry/bundle/v1\n"))
	for _, d := range digests {
		h.Write(d)
	}
	// A fixed-width digest of the source list keeps "no list" (a constant) clear
	// of any list, and its length out of the relay digests above.
	sd := sha256.Sum256(sources)
	h.Write(sd[:])
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// rank orders candidates by rendezvous hash: HMAC(batchKey, account ‖ name),
// highest first. Deterministic per account, different across accounts — so
// batches are combinations of the pool, not a few shared groups — and keyed,
// so nobody without the server key can compute them.
func (s *Service) rank(account uuid.UUID, cands []candidate) []candidate {
	type scored struct {
		c     candidate
		score []byte
	}
	list := make([]scored, len(cands))
	for i, c := range cands {
		m := hmac.New(sha256.New, s.batchKey)
		m.Write(account[:])
		m.Write([]byte{0})
		m.Write([]byte(c.name))
		list[i] = scored{c, m.Sum(nil)}
	}
	sort.Slice(list, func(i, j int) bool { return bytes.Compare(list[i].score, list[j].score) > 0 })
	out := make([]candidate, len(list))
	for i, l := range list {
		out[i] = l.c
	}
	return out
}

func sealMeta(meta GrantMeta) ([]byte, error) {
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return cryptobox.Encrypt(raw)
}

// RelayFace is what the console may show of a relay's sealed identity: how it
// looks from outside. Never a key, a short id or a path.
type RelayFace struct {
	Port   int
	Proto  string
	Covers []string
}

// OpenRelayFace opens a relay's identity_ct far enough to say how the relay
// looks from outside; the secrets in it stay in this package.
func OpenRelayFace(identityCT []byte) (RelayFace, error) {
	id, err := openIdentity(identityCT)
	if err != nil {
		return RelayFace{}, err
	}
	f := RelayFace{Port: id.Port, Proto: id.proto(), Covers: id.Covers}
	if id.Xhttp != nil && id.Xhttp.Domain != "" {
		f.Covers = []string{id.Xhttp.Domain}
	}
	return f, nil
}
