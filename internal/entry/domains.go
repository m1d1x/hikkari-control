package entry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// Errors the domain acquirer returns when it cannot give a box a domain.
var (
	// ErrNoRegistrar: the pool is dry and no registrar is wired, so the hub
	// cannot buy a replacement.
	ErrNoRegistrar = errors.New("entry: no domain registrar wired")
	// ErrBudget: a purchase would break a spend group's day or month cap
	// (spend.go), or the registrar's own pre-flight said it would not go
	// through.
	ErrBudget = errors.New("entry: domain purchase over budget")
	// ErrNoCandidate: no available name the budget has room for was found in
	// the candidate space (dictionary × TLDs) within the attempt limit.
	ErrNoCandidate = errors.New("entry: no affordable domain candidate")
	// ErrDomainUnavailable: the registrar will not register that name now
	// (taken, reserved, or its TLD is not sold through the API).
	ErrDomainUnavailable = errors.New("entry: domain not available")
)

// DomainPolicy is the candidate space the hub draws a relay's domain from
// (Ф6). What a purchase may cost is the registrar group's day and month
// (spend.go) — the hub's half of the budget gate; the registrar's dry-run
// (funds, its own monthly limit) is the other half. The TLDs are the registrar
// account's, saved by the owner in the console (П9.1, entry_providers).
type DomainPolicy struct {
	// TLDs are the cheap, API-registerable top-level domains to try, in order.
	TLDs []string
	// Words is the dictionary a candidate name is built from: one word plus a
	// few digits, neutral and never tied to the brand (П4б rule).
	Words []string
}

// domainBuyer is the registrar and the policy a purchase runs under, read once
// per purchase so a console save mid-flight cannot mix two accounts' answers.
type domainBuyer struct {
	reg    DomainRegistrar
	policy DomainPolicy
	// kind is the saved account the buyer came from; empty for one wired by
	// code (a test), whose policy is then the one it was wired with.
	kind string
}

// domainCandidateAttempts bounds how many names the acquirer will quote before
// giving up, so a saturated candidate space or a registrar outage fails the
// job with ErrNoCandidate instead of looping forever. Each miss is one
// checkDomain call, rate-limited to ten per ten seconds.
const domainCandidateAttempts = 24

// checkSpacing keeps the acquirer under Porkbun's checkDomain rate limit (ten
// per ten seconds): one quote per second leaves margin.
const checkSpacing = time.Second

// defaultDomainWords is the neutral name dictionary. Short, pronounceable,
// nothing that reads as a brand or a VPN (П4б: the domain is a throwaway that
// must look like any small site).
var defaultDomainWords = []string{
	"cedar", "maple", "harbor", "quartz", "otter", "amber", "ridge", "delta",
	"willow", "pine", "cove", "marsh", "heron", "slate", "birch", "fjord",
	"meadow", "larch", "basin", "cirrus", "pebble", "cedarly", "thistle", "reef",
}

// defaultDomainTLDs are cheap, API-registerable TLDs (first-year prices around
// $1.3–2.0 on 2026-10-21). The registrar's Registerable check is still applied
// per TLD before any purchase; this list is the cost preference, tried in order.
var defaultDomainTLDs = []string{"lol", "top", "mom", "autos"}

// DefaultDomainPolicy is the policy of a registrar account that saved no
// TLDs.
func DefaultDomainPolicy() DomainPolicy {
	return DomainPolicy{
		TLDs:  append([]string(nil), defaultDomainTLDs...),
		Words: defaultDomainWords,
	}
}

// SetDomainRegistrar swaps the registrar and the purchase policy. LoadProviders
// calls it from the saved accounts; a nil registrar makes the hub pool-only:
// it draws domains from the pool and AcquireDomain fails with ErrNoRegistrar
// when the pool is dry.
func (s *Service) SetDomainRegistrar(r DomainRegistrar, p DomainPolicy) {
	s.buyer.Store(&domainBuyer{reg: r, policy: p})
}

// setSavedRegistrar is SetDomainRegistrar for a saved account.
func (s *Service) setSavedRegistrar(kind string, r DomainRegistrar, p DomainPolicy) {
	s.buyer.Store(&domainBuyer{reg: r, policy: p, kind: kind})
}

