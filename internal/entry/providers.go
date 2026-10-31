package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// Provider accounts (roadmap 42 П9.1): the registrar and, from П9.2, hosting
// accounts the hub spends through. The owner saves credentials and limits in
// the console; they are sealed at rest (entry_providers, migration 056) and
// applied to the running hub at once, without a restart.
//
// This package never imports a provider. What a kind is — its role, the
// fields its credentials have, how to turn them into a live client — is told
// by a ProviderConnector, declared here and implemented in internal/entrywire
// next to the adapters themselves.

// Errors a provider operation returns.
var (
	// ErrUnknownProvider: no adapter of that kind is built into this hub.
	ErrUnknownProvider = errors.New("entry: unknown provider")
	// ErrProviderCheck: the provider refused the credentials or could not be
	// reached; nothing was saved. The wrapped error is the provider's own.
	ErrProviderCheck = errors.New("entry: provider check failed")
)

// ProviderRole is what a provider kind does for the hub.
type ProviderRole string

const (
	// RoleRegistrar sells and points the throwaway domains of XHTTP boxes.
	RoleRegistrar ProviderRole = "registrar"
	// RoleHosting rents the machines relays run on (П9.2).
	RoleHosting ProviderRole = "hosting"
)

// CredentialField is one field of a provider's credentials, as the console
// asks for it.
type CredentialField struct {
	// Name is the key in the sealed credentials object.
	Name string `json:"name"`
	// Label is what the console shows next to the input.
	Label string `json:"label"`
	// Secret fields are typed into a password input; a non-secret one (a
	// public key id) is what the masked hint is cut from.
	Secret bool `json:"secret"`
	// Optional fields may be left blank.
	Optional bool `json:"optional,omitempty"`
}

// ProviderKind describes one built-in adapter.
type ProviderKind struct {
	Kind   string            `json:"kind"`
	Title  string            `json:"title"`
	Role   ProviderRole      `json:"role"`
	Fields []CredentialField `json:"fields"`
}

// ProviderConnector is how the hub learns its provider kinds and turns stored
// credentials into a live client. Declared on the consumer; implemented in
// internal/entrywire.
type ProviderConnector interface {
	// Kinds lists the built-in adapters, in the order the console shows them
	// and the hub prefers them when more than one of a role is enabled.
	Kinds() []ProviderKind
	// Registrar builds a registrar client for kind from its credentials.
	// ErrUnknownProvider when kind is not a registrar this hub knows.
	Registrar(kind string, creds map[string]string) (DomainRegistrar, error)
	// Hosting builds a hosting client for kind from its credentials (П9.2).
	// ErrUnknownProvider when kind is not a hoster this hub knows.
	Hosting(kind string, creds map[string]string) (BoxProvider, error)
}

