package entry

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
)

// SubdomainARegistrar can add an A record below a zone without replacing records.
type SubdomainARegistrar interface {
	EnsureSubdomainA(ctx context.Context, zone, name string, ip netip.Addr) (string, error)
}

// EnsureSubdomainA publishes an IPv4 address under a zone the hub bought.
func (s *Service) EnsureSubdomainA(ctx context.Context, domain, ipv4 string) (string, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	ip, err := netip.ParseAddr(ipv4)
	if err != nil || !ip.Is4() {
		return "", fmt.Errorf("%w: %q is not an IPv4 address", ErrRejected, ipv4)
	}
	zone, err := s.receiverZone(ctx, domain)
	if err != nil {
		return "", err
	}
	if zone == domain {
		return "", fmt.Errorf("%w: %s is the zone apex, expected a subdomain", ErrRejected, domain)
	}
	buyer := s.currentBuyer()
	if buyer == nil || buyer.reg == nil {
		return "", ErrNoRegistrar
	}
	registrar, ok := buyer.reg.(SubdomainARegistrar)
	if !ok {
		return "", fmt.Errorf("%w: the registrar cannot write subdomain A records", ErrRejected)
	}

	// Serialize the registrar's read and create for simultaneous CLI calls.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('entry-domain-a:' || $1::text, 0))`, domain); err != nil {
		return "", err
	}
	return registrar.EnsureSubdomainA(ctx, zone, strings.TrimSuffix(domain, "."+zone), ip)
}