// currentBuyer is the registrar and policy in force now; never nil.
func (s *Service) currentBuyer() *domainBuyer {
	if b := s.buyer.Load(); b != nil {
		return b
	}
	return &domainBuyer{policy: DefaultDomainPolicy()}
}

// SetDomainReplacer wires the auto-replacement of a blocked domain. Called once
// at startup; left unset, a domain_blocked death waits for a hand.
func (s *Service) SetDomainReplacer(r DomainReplacer) { s.replacer = r }

// AcquireDomain gives box a domain whose A record points at ip, without a
// human: a free name from the pool when one is there, otherwise a freshly
// bought cheap name (Ф6). The domain ends assigned to box in the ledger and
// resolving to ip at the registrar. bought reports whether money was spent
// (for the job log and the admin journal).
//
// The caller seals the returned domain onto the box and waits for public
// resolvers before deploying; AcquireDomain itself only owns and aims the name.
//
// # The purchase is a spend like any other
//
// A dry pool used to have any provision of an XHTTP box buy a name, past the
// mode and the pause the owner set for buying domains (audit 42 recheck З2).
// Owner's decision 27.10: provisioning stays an operator's right, but the
// purchase inside it obeys buy_domain's mode — only `auto` buys without a
// person pressing that repair — and the pause of the domains' spend group.
// Taking a name from the pool is free and needs neither.
func (s *Service) AcquireDomain(ctx context.Context, box, ip string) (domain string, bought bool, err error) {
	b := s.currentBuyer()
	// Pool first: a name we already own costs nothing.
	claimed, ok, err := s.claimPooledDomain(ctx, b, box, ip)
	if err != nil || ok {
		return claimed, false, err
	}
	if b.reg == nil {
		return "", false, ErrNoRegistrar
	}
	if why, err := s.domainSpendHeld(ctx); err != nil {
		return "", false, err
	} else if why != "" {
		return "", false, fmt.Errorf("%w: пул доменов пуст, а %s", ErrRejected, why)
	}
	d, err := s.buyDomain(ctx, b, box, ip)
	if err != nil {
		return "", false, err
	}
	return d, true, nil
}

// domainSpendHeld says why a purchase outside the buy_domain repair must not
// happen now, empty when it may: the owner's mode for buy_domain is not
// `auto`, or a pause stands on a cause of the domains' spend group.
func (s *Service) domainSpendHeld(ctx context.Context) (string, error) {
	modes, err := LoadPlaybookModes(ctx, s.pool)
	if err != nil {
		return "", err
	}
	if p := modes.For(CauseShortOfDomains); p.Mode != ModeAuto {
		return fmt.Sprintf("покупка домена в режиме %q — купи имя в консоли или включи auto для %s", p.Mode, PlaybookBuyDomain), nil
	}
	pause, paused, err := s.anyCausePaused(ctx, pauseCauses(CauseShortOfDomains, PlaybookBuyDomain))
	if err != nil {
		return "", err
	}
	if paused {
		return "траты на домены на паузе: " + pause.Reason, nil
	}
	return "", nil
}

// claimPooledDomain gives box a free name from the pool and aims it at ip,
// buying nothing; ok is false when the pool is dry. The eviction of a blocked
// domain (evict_domain) takes its replacement this way and no other.
//
// The claim is its own transaction so a crash between the claim and the DNS
// update leaves the name assigned (recoverable) rather than lost.
func (s *Service) claimPooledDomain(ctx context.Context, b *domainBuyer, box, ip string) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	claimed, ok, err := s.repo.claimFreeDomain(ctx, tx, box)
	if err != nil || !ok {
		return "", false, err
	}
	// Seal the name onto the box in the same transaction as the claim, so
	// the box and the ledger never disagree on which domain it carries.
	if err := s.sealDomainOnBox(ctx, tx, box, claimed); err != nil {
		return "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	// A pooled name may still point at a box it used to serve; re-aim it.
	// A pool-only hub (no registrar) leaves a hand-seeded name's A record
	// as the owner set it.
	if b.reg != nil {
		if err := b.reg.SetARecord(ctx, claimed, ip); err != nil {
			return "", false, fmt.Errorf("entry: aim pooled domain %s: %w", claimed, err)
		}
	}
	return claimed, true, nil
}