// ProviderCheck is the last check of a provider account.
type ProviderCheck struct {
	At    time.Time `json:"at"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	// Money as of the check; nil when the check failed or the role has none.
	BalanceCents *int `json:"balanceCents,omitempty"`
	// The provider's own monthly cap on API spend and what the account spent
	// this month under it; LimitCents nil = the provider applies no cap.
	AccountSpentCents *int `json:"accountSpentCents,omitempty"`
	AccountLimitCents *int `json:"accountLimitCents,omitempty"`
}

// ProviderView is a provider kind with its saved account, for the console.
// Credentials themselves never appear in it.
type ProviderView struct {
	ProviderKind
	// Configured: credentials are saved.
	Configured bool `json:"configured"`
	// Enabled: the owner switched it on; Active: it is the one the hub uses
	// now for its role.
	Enabled bool `json:"enabled"`
	Active  bool `json:"active"`
	// CredsHint is a masked tail of the public key, so the owner can tell
	// which key is saved.
	CredsHint string   `json:"credsHint,omitempty"`
	TLDs      []string `json:"tlds"`
	// DefaultRegion and DefaultPlan are a hoster's order defaults: where a
	// machine is ordered and which size, when the order names neither (П9.2).
	// Empty on a registrar.
	DefaultRegion string         `json:"defaultRegion,omitempty"`
	DefaultPlan   string         `json:"defaultPlan,omitempty"`
	Check         *ProviderCheck `json:"check,omitempty"`
	UpdatedAt     *time.Time     `json:"updatedAt,omitempty"`
}

// ProviderUpdate is what the owner saves for one provider.
type ProviderUpdate struct {
	Enabled bool
	// Credentials, when non-nil, replaces the saved ones; every field the kind
	// declares must be set. Nil keeps what is saved.
	Credentials map[string]string
	// TLDs is the registrar's candidate list in order; empty = built-in list.
	TLDs []string
	// DefaultRegion and DefaultPlan are the hoster's order defaults; ignored
	// for a registrar.
	DefaultRegion string
	DefaultPlan   string
}

// providerCheckTimeout bounds one account check (a ping and two reads).
const providerCheckTimeout = 30 * time.Second

// SetProviderConnector wires the provider adapters. Called once at startup,
// before LoadProviders; left unset, the hub has no providers and is
// pool-only, and the console lists none.
func (s *Service) SetProviderConnector(c ProviderConnector) { s.connector = c }

// LoadProviders applies the saved provider accounts to the running hub: the
// first enabled registrar becomes the one domains are bought through. Called at startup and after every save. With no usable registrar the
// hub is pool-only (AcquireDomain fails with ErrNoRegistrar once the pool is
// dry), which is a state, not an error.
func (s *Service) LoadProviders(ctx context.Context) error {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	return s.loadProvidersLocked(ctx)
}

func (s *Service) loadProvidersLocked(ctx context.Context) error {
	if s.connector == nil {
		s.SetDomainRegistrar(nil, DefaultDomainPolicy())
		s.SetHoster("", nil, HostingPolicy{})
		return nil
	}
	rows, err := s.repo.providers(ctx)
	if err != nil {
		return err
	}
	s.loadRegistrarLocked(rows)
	s.loadHosterLocked(rows)
	return nil
}

// loadRegistrarLocked makes the first enabled registrar the one domains are
// bought through.
func (s *Service) loadRegistrarLocked(rows map[string]providerRow) {
	for _, k := range s.connector.Kinds() {
		if k.Role != RoleRegistrar {
			continue
		}
		creds, ok := usableCreds(k, rows)
		if !ok {
			continue
		}
		reg, err := s.connector.Registrar(k.Kind, creds)
		if err != nil {
			applog.Error(context.Background(), "entry", "registrar unusable", applog.F("provider", k.Kind), applog.F("err", err.Error()))
			continue
		}
		s.setSavedRegistrar(k.Kind, reg, rows[k.Kind].policy())
		s.activeRegistrar.Store(&k.Kind)
		return
	}
	s.SetDomainRegistrar(nil, DefaultDomainPolicy())
	s.activeRegistrar.Store(nil)
}

// loadHosterLocked makes the first enabled hoster the one machines are ordered
// from, with its region and plan (П9.2). No usable hoster leaves the hub
// unable to order or hand back a machine, which is the state of a hub whose
// boxes were all rented by hand — not an error.
func (s *Service) loadHosterLocked(rows map[string]providerRow) {
	for _, k := range s.connector.Kinds() {
		if k.Role != RoleHosting {
			continue
		}
		creds, ok := usableCreds(k, rows)
		if !ok {
			continue
		}
		bp, err := s.connector.Hosting(k.Kind, creds)
		if err != nil {
			applog.Error(context.Background(), "entry", "hoster unusable", applog.F("provider", k.Kind), applog.F("err", err.Error()))
			continue
		}
		s.SetHoster(k.Kind, bp, rows[k.Kind].hostingPolicy())
		return
	}
	s.SetHoster("", nil, HostingPolicy{})
}

// usableCreds opens the saved credentials of an enabled kind; ok is false when
// the kind has no account, is switched off, or its row cannot be opened with
// this hub's server key.
func usableCreds(k ProviderKind, rows map[string]providerRow) (map[string]string, bool) {
	row, ok := rows[k.Kind]
	if !ok || !row.Enabled || row.CredsCT == nil {
		return nil, false
	}
	creds, err := openCreds(row.CredsCT)
	if err != nil {
		// A row the server key cannot open is not this hub's; skip it rather
		// than refuse to start, and say so.
		applog.Error(context.Background(), "entry", "provider credentials unreadable", applog.F("provider", k.Kind), applog.F("err", err.Error()))
		return nil, false
	}
	return creds, true
}

// Providers lists every built-in provider kind with its saved account.
func (s *Service) Providers(ctx context.Context) ([]ProviderView, error) {
	if s.connector == nil {
		return []ProviderView{}, nil
	}
	rows, err := s.repo.providers(ctx)
	if err != nil {
		return nil, err
	}
	active := ""
	if p := s.activeRegistrar.Load(); p != nil {
		active = *p
	}
	activeHost := s.hosterKind()
	out := make([]ProviderView, 0, len(s.connector.Kinds()))
	for _, k := range s.connector.Kinds() {
		v := ProviderView{ProviderKind: k, TLDs: []string{}}
		if row, ok := rows[k.Kind]; ok {
			v.Configured = row.CredsCT != nil
			v.Enabled = row.Enabled
			v.CredsHint = row.CredsHint
			v.TLDs = append(v.TLDs, row.TLDs...)
			v.DefaultRegion, v.DefaultPlan = row.DefaultRegion, row.DefaultPlan
			v.Check = row.check()
			at := row.UpdatedAt
			v.UpdatedAt = &at
		}
		v.Active = (k.Role == RoleRegistrar && k.Kind == active) ||
			(k.Role == RoleHosting && k.Kind == activeHost && activeHost != "")
		out = append(out, v)
	}
	return out, nil
}

// SaveProvider stores the owner's settings for kind and applies them to the
// running hub. New credentials are checked against the provider first and are
// not saved if the check fails (ErrProviderCheck): a key that does not work is
// not something to keep. Enabling needs credentials, saved or given.
func (s *Service) SaveProvider(ctx context.Context, kind string, u ProviderUpdate, by string) (ProviderView, error) {
	k, ok := s.providerKind(kind)
	if !ok {
		return ProviderView{}, ErrUnknownProvider
	}
	tlds, err := normalizeTLDs(u.TLDs)
	if err != nil {
		return ProviderView{}, err
	}
	u.DefaultRegion, u.DefaultPlan = strings.TrimSpace(u.DefaultRegion), strings.TrimSpace(u.DefaultPlan)
	if k.Role != RoleHosting && (u.DefaultRegion != "" || u.DefaultPlan != "") {
		return ProviderView{}, fmt.Errorf("%w: only a hoster has a region and a plan", ErrRejected)
	}

	s.providerMu.Lock()
	defer s.providerMu.Unlock()

	var sealed []byte
	var hint string
	var check *ProviderCheck
	if u.Credentials != nil {
		creds, err := completeCreds(k, u.Credentials)
		if err != nil {
			return ProviderView{}, err
		}
		acct, err := s.checkAccount(ctx, k, creds)
		if err != nil {
			return ProviderView{}, fmt.Errorf("%w: %v", ErrProviderCheck, err)
		}
		check = acct
		raw, err := json.Marshal(creds)
		if err != nil {
			return ProviderView{}, err
		}
		if sealed, err = cryptobox.Encrypt(raw); err != nil {
			return ProviderView{}, fmt.Errorf("entry: seal %s credentials: %w", kind, err)
		}
		hint = credsHint(k, creds)
	} else if u.Enabled {
		row, ok, err := s.repo.provider(ctx, kind)
		if err != nil {
			return ProviderView{}, err
		}
		if !ok || row.CredsCT == nil {
			return ProviderView{}, fmt.Errorf("%w: save the credentials before switching it on", ErrRejected)
		}
	}

	// Refuse stale or forged catalog selections before enabling purchases.
	// Disabling an account must work even if its API is unavailable.
	if k.Role == RoleHosting && u.Enabled {
		if err := s.validateHostingDefaults(ctx, kind, u); err != nil {
			return ProviderView{}, err
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderView{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if err := s.repo.upsertProvider(ctx, tx, kind, u, tlds, by, sealed, hint); err != nil {
		return ProviderView{}, err
	}
	if err := s.repo.recordProviderCheck(ctx, tx, kind, check); err != nil {
		return ProviderView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderView{}, err
	}
	if err := s.loadProvidersLocked(ctx); err != nil {
		return ProviderView{}, err
	}
	return s.providerView(ctx, kind)
}

// CheckProvider re-checks a saved account against the provider and records
// the result (credentials still valid, credit left, the provider's own cap).
// A failed check is recorded too, and is not an error of this call.
func (s *Service) CheckProvider(ctx context.Context, kind string) (ProviderView, error) {
	k, ok := s.providerKind(kind)
	if !ok {
		return ProviderView{}, ErrUnknownProvider
	}
	row, ok, err := s.repo.provider(ctx, kind)
	if err != nil {
		return ProviderView{}, err
	}
	if !ok || row.CredsCT == nil {
		return ProviderView{}, fmt.Errorf("%w: no credentials saved for %s", ErrRejected, kind)
	}
	creds, err := openCreds(row.CredsCT)
	if err != nil {
		return ProviderView{}, fmt.Errorf("entry: open %s credentials: %w", kind, err)
	}
	check, cerr := s.checkAccount(ctx, k, creds)
	if cerr != nil {
		check = &ProviderCheck{At: s.now(), OK: false, Error: cerr.Error()}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProviderView{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if err := s.repo.recordProviderCheck(ctx, tx, kind, check); err != nil {
		return ProviderView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProviderView{}, err
	}
	return s.providerView(ctx, kind)
}

// ProviderCheckInterval is how often the hub asks every saved provider account
// for its money and its key's health by itself.
//
// Ten minutes: a balance that runs dry, a key revoked in the provider's panel
// or a limit hit are things the budget gate has to know before it spends, and
// the console has to show without anybody pressing "check" — the autopilot
// has nobody to press it (owner, 25.10). Often enough to be current within a
// repair's own time scale, rare enough to be nothing next to the providers'
// rate limits (three reads per account per pass).
const ProviderCheckInterval = 10 * time.Minute

// RunProviderChecks re-checks every saved account at startup and then every
// interval until ctx ends, enabled or not: an owner deciding whether to switch
// a hoster on needs its balance as much as the hub does once it is on. A
// failed check is recorded like a passed one — a key that stopped working is
// the finding, not an error of the loop.
func (s *Service) RunProviderChecks(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.checkProviders(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) checkProviders(ctx context.Context) {
	if s.connector == nil {
		return
	}
	rows, err := s.repo.providers(ctx)
	if err != nil {
		if ctx.Err() == nil {
			applog.Error(ctx, "entry", "provider checks failed", applog.F("err", err.Error()))
		}
		return
	}
	for kind, row := range rows {
		if row.CredsCT == nil {
			continue
		}
		if _, err := s.CheckProvider(ctx, kind); err != nil && ctx.Err() == nil {
			applog.Warn(ctx, "entry", "provider check failed", applog.F("provider", kind), applog.F("err", err.Error()))
		}
	}
}

// checkAccount builds a client for creds and asks the provider for the
// account: the same mechanism for both roles, which is what makes a key that
// does not work impossible to save whichever provider it is for.
func (s *Service) checkAccount(ctx context.Context, k ProviderKind, creds map[string]string) (*ProviderCheck, error) {
	ctx, cancel := context.WithTimeout(ctx, providerCheckTimeout)
	defer cancel()
	switch k.Role {
	case RoleRegistrar:
		reg, err := s.connector.Registrar(k.Kind, creds)
		if err != nil {
			return nil, err
		}
		acct, err := reg.Account(ctx)
		if err != nil {
			return nil, err
		}
		c := &ProviderCheck{At: s.now(), OK: true}
		bal, spent := acct.BalanceCents, acct.SpentCents
		c.BalanceCents, c.AccountSpentCents = &bal, &spent
		if acct.Limited {
			lim := acct.LimitCents
			c.AccountLimitCents = &lim
		}
		return c, nil
	case RoleHosting:
		bp, err := s.connector.Hosting(k.Kind, creds)
		if err != nil {
			return nil, err
		}
		acct, err := bp.Account(ctx)
		if err != nil {
			return nil, err
		}
		c := &ProviderCheck{At: s.now(), OK: true}
		c.BalanceCents = acct.BalanceCents
		// What a hoster has accrued and not billed is the nearest thing it has
		// to the registrar's month-to-date spend, and it is read into the same
		// field so the console shows one money row for both roles.
		if acct.PendingCents != 0 {
			pending := acct.PendingCents
			c.AccountSpentCents = &pending
		}
		return c, nil
	}
	return nil, ErrUnknownProvider
}

func (s *Service) providerView(ctx context.Context, kind string) (ProviderView, error) {
	all, err := s.Providers(ctx)
	if err != nil {
		return ProviderView{}, err
	}
	for _, v := range all {
		if v.Kind == kind {
			return v, nil
		}
	}
	return ProviderView{}, ErrUnknownProvider
}

func (s *Service) providerKind(kind string) (ProviderKind, bool) {
	if s.connector == nil {
		return ProviderKind{}, false
	}
	for _, k := range s.connector.Kinds() {
		if k.Kind == kind {
			return k, true
		}
	}
	return ProviderKind{}, false
}

// completeCreds keeps exactly the fields kind declares, trimmed, and refuses a
// missing one: a half-saved key would pass the save and fail the first buy.
func completeCreds(k ProviderKind, in map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(k.Fields))
	for _, f := range k.Fields {
		v := strings.TrimSpace(in[f.Name])
		if v == "" && !f.Optional {
			return nil, fmt.Errorf("%w: %s is empty", ErrRejected, f.Label)
		}
		out[f.Name] = v
	}
	return out, nil
}

// credsHint masks the first non-secret field down to its tail, so the console
// can show which key is saved without showing the key.
func credsHint(k ProviderKind, creds map[string]string) string {
	for _, f := range k.Fields {
		if f.Secret {
			continue
		}
		v := creds[f.Name]
		if len(v) <= 8 {
			return "…"
		}
		return v[:4] + "…" + v[len(v)-4:]
	}
	return "…"
}

func openCreds(ct []byte) (map[string]string, error) {
	raw, err := cryptobox.Decrypt(ct)
	if err != nil {
		return nil, err
	}
	var creds map[string]string
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// normalizeTLDs lower-cases, strips dots and duplicates, and refuses a label
// that cannot be a TLD.
func normalizeTLDs(in []string) ([]string, error) {
	out := []string{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(t), "."))
		if t == "" || slices.Contains(out, t) {
			continue
		}
		for _, r := range t {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return nil, fmt.Errorf("%w: %q is not a top-level domain", ErrRejected, t)
			}
		}
		out = append(out, t)
	}
	return out, nil
}
