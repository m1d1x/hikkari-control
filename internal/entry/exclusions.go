package entry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/config"
)

// Domains the hub must never use for a relay (roadmap 42 П9.1, migration
// 057). The brand's configured domain is excluded by code and cannot be
// unlisted; the owner's list in the console sits on top of it. An entry
// covers the name and every subdomain of it.

// ErrNoSuchExclusion: the name is not on the owner's list.
var ErrNoSuchExclusion = errors.New("entry: domain is not excluded")

// DomainExclusion is one excluded name, for the console.
type DomainExclusion struct {
	Domain string `json:"domain"`
	Note   string `json:"note,omitempty"`
	// BuiltIn: the brand's domain from the hub's config; not removable.
	BuiltIn   bool       `json:"builtIn"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	// HubStatus and Box say where the name is in the hub now, when it is:
	// an excluded name already on a box stays there until re-provisioning.
	HubStatus string `json:"hubStatus,omitempty"`
	Box       string `json:"box,omitempty"`
}

// exclusionSet answers «may this name be a relay's?» for one request, read
// once so a loop over many names costs one query.
type exclusionSet struct {
	brand string
	owner []string
}

// excludedBy returns the entry that covers domain, or "" when none does.
func (x exclusionSet) excludedBy(domain string) (entry string, builtIn bool) {
	if covers(x.brand, domain) {
		return x.brand, true
	}
	for _, e := range x.owner {
		if covers(e, domain) {
			return e, false
		}
	}
	return "", false
}

// covers: domain is entry itself or a subdomain of it.
func covers(entry, domain string) bool {
	return entry != "" && (domain == entry || strings.HasSuffix(domain, "."+entry))
}

func (s *Service) exclusionSet(ctx context.Context) (exclusionSet, error) {
	rows, err := s.repo.exclusions(ctx)
	if err != nil {
		return exclusionSet{}, err
	}
	x := exclusionSet{brand: strings.ToLower(config.Default().Base)}
	for _, r := range rows {
		x.owner = append(x.owner, r.Domain)
	}
	return x, nil
}

// checkNotExcluded refuses a name the brand rule or the owner's list covers.
func (s *Service) checkNotExcluded(ctx context.Context, domain string) error {
	x, err := s.exclusionSet(ctx)
	if err != nil {
		return err
	}
	if e, builtIn := x.excludedBy(domain); e != "" {
		if builtIn {
			return fmt.Errorf("%w: a relay's domain is never the brand's (%s)", ErrRejected, e)
		}
		return fmt.Errorf("%w: %s is excluded (%s is on the do-not-use list)", ErrRejected, domain, e)
	}
	return nil
}

// Exclusions lists the brand's built-in entry and the owner's, each with
// where the name is in the hub now.
func (s *Service) Exclusions(ctx context.Context) ([]DomainExclusion, error) {
	rows, err := s.repo.exclusions(ctx)
	if err != nil {
		return nil, err
	}
	ledger, err := s.repo.domains(ctx, 1000)
	if err != nil {
		return nil, err
	}
	out := make([]DomainExclusion, 0, len(rows)+1)
	if brand := strings.ToLower(config.Default().Base); brand != "" {
		out = append(out, DomainExclusion{Domain: brand, BuiltIn: true})
	}
	for _, r := range rows {
		at := r.CreatedAt
		out = append(out, DomainExclusion{Domain: r.Domain, Note: r.Note, CreatedAt: &at})
	}
	// Show where a covered name is in the hub now; a burned or failed one is
	// history and says nothing about today.
	for i := range out {
		for _, d := range ledger {
			if covers(out[i].Domain, d.Domain) && (d.Status == domainOwned || d.Status == domainAssigned) {
				out[i].HubStatus, out[i].Box = d.Status, d.Box
				break
			}
		}
	}
	return out, nil
}

// ExcludeDomain puts domain on the do-not-use list. Idempotent; a second add
// keeps the first note. by is the admin's id, empty for the hub itself.
func (s *Service) ExcludeDomain(ctx context.Context, domain, note, by string) error {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if len(domain) > 253 || !domainRe.MatchString(domain) {
		return fmt.Errorf("%w: %q is not a domain name", ErrRejected, domain)
	}
	note = strings.TrimSpace(note)
	if len(note) > 200 {
		return fmt.Errorf("%w: the note is longer than 200 characters", ErrRejected)
	}
	var actor *uuid.UUID
	if by != "" {
		id, err := uuid.Parse(by)
		if err != nil {
			return fmt.Errorf("entry: admin id %q: %w", by, err)
		}
		actor = &id
	}
	return s.repo.insertExclusion(ctx, domain, note, actor)
}

// UnexcludeDomain takes domain off the owner's list. The brand's built-in
// entry is not on it and cannot be taken off.
func (s *Service) UnexcludeDomain(ctx context.Context, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == strings.ToLower(config.Default().Base) {
		return fmt.Errorf("%w: the brand's domain is excluded by the hub's config", ErrRejected)
	}
	ok, err := s.repo.deleteExclusion(ctx, domain)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoSuchExclusion
	}
	return nil
}