// buyDomain finds an affordable available name, rehearses and buys it, assigns
// it to box and aims it at ip. Each candidate is one checkDomain; the budget
// gate (the registrar group's caps + the registrar's dry-run) runs before any
// charge.
//
// A quote the budget has no room for is passed over for a cheaper one, not
// refused: the TLDs are a preference, and a day with $1 left may still buy a
// $0.90 name. The room is read once, before the quotes; the purchase reads
// it again under its lock.
func (s *Service) buyDomain(ctx context.Context, b *domainBuyer, box, ip string) (string, error) {
	// A purchase nobody heard the answer to is settled before anything new is
	// bought: it may well be a name the hub already paid for (В4).
	if name, ok, err := s.replayUnknownPurchases(ctx, b, box, ip); err != nil || ok {
		return name, err
	}
	p := b.policy
	excluded, err := s.exclusionSet(ctx)
	if err != nil {
		return "", err
	}
	spend, err := s.spendOf(ctx, s.pool, SpendRegistrar)
	if err != nil {
		return "", err
	}
	room := spend.RoomCents()
	if room <= 0 {
		return "", fmt.Errorf("%w: на домены места нет — за день %s из %s, за месяц %s из %s",
			ErrBudget, money(spend.DayCents), money(spend.DailyCents), money(spend.MonthCents), money(spend.MonthlyCents))
	}
	registerable := map[string]bool{}
	tried := 0
	// dearest is the cheapest price the budget had no room for: a TLD costs
	// one price, so once a name of it is too dear the TLD is passed over.
	dearest := 0
	for _, tld := range p.TLDs {
		reg, seen := registerable[tld]
		if !seen {
			ok, err := b.reg.Registerable(ctx, tld)
			if err != nil {
				return "", fmt.Errorf("entry: tld %s registerable: %w", tld, err)
			}
			registerable[tld], reg = ok, ok
		}
		if !reg {
			continue
		}
		for i := 0; i < len(p.Words) && tried < domainCandidateAttempts; i++ {
			if err := s.paceQuote(ctx, tried); err != nil {
				return "", err
			}
			tried++
			name, err := candidateName(p.Words, tld)
			if err != nil {
				return "", err
			}
			if e, _ := excluded.excludedBy(name); e != "" {
				continue
			}
			q, err := b.reg.Quote(ctx, name)
			if err != nil {
				return "", fmt.Errorf("entry: quote %s: %w", name, err)
			}
			if !q.Available || q.CostCents <= 0 {
				continue
			}
			if q.CostCents > room {
				if dearest == 0 || q.CostCents < dearest {
					dearest = q.CostCents
				}
				break
			}
			ok, err := s.purchase(ctx, b, box, ip, name, tld, q.CostCents)
			if err != nil {
				return "", err
			}
			if ok {
				return name, nil
			}
		}
	}
	if dearest > 0 {
		return "", fmt.Errorf("%w: дешевле %s имени нет, а на домены осталось %s", ErrBudget, money(dearest), money(room))
	}
	return "", ErrNoCandidate
}

// paceQuote waits out the registrar's quote rate limit before every quote but
// the first.
func (s *Service) paceQuote(ctx context.Context, tried int) error {
	if tried == 0 || s.checkSpacing <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.checkSpacing):
		return nil
	}
}

