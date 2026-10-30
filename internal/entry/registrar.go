package entry

import (
	"context"
	"time"
)

// DomainRegistrar is everything the hub needs from a domain registrar to run
// an XHTTP box's throwaway domain without a human (plan 42 П4в Ф6): decide
// whether a TLD can be bought at all, quote a candidate name, rehearse the
// spend, buy it, and point its A record at the box's address.
//
// The interface and its data types are declared here, on the consumer, so the
// entry package never imports a provider (project Go rule). The Porkbun
// implementation is a pure client in internal/porkbun; the adapter that makes
// it a DomainRegistrar lives in internal/entrywire, the same place the media
// source and the live notifier are adapted.
type DomainRegistrar interface {
	// Registerable reports whether this TLD can be registered through the
	// registrar's API at all. Most country-code and premium TLDs cannot be,
	// and a name on one of them must never reach a purchase.
	Registerable(ctx context.Context, tld string) (bool, error)

	// Quote is the live first-year registration price of domain, in whole
	// cents, with whether it is available to register now. It is the figure a
	// Buy of the same name must pay to the cent, so a quote and a buy read the
	// same source — never a bulk price list, which omits promos and premiums.
	Quote(ctx context.Context, domain string) (DomainQuote, error)

	// DryRun rehearses buying domain for costCents without spending: whether
	// the purchase would go through, whether the prepaid credit covers it, and
	// whether it stays inside the registrar's own monthly spend limit. It is
	// the account-side half of the budget gate; the per-domain and monthly
	// caps the hub enforces itself are the other half.
	DryRun(ctx context.Context, domain string, costCents int) (DomainPreflight, error)

	// Buy registers domain for costCents, which must equal a fresh Quote of
	// the same name. idempotencyKey makes the charge safe to retry across a
	// dropped response: the registrar replays the original response for 24h,
	// the error included, so a retry after a *fixed* failure must pass a NEW
	// key or it will read the stale refusal back.
	Buy(ctx context.Context, domain string, costCents int, idempotencyKey string) (DomainOrder, error)

	// SetARecord points domain's apex A record at ip, replacing any apex A
	// record already there. It aims a freshly bought or pooled domain at the
	// box that will serve it.
	SetARecord(ctx context.Context, domain, ip string) error

	// Account proves the credentials work and reads the account's money: the
	// prepaid credit purchases are paid from and the monthly cap the registrar
	// itself enforces (П9.1). Bad credentials are an error, not a zero balance.
	Account(ctx context.Context) (RegistrarAccount, error)

	// ListDomains lists every domain registered on the account, so the owner
	// can put ones bought elsewhere into the pool without typing them.
	ListDomains(ctx context.Context) ([]RegistrarDomain, error)

	// CanSetARecord asks the registrar to rehearse aiming domain, changing
	// nothing: nil when the hub's key may edit the domain's DNS, so a pooled
	// name will not fail its box's provisioning at the A record.
	CanSetARecord(ctx context.Context, domain string) error
}

// RegistrarDomain is one domain registered on the registrar account.
type RegistrarDomain struct {
	Domain string
	TLD    string
	// ExpiresAt is the registration's expiry; nil when the registrar did not
	// give a readable one.
	ExpiresAt *time.Time
}

// RegistrarAccount is a registrar account's money as of one check.
type RegistrarAccount struct {
	// BalanceCents is the prepaid credit left, in whole cents.
	BalanceCents int
	// LimitCents is the registrar's own monthly cap on API purchases;
	// meaningful only when Limited.
	LimitCents int
	// Limited is false when the registrar applies no monthly cap.
	Limited bool
	// SpentCents is what API purchases spent this calendar month, across
	// everything on the account (not only this hub's purchases).
	SpentCents int
}

// DomainReplacer re-provisions a box whose domain was blocked: a fresh domain
// and a new relay on the same machine, no human in the loop (Ф6). It is
// declared here, on the consumer, and implemented by the Provisioner; the
// Service holds one, set at startup, and calls it when a domain_blocked death
// leaves a box with no live relay. Left unset, such a death waits for a hand.
type DomainReplacer interface {
	// ReplaceDomain burns the box's blocked domain and runs a provisioning job
	// that gives it a new one and a new relay. It is expected to return
	// promptly and do its work on its own; the Service calls it in a goroutine.
	ReplaceDomain(box string)
}

// DomainQuote is a registrar's live answer for one candidate name.
type DomainQuote struct {
	// Domain is the full candidate, lower-case (e.g. "cedar4821.lol").
	Domain string
	// TLD is its top-level domain without the leading dot (e.g. "lol").
	TLD string
	// Available is whether the registrar can register it right now.
	Available bool
	// CostCents is the first-year price in whole cents; what Buy must pay.
	CostCents int
}

// DomainPreflight is a registrar's rehearsal of a purchase (DryRun).
type DomainPreflight struct {
	// WouldSucceed is the registrar's own verdict that the purchase would go
	// through: funds, limits and availability all clear.
	WouldSucceed bool
	// SufficientFunds is whether the prepaid credit covers the cost.
	SufficientFunds bool
	// WithinMonthlyLimit is whether the purchase stays inside the registrar's
	// account-wide monthly spend limit (separate from the hub's own cap).
	WithinMonthlyLimit bool
	// BalanceCents is the prepaid credit left, best-effort: 0 when the
	// registrar did not return a parseable balance.
	BalanceCents int
}

// DomainOrder is a completed registration.
type DomainOrder struct {
	// Domain is the registered name, lower-case.
	Domain string
	// OrderID is the registrar's order reference, recorded in the journal so a
	// purchase can be reconciled against the account later.
	OrderID string
	// CostCents is what was charged, in whole cents.
	CostCents int
}
