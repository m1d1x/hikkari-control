package entrywire

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/porkbun"
)

// Registrar adapts a Porkbun client to the hub's entry.DomainRegistrar
// (roadmap 42 П4в Ф6). The entry package declares the interface and its data
// types on the consumer side; this adapter is the one place that knows both,
// mapping the registrar's wire answers into the domain's types and its
// concrete errors through unchanged.
func Registrar(c *porkbun.Client) entry.DomainRegistrar { return registrar{c: c} }

type registrar struct{ c *porkbun.Client }

func (r registrar) Registerable(ctx context.Context, tld string) (bool, error) {
	return r.c.Registerable(ctx, tld)
}

func (r registrar) Quote(ctx context.Context, domain string) (entry.DomainQuote, error) {
	res, err := r.c.CheckDomain(ctx, domain)
	if err != nil {
		return entry.DomainQuote{}, err
	}
	return entry.DomainQuote{
		Domain:    strings.ToLower(domain),
		TLD:       tldOf(domain),
		Available: res.Available,
		CostCents: res.CostCents,
	}, nil
}

func (r registrar) DryRun(ctx context.Context, domain string, costCents int) (entry.DomainPreflight, error) {
	res, err := r.c.Create(ctx, porkbun.CreateParams{Domain: domain, CostCents: costCents, DryRun: true})
	if err != nil {
		return entry.DomainPreflight{}, err
	}
	return entry.DomainPreflight{
		WouldSucceed:       res.WouldSucceed,
		SufficientFunds:    res.SufficientFunds,
		WithinMonthlyLimit: res.WithinMonthlyLimit,
		BalanceCents:       res.BalanceCents,
	}, nil
}

func (r registrar) Buy(ctx context.Context, domain string, costCents int, idempotencyKey string) (entry.DomainOrder, error) {
	res, err := r.c.Create(ctx, porkbun.CreateParams{
		Domain:         domain,
		CostCents:      costCents,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return entry.DomainOrder{}, registrarRefusal(err)
	}
	return entry.DomainOrder{
		Domain:    strings.ToLower(domain),
		OrderID:   res.OrderID,
		CostCents: costCents,
	}, nil
}

// registrarRefusal marks a purchase Porkbun answered with a no as
// entry.ErrProviderRefused: an envelope that parsed and said ERROR, under
// 500, or a 429 — the registrar spoke and charged nothing. Anything else (no
// body, a gateway's page, a reset, a timeout) is an answer that did not
// arrive, and the hub counts the purchase until its replay says (audit В4).
func registrarRefusal(err error) error {
	var api *porkbun.APIError
	switch {
	case errors.Is(err, porkbun.ErrBadCredentials), errors.Is(err, porkbun.ErrUnavailable),
		errors.Is(err, porkbun.ErrNotRegisterable), errors.Is(err, porkbun.ErrInsufficientFunds),
		errors.Is(err, porkbun.ErrOrderTooLarge), errors.Is(err, porkbun.ErrVerificationRequired),
		errors.Is(err, porkbun.ErrRateLimited),
		errors.As(err, &api) && api.Message != "" && api.HTTPStatus < 500:
		return fmt.Errorf("%w: %w", entry.ErrProviderRefused, err)
	}
	return err
}

func (r registrar) Account(ctx context.Context) (entry.RegistrarAccount, error) {
	bal, err := r.c.Balance(ctx)
	if err != nil {
		return entry.RegistrarAccount{}, err
	}
	lim, err := r.c.SpendLimit(ctx)
	if err != nil {
		return entry.RegistrarAccount{}, err
	}
	return entry.RegistrarAccount{
		BalanceCents: bal,
		LimitCents:   lim.LimitCents,
		Limited:      lim.Limited,
		SpentCents:   lim.SpentCents,
	}, nil
}

func (r registrar) ListDomains(ctx context.Context) ([]entry.RegistrarDomain, error) {
	ds, err := r.c.Domains(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entry.RegistrarDomain, 0, len(ds))
	for _, d := range ds {
		rd := entry.RegistrarDomain{Domain: d.Domain, TLD: d.TLD}
		if rd.TLD == "" {
			rd.TLD = tldOf(d.Domain)
		}
		// Porkbun writes the expiry in UTC as "2027-10-21 12:00:00".
		if t, err := time.Parse(time.DateTime, d.ExpireDate); err == nil {
			rd.ExpiresAt = &t
		}
		out = append(out, rd)
	}
	return out, nil
}

func (r registrar) CanSetARecord(ctx context.Context, domain string) error {
	return r.c.CanSetARecord(ctx, domain)
}

// SetARecord makes domain's apex A record resolve to ip and nothing else:
// every existing apex A record is removed first, so a pooled domain that used
// to point at another box is re-aimed cleanly rather than resolving to two
// addresses.
func (r registrar) SetARecord(ctx context.Context, domain, ip string) error {
	records, err := r.c.Records(ctx, domain)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if rec.Type != "A" || !isApex(rec.Name, domain) {
			continue
		}
		if rec.Content == ip {
			continue // already there; leave it rather than churn the record
		}
		if err := r.c.DeleteRecord(ctx, domain, rec.ID); err != nil {
			return fmt.Errorf("entrywire: clear A record %s of %s: %w", rec.ID, domain, err)
		}
	}
	// Re-check after the clear: if an apex A for ip already survived above we
	// must not add a duplicate.
	records, err = r.c.Records(ctx, domain)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if rec.Type == "A" && isApex(rec.Name, domain) && rec.Content == ip {
			return nil
		}
	}
	return r.c.CreateARecord(ctx, domain, ip)
}

// isApex reports whether a DNS record name is the domain's apex. Porkbun
// returns the apex either as an empty name or as the bare domain.
func isApex(name, domain string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "" || name == strings.ToLower(domain)
}

// tldOf is the part after the last dot, lower-case.
func tldOf(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if i := strings.LastIndex(domain, "."); i >= 0 && i+1 < len(domain) {
		return domain[i+1:]
	}
	return ""
}