// purchase runs the budget gate and, if it clears, buys name. With a box it is
// then assigned to box and aimed at ip; with box empty it lands free in the
// pool (an owner's purchase from the console, П9.1). ok is false (no error)
// when the gate declined this name for a reason the next candidate might pass
// (the registrar would not sell it); a hard failure — over budget, no funds —
// returns an error so the job stops rather than burning through candidates.
func (s *Service) purchase(ctx context.Context, b *domainBuyer, box, ip, name, tld string, costCents int) (ok bool, err error) {
	// Registrar pre-flight: funds and its own monthly limit.
	pre, err := b.reg.DryRun(ctx, name, costCents)
	if err != nil {
		return false, fmt.Errorf("entry: dry-run %s: %w", name, err)
	}
	if !pre.SufficientFunds {
		return false, fmt.Errorf("%w: registrar reports insufficient funds (balance %d)", ErrBudget, pre.BalanceCents)
	}
	if !pre.WithinMonthlyLimit {
		return false, fmt.Errorf("%w: over the registrar's own monthly limit", ErrBudget)
	}
	if !pre.WouldSucceed {
		return false, fmt.Errorf("%w: registrar pre-flight declined %s", ErrBudget, name)
	}

	// The registrar group's caps and the purchase intent, reserved
	// atomically: an advisory lock serializes every purchase on this
	// database, so two purchases at once — or the hub and the CLI — cannot
	// both read the same spend and both slip under a cap (READ COMMITTED
	// alone would let them). The caps are the saved ones, read under the
	// lock. The idempotency
	// key is persisted before the charge: an answer that never arrives
	// leaves the row `unknown` with its key, and the next purchase sends the
	// same name with the same key, which the registrar answers from its
	// replay (replayUnknownPurchases).
	key, err := idempotencyKey(name)
	if err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	if err := s.repo.lockDomainPurchases(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		return false, err
	}
	if err := s.domainAffordable(ctx, tx, costCents); err != nil {
		_ = tx.Rollback(ctx)
		return false, err
	}
	if err := s.repo.insertBuyingDomain(ctx, tx, name, tld, key, costCents); err != nil {
		_ = tx.Rollback(ctx)
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}

	order, buyErr := b.reg.Buy(ctx, name, costCents, key)
	if buyErr != nil {
		return false, s.purchaseUnanswered(ctx, name, buyErr)
	}
	return s.settlePurchase(ctx, b, name, order.OrderID, box, ip)
}

// purchaseUnanswered records a Buy that did not come back with an order.
//
// A refusal the registrar owns (ErrProviderRefused: funds, availability) is
// terminal and charged nothing: the row is `failed`, kept for audit, never
// drawn again. Anything else — a timeout, a reset, a body that would not
// parse — is an answer that did not arrive, and the money may be gone: the
// row is `unknown`, stays in the month's spend, and keeps its key for the
// replay (В4).
func (s *Service) purchaseUnanswered(ctx context.Context, name string, buyErr error) error {
	if errors.Is(buyErr, ErrProviderRefused) {
		s.recordDomainFailure(ctx, name, buyErr)
		return fmt.Errorf("entry: buy %s: %w", name, buyErr)
	}
	if _, err := s.pool.Exec(context.WithoutCancel(ctx), markDomainUnknown, name, clip(buyErr.Error(), 512)); err != nil {
		// The row stays `buying`, which the spend counts too.
		return fmt.Errorf("entry: buy %s: %w (and not recorded: %v)", name, buyErr, err)
	}
	return fmt.Errorf("entry: buy %s: ответ регистратора не получен — покупка считается в бюджете и будет повторена с тем же ключом: %w", name, buyErr)
}

// settlePurchase records a purchase the registrar answered with an order:
// onto box (sealed, aimed at ip) when there is one, free in the pool when
// not. ok is false when another caller settled the same row first — a replay
// racing a replay — which is not this caller's name to use.
func (s *Service) settlePurchase(ctx context.Context, b *domainBuyer, name, orderID, box, ip string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	settled, err := s.repo.settleDomain(ctx, tx, name, orderID, box)
	if err != nil || !settled {
		return false, err
	}
	if box != "" {
		if err := s.sealDomainOnBox(ctx, tx, box, name); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if box == "" {
		return true, nil
	}
	if err := b.reg.SetARecord(ctx, name, ip); err != nil {
		return false, fmt.Errorf("entry: aim bought domain %s: %w", name, err)
	}
	return true, nil
}

// idempotencyKeyLife is how long the registrar replays a key's answer:
// Porkbun keeps it 24 h. An hour short of that, so a replay is never sent
// with a key the registrar may already have forgotten — which it would read
// as a new purchase.
const idempotencyKeyLife = 23 * time.Hour

// replayUnknownPurchases settles every purchase whose answer never came,
// oldest first, before anything new is bought. ok is true when one of them
// turned out bought and now serves box (or the pool, with box empty).
//
//   - within the key's life: Buy again, same name, same key, same price. The
//     registrar replays its first answer — the order, if it charged; its
//     refusal, if it did not — and never charges twice.
//   - past it: the key cannot be trusted to replay, so the registrar's own
//     account is asked instead. On it — bought, the answer was lost; not on
//     it — it never charged.
//
// A replay that itself gets no answer stops here with the error: while the
// hub cannot hear the registrar, buying another name would be guessing with
// money.
func (s *Service) replayUnknownPurchases(ctx context.Context, b *domainBuyer, box, ip string) (string, bool, error) {
	rows, err := s.pool.Query(ctx, selectUnknownPurchases)
	if err != nil {
		return "", false, fmt.Errorf("entry: unknown purchases: %w", err)
	}
	type unknown struct {
		name, key string
		cost      int
		at        time.Time
	}
	var due []unknown
	for rows.Next() {
		var u unknown
		if err := rows.Scan(&u.name, &u.key, &u.cost, &u.at); err != nil {
			rows.Close()
			return "", false, err
		}
		due = append(due, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	var listed []RegistrarDomain
	for _, u := range due {
		if s.now().Sub(u.at) > idempotencyKeyLife {
			if listed == nil {
				if listed, err = b.reg.ListDomains(ctx); err != nil {
					return "", false, fmt.Errorf("entry: list account domains: %w", err)
				}
			}
			if !slices.ContainsFunc(listed, func(d RegistrarDomain) bool { return d.Domain == u.name }) {
				s.recordDomainFailure(ctx, u.name, errors.New("ответа не было, а на аккаунте регистратора имени нет — не списано"))
				continue
			}
			if ok, err := s.settlePurchase(ctx, b, u.name, "", box, ip); err != nil || ok {
				return u.name, ok, err
			}
			continue
		}
		order, err := b.reg.Buy(ctx, u.name, u.cost, u.key)
		if err != nil {
			if errors.Is(err, ErrProviderRefused) {
				s.recordDomainFailure(ctx, u.name, err)
				continue
			}
			return "", false, fmt.Errorf("entry: replay of %s: ответ регистратора снова не получен: %w", u.name, err)
		}
		if ok, err := s.settlePurchase(ctx, b, u.name, order.OrderID, box, ip); err != nil || ok {
			return u.name, ok, err
		}
	}
	return "", false, nil
}

// ── The owner's purchases from the console (П9.1) ─────────────────────────

// DomainOffer is one name the registrar quoted, judged against the registrar
// group's caps.
type DomainOffer struct {
	Domain    string `json:"domain"`
	TLD       string `json:"tld"`
	Available bool   `json:"available"`
	CostCents int    `json:"costCents"`
	// OverCap: dearer than what is left of today's or this month's domain
	// budget; the hub will not buy it now.
	OverCap bool `json:"overCap"`
}

// DomainCheckout is everything the owner sees before pressing «buy»: the
// fresh quote, the registrar's dry-run and the hub's month against its cap.
type DomainCheckout struct {
	DomainOffer
	WouldSucceed         bool `json:"wouldSucceed"`
	SufficientFunds      bool `json:"sufficientFunds"`
	WithinRegistrarLimit bool `json:"withinRegistrarLimit"`
	BalanceCents         int  `json:"balanceCents"`
	// Spend is the registrar group against its caps, before this purchase.
	Spend SpendReading `json:"spend"`
	// WithinHubCaps: the price fits under the day's and the month's caps.
	WithinHubCaps bool `json:"withinHubCaps"`
}

// DomainSpend is the registrar group's spend against its caps.
type DomainSpend struct {
	SpendReading
	// Registrar is the kind domains are bought through; empty = pool only.
	Registrar string `json:"registrar"`
}

// DomainSpendNow reads the registrar group's spend from the ledger and the
// caps in force.
func (s *Service) DomainSpendNow(ctx context.Context) (DomainSpend, error) {
	b := s.currentBuyer()
	spend, err := s.spendOf(ctx, s.pool, SpendRegistrar)
	if err != nil {
		return DomainSpend{}, err
	}
	out := DomainSpend{SpendReading: spend}
	if b.reg != nil {
		if k := s.activeRegistrar.Load(); k != nil {
			out.Registrar = *k
		}
	}
	return out, nil
}

// SuggestDomains quotes up to n fresh candidate names, one TLD after another
// from the policy, and returns the available ones. Each is one rate-limited
// quote, so n stays small.
func (s *Service) SuggestDomains(ctx context.Context, n int) ([]DomainOffer, error) {
	b := s.currentBuyer()
	if b.reg == nil {
		return nil, ErrNoRegistrar
	}
	if n <= 0 || n > 6 {
		n = 3
	}
	out := []DomainOffer{}
	tried := 0
	for _, tld := range b.policy.TLDs {
		if len(out) >= n || tried >= domainCandidateAttempts {
			break
		}
		ok, err := b.reg.Registerable(ctx, tld)
		if err != nil {
			return nil, fmt.Errorf("entry: tld %s registerable: %w", tld, err)
		}
		if !ok {
			continue
		}
		// Up to two tries per TLD: a random name is almost always free.
		for i := 0; i < 2 && len(out) < n; i++ {
			if err := s.paceQuote(ctx, tried); err != nil {
				return nil, err
			}
			tried++
			name, err := candidateName(b.policy.Words, tld)
			if err != nil {
				return nil, err
			}
			offer, err := s.offer(ctx, b, name)
			if err != nil {
				return nil, err
			}
			if offer.Available {
				out = append(out, offer)
				break
			}
		}
	}
	return out, nil
}

// QuoteDomain quotes one name the owner typed.
func (s *Service) QuoteDomain(ctx context.Context, name string) (DomainOffer, error) {
	b := s.currentBuyer()
	if b.reg == nil {
		return DomainOffer{}, ErrNoRegistrar
	}
	name, err := ownerDomainName(name)
	if err != nil {
		return DomainOffer{}, err
	}
	if err := s.checkNotExcluded(ctx, name); err != nil {
		return DomainOffer{}, err
	}
	ok, err := b.reg.Registerable(ctx, tldOf(name))
	if err != nil {
		return DomainOffer{}, fmt.Errorf("entry: tld %s registerable: %w", tldOf(name), err)
	}
	if !ok {
		return DomainOffer{Domain: name, TLD: tldOf(name)}, nil
	}
	return s.offer(ctx, b, name)
}

// CheckoutDomain is the dry run of buying name into the pool: a fresh quote,
// the registrar's pre-flight and the hub's caps. It spends nothing.
func (s *Service) CheckoutDomain(ctx context.Context, name string) (DomainCheckout, error) {
	b := s.currentBuyer()
	offer, err := s.QuoteDomain(ctx, name)
	if err != nil {
		return DomainCheckout{}, err
	}
	out := DomainCheckout{DomainOffer: offer}
	if out.Spend, err = s.spendOf(ctx, s.pool, SpendRegistrar); err != nil {
		return DomainCheckout{}, err
	}
	out.WithinHubCaps = offer.CostCents <= out.Spend.RoomCents()
	if !offer.Available {
		return out, nil
	}
	pre, err := b.reg.DryRun(ctx, offer.Domain, offer.CostCents)
	if err != nil {
		return DomainCheckout{}, fmt.Errorf("entry: dry-run %s: %w", offer.Domain, err)
	}
	out.WouldSucceed, out.SufficientFunds = pre.WouldSucceed, pre.SufficientFunds
	out.WithinRegistrarLimit, out.BalanceCents = pre.WithinMonthlyLimit, pre.BalanceCents
	return out, nil
}

// BuyDomain buys name into the pool, free for the next XHTTP box, through the
// same budget gate the hub's own purchases pass (П9.1). The price is quoted
// afresh here: what the owner saw a minute ago may have changed.
//
// A purchase whose answer never came is settled first, as the hub's own
// purchases do (buyDomain): it may be a name already paid for, and while the
// registrar cannot be heard a new purchase is guessing with money (audit 42
// recheck НН6). A replay that settles lands its name free in the pool.
func (s *Service) BuyDomain(ctx context.Context, name string) (Domain, error) {
	b := s.currentBuyer()
	if b.reg == nil {
		return Domain{}, ErrNoRegistrar
	}
	if _, _, err := s.replayUnknownPurchases(ctx, b, "", ""); err != nil {
		return Domain{}, err
	}
	offer, err := s.QuoteDomain(ctx, name)
	if err != nil {
		return Domain{}, err
	}
	if !offer.Available {
		return Domain{}, ErrDomainUnavailable
	}
	ok, err := s.purchase(ctx, b, "", "", offer.Domain, offer.TLD, offer.CostCents)
	if err != nil {
		return Domain{}, err
	}
	if !ok {
		return Domain{}, fmt.Errorf("%w: %s", ErrBudget, offer.Domain)
	}
	d, found, err := s.repo.domain(ctx, offer.Domain)
	if err != nil {
		return Domain{}, err
	}
	if !found {
		return Domain{}, fmt.Errorf("entry: bought %s but the ledger has no row", offer.Domain)
	}
	return d, nil
}

// AccountDomain is a domain on the registrar account, seen against the hub:
// whether the hub already has it and, if not, whether it may go into the pool.
type AccountDomain struct {
	Domain    string     `json:"domain"`
	TLD       string     `json:"tld"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	// HubStatus is the ledger status when the hub has the name; empty = not
	// in the hub. Box is where it lives when assigned.
	HubStatus string `json:"hubStatus,omitempty"`
	Box       string `json:"box,omitempty"`
	// Blocked says why the name may not go into the pool; empty = it may.
	// "brand": the brand's own domain, never a relay's; "excluded": on the
	// owner's do-not-use list (057); "invalid": not a name a relay can carry.
	// Whether the hub may edit the name's DNS is not known here: the seed
	// asks the registrar (CanSetARecord), one name at a time.
	Blocked string `json:"blocked,omitempty"`
}

// Reasons an account domain may not go into the pool.
const (
	blockedBrand    = "brand"
	blockedExcluded = "excluded"
	blockedInvalid  = "invalid"
)

// AccountDomains lists the domains on the registrar account next to the
// hub's ledger, so the owner can put the ones bought elsewhere into the pool
// with one click. Nothing is imported by itself: the account may hold the
// brand's domains and personal ones, and a pool domain is burned on its
// first block.
func (s *Service) AccountDomains(ctx context.Context) ([]AccountDomain, error) {
	b := s.currentBuyer()
	if b.reg == nil {
		return nil, ErrNoRegistrar
	}
	listed, err := b.reg.ListDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("entry: list account domains: %w", err)
	}
	ledger, err := s.repo.domains(ctx, 1000)
	if err != nil {
		return nil, err
	}
	known := make(map[string]Domain, len(ledger))
	for _, d := range ledger {
		known[d.Domain] = d
	}
	x, err := s.exclusionSet(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AccountDomain, 0, len(listed))
	for _, d := range listed {
		a := AccountDomain{Domain: d.Domain, TLD: d.TLD, ExpiresAt: d.ExpiresAt}
		if k, ok := known[d.Domain]; ok {
			a.HubStatus, a.Box = k.Status, k.Box
		}
		entry, builtIn := x.excludedBy(d.Domain)
		switch {
		case entry != "" && builtIn:
			a.Blocked = blockedBrand
		case entry != "":
			a.Blocked = blockedExcluded
		case checkRelayDomain(d.Domain) != nil:
			a.Blocked = blockedInvalid
		}
		out = append(out, a)
	}
	return out, nil
}

// offer quotes name and judges it against what is left of the domain budget.
func (s *Service) offer(ctx context.Context, b *domainBuyer, name string) (DomainOffer, error) {
	q, err := b.reg.Quote(ctx, name)
	if err != nil {
		return DomainOffer{}, fmt.Errorf("entry: quote %s: %w", name, err)
	}
	spend, err := s.spendOf(ctx, s.pool, SpendRegistrar)
	if err != nil {
		return DomainOffer{}, err
	}
	return DomainOffer{
		Domain: name, TLD: tldOf(name),
		Available: q.Available && q.CostCents > 0,
		CostCents: q.CostCents,
		OverCap:   q.CostCents > spend.RoomCents(),
	}, nil
}

// ownerDomainName normalizes a name the owner typed and refuses what cannot be
// a relay's domain (a subdomain, the brand, junk).
func ownerDomainName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if err := checkRelayDomain(name); err != nil {
		return "", err
	}
	if strings.Count(name, ".") != 1 {
		return "", fmt.Errorf("%w: buy a registrable name like cedar4821.lol, not a subdomain", ErrRejected)
	}
	return name, nil
}

// recordDomainFailure marks a bought-intent row failed, best-effort: a failure
// to record must not mask the purchase error the caller is already returning.
func (s *Service) recordDomainFailure(ctx context.Context, name string, cause error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort
	if err := s.repo.failDomain(ctx, tx, name, cause.Error()); err != nil {
		return
	}
	_ = tx.Commit(ctx)
}

// sealDomainOnBox writes domain, sealed, as the box's own — the ciphertext
// newXhttpIdentity reads when it mints a relay. Runs inside the caller's claim
// or settle transaction so the box and the ledger move together.
func (s *Service) sealDomainOnBox(ctx context.Context, tx pgx.Tx, box, domain string) error {
	ct, err := cryptobox.Encrypt([]byte(domain))
	if err != nil {
		return fmt.Errorf("entry: seal domain of %s: %w", box, err)
	}
	return s.repo.setBoxDomain(ctx, tx, box, ct)
}

// burnBoxDomainClear burns the domain a box carries and strips it from the
// box, so a re-provision treats the box as domainless and acquires a fresh
// name (Ф6). Returns the burned domain, empty when the box carried none.
func (s *Service) burnBoxDomainClear(ctx context.Context, box string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	burned, ok, err := s.repo.burnBoxDomain(ctx, tx, box)
	if err != nil {
		return "", err
	}
	// Burned is suspect, not dead (П9.11): the name gets its second chance.
	if ok {
		if err := suspect(ctx, tx, SuspectDomain, burned, CauseDomainBlocked); err != nil {
			return "", err
		}
	}
	if err := s.repo.setBoxDomain(ctx, tx, box, nil); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return burned, nil
}

// SeedDomain records a name the hub already owns (bought by hand) as free in
// the pool, so it is drawn before any purchase. Idempotent.
//
// With a registrar wired the name must be on its account and the registrar
// must accept a rehearsed A-record write for it: otherwise the provisioning
// job that draws it would fail at aiming the name, long after the owner moved
// on. The rehearsal, not listAll's apiAccess flag, decides — the flag read 0
// for a domain whose DNS the key edits fine (2026-10-22). A pool-only hub
// takes the name on trust; its DNS is the owner's to set.
func (s *Service) SeedDomain(ctx context.Context, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if err := checkRelayDomain(domain); err != nil {
		return err
	}
	if err := s.checkNotExcluded(ctx, domain); err != nil {
		return err
	}
	if b := s.currentBuyer(); b.reg != nil {
		listed, err := b.reg.ListDomains(ctx)
		if err != nil {
			return fmt.Errorf("entry: list account domains: %w", err)
		}
		if !slices.ContainsFunc(listed, func(d RegistrarDomain) bool { return d.Domain == domain }) {
			return fmt.Errorf("%w: %s is not on the registrar account", ErrRejected, domain)
		}
		if err := b.reg.CanSetARecord(ctx, domain); err != nil {
			return fmt.Errorf("%w: the registrar will not let the hub edit the DNS of %s: %v", ErrRejected, domain, err)
		}
	}
	return s.repo.seedDomain(ctx, domain, tldOf(domain))
}

// Domains lists the registrar ledger newest-first, for the admin journal.
func (s *Service) Domains(ctx context.Context, limit int) ([]Domain, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.repo.domains(ctx, limit)
}

// candidateName builds "<word><4 digits>.<tld>" with a cryptographically random
// word and number, so two boxes provisioned at once do not collide on a guess.
func candidateName(words []string, tld string) (string, error) {
	w, err := randIndex(len(words))
	if err != nil {
		return "", err
	}
	n, err := randN(9000)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d.%s", words[w], n+1000, tld), nil
}

// tldOf is the label after the last dot, lower-case.
func tldOf(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if i := strings.LastIndex(domain, "."); i >= 0 && i+1 < len(domain) {
		return domain[i+1:]
	}
	return ""
}

// idempotencyKey is a fresh per-attempt key: the domain plus random bytes.
// It is stored with the attempt's row before the charge; a retry of an
// answer that never came reuses it from there (replayUnknownPurchases) and
// is answered from the registrar's replay, while a new attempt — another
// name — gets a new one.
func idempotencyKey(domain string) (string, error) {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("nekko-%s-%x", domain, b), nil
}

func randIndex(n int) (int, error) { return randN(n) }

func randN(n int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}
