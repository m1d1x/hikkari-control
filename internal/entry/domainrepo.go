package entry

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Domain records of the hub's registrar ledger (migration 054). The name is
// plain — this ledger is the hub's own and never reaches a client — so a
// Domain is safe to read and list without a content key.
type Domain struct {
	Domain     string
	TLD        string
	Status     string
	Box        string // the box it is assigned to, empty when free, burned or failed
	Source     string // "purchased" or "seeded"
	CostCents  int
	OrderID    string
	Error      string
	BoughtAt   *time.Time
	AssignedAt *time.Time
	BurnedAt   *time.Time
	CreatedAt  time.Time
	// The burned name's second chance (073, the ledger's open row): when it
	// was last asked about, and the clean reprobes in a row since.
	LastReprobeAt *time.Time
	ReprobeStreak int
}

// Domain status values (migration 054).
const (
	domainBuying   = "buying"
	domainOwned    = "owned"
	domainAssigned = "assigned"
	domainBurned   = "burned"
	domainDead     = "dead"
	domainFailed   = "failed"

	domainPurchased = "purchased"
	domainSeeded    = "seeded"
)

const (
	// claimFreeDomain takes one free owned name for a box. SKIP LOCKED lets two
	// provisioning jobs claim different names at once without blocking; the
	// oldest free name goes first so the pool drains in the order it filled.
	//
	// A name the owner excluded after it was pooled (057) is never drawn: it
	// matches an exclusion exactly or as a subdomain of one.
	claimFreeDomain = `
		UPDATE entry_domains SET status = 'assigned', box = $1, assigned_at = now(), updated_at = now()
		WHERE domain = (
			SELECT d.domain FROM entry_domains d
			WHERE d.status = 'owned'
			  AND NOT EXISTS (
			      SELECT 1 FROM entry_domain_exclusions x
			      WHERE d.domain = x.domain OR right(d.domain, length(x.domain) + 1) = '.' || x.domain)
			ORDER BY d.created_at
			FOR UPDATE OF d SKIP LOCKED
			LIMIT 1
		)
		RETURNING domain`

	selectExclusions = `
		SELECT domain, note, created_at FROM entry_domain_exclusions ORDER BY domain`

	// insertExclusion keeps the first note when the name is already listed.
	insertExclusion = `
		INSERT INTO entry_domain_exclusions (domain, note, created_by) VALUES ($1, $2, $3)
		ON CONFLICT (domain) DO NOTHING`

	deleteExclusion = `DELETE FROM entry_domain_exclusions WHERE domain = $1`

	// insertBuyingDomain writes a purchase intent before the charge, with the
	// idempotency key that makes the charge recoverable. A clash on the key or
	// the name is a conflict, not a second row.
	insertBuyingDomain = `
		INSERT INTO entry_domains (domain, tld, status, source, cost_cents, idempotency_key)
		VALUES ($1, $2, 'buying', 'purchased', $3, $4)`

	// settleDomain turns a settled purchase into a usable domain: assigned to a
	// box when $3 is given, otherwise free in the pool. An `unknown` one too:
	// its replay came back with the order (or the account shows the name).
	settleDomain = `
		UPDATE entry_domains
		SET status = CASE WHEN $3 <> '' THEN 'assigned' ELSE 'owned' END,
		    box = CASE WHEN $3 <> '' THEN $3 ELSE box END,
		    order_id = $2, bought_at = now(),
		    assigned_at = CASE WHEN $3 <> '' THEN now() ELSE assigned_at END,
		    updated_at = now()
		WHERE domain = $1 AND status IN ('buying', 'unknown')`

	// failDomain marks a purchase the registrar refused, or one whose replay
	// showed it never charged; kept for audit, never drawn again.
	failDomain = `
		UPDATE entry_domains SET status = 'failed', error = $2, updated_at = now()
		WHERE domain = $1 AND status IN ('buying', 'unknown')`

	// markDomainUnknown records a purchase sent whose answer never came (В4):
	// counted in the month, kept with its key for the replay.
	markDomainUnknown = `
		UPDATE entry_domains SET status = 'unknown', error = $2, updated_at = now()
		WHERE domain = $1 AND status = 'buying'`

	// selectUnknownPurchases lists them for the replay, oldest first. The
	// key's age is the row's: the key is written with it. A row still
	// `buying` past what one Buy takes is one too (audit 42 recheck НН6): the
	// hub died between the commit and the Buy, or markDomainUnknown failed,
	// and the registrar may have charged either way.
	selectUnknownPurchases = `
		SELECT domain, idempotency_key, cost_cents, created_at FROM entry_domains
		WHERE idempotency_key <> ''
		  AND (status = 'unknown' OR (status = 'buying' AND updated_at < now() - interval '10 minutes'))
		ORDER BY created_at`

	// seedDomain records a name the hub already owns (bought by hand), free in
	// the pool from the start.
	seedDomain = `
		INSERT INTO entry_domains (domain, tld, status, source, bought_at)
		VALUES ($1, $2, 'owned', 'seeded', now())
		ON CONFLICT (domain) DO NOTHING`

	// burnBoxDomain burns whichever domain a box carries: a blocked name
	// leaves the box and the pool, suspect until its second chance says
	// otherwise (secondchance.go). Returns the burned name, if any.
	burnBoxDomain = `
		UPDATE entry_domains SET status = 'burned', box = NULL, burned_at = now(), updated_at = now()
		WHERE box = $1 AND status = 'assigned'
		RETURNING domain`

	// lockDomainPurchases serializes purchase reservations on one hub: a
	// transaction-scoped advisory lock on a fixed key (42·06), released at
	// commit or rollback, so the caps check (domainAffordable) and the intent
	// insert are atomic against a concurrent purchase.
	lockDomainPurchases = `SELECT pg_advisory_xact_lock(4206)`

	// setBoxDomain swaps a box's sealed domain (a replacement after a block).
	setBoxDomain = `UPDATE entry_boxes SET domain_ct = $2 WHERE name = $1`

	// boxAndSurvivors returns a relay's box and how many of that box's relays
	// are still not dead (the relay named is already dead when this is asked,
	// so it counts the others).
	boxAndSurvivors = `
		SELECT r.box,
		       (SELECT count(*) FROM entry_relays o WHERE o.box = r.box AND o.status <> 'dead' AND o.id <> r.id)
		FROM entry_relays r WHERE r.name = $1`

	domainColumns = `
		SELECT d.domain, d.tld, d.status, coalesce(d.box, ''), d.source, d.cost_cents, d.order_id, d.error,
		       d.bought_at, d.assigned_at, d.burned_at, d.created_at, s.last_probe_at, coalesce(s.clean_streak, 0)
		FROM entry_domains d
		LEFT JOIN entry_suspects s ON s.kind = 'domain' AND s.subject = d.domain AND s.status = 'suspect'`

	selectDomains = domainColumns + ` ORDER BY d.created_at DESC LIMIT $1`

	selectDomain = domainColumns + ` WHERE d.domain = $1`
)

// ClaimFreeDomain assigns one free pooled domain to box, returning it. ok is
// false (no error) when the pool is empty — the caller then buys one.
func (r *Repository) claimFreeDomain(ctx context.Context, tx pgx.Tx, box string) (domain string, ok bool, err error) {
	err = tx.QueryRow(ctx, claimFreeDomain, box).Scan(&domain)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return domain, true, nil
}

// insertBuyingDomain writes a purchase intent (status 'buying') with its
// idempotency key, before the charge.
func (r *Repository) insertBuyingDomain(ctx context.Context, tx pgx.Tx, domain, tld, idempotencyKey string, costCents int) error {
	_, err := tx.Exec(ctx, insertBuyingDomain, domain, tld, costCents, idempotencyKey)
	return err
}

// settleDomain records a settled purchase. When box is non-empty the domain
// goes straight onto that box; otherwise it lands free in the pool. ok is
// false when the row was not waiting to be settled — another caller did.
func (r *Repository) settleDomain(ctx context.Context, tx pgx.Tx, domain, orderID, box string) (bool, error) {
	tag, err := tx.Exec(ctx, settleDomain, domain, orderID, box)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// failDomain marks an intent whose purchase did not settle.
func (r *Repository) failDomain(ctx context.Context, tx pgx.Tx, domain, reason string) error {
	_, err := tx.Exec(ctx, failDomain, domain, reason)
	return err
}

// seedDomain records a hand-bought name as free in the pool. Idempotent.
func (r *Repository) seedDomain(ctx context.Context, domain, tld string) error {
	_, err := r.pool.Exec(ctx, seedDomain, domain, tld)
	return err
}

// burnBoxDomain burns the domain a box carried and returns it; ok is false
// when the box carried none.
func (r *Repository) burnBoxDomain(ctx context.Context, tx pgx.Tx, box string) (domain string, ok bool, err error) {
	err = tx.QueryRow(ctx, burnBoxDomain, box).Scan(&domain)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return domain, true, nil
}

// lockDomainPurchases takes the hub-wide advisory lock that serializes
// purchase reservations, for the length of tx.
func (r *Repository) lockDomainPurchases(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, lockDomainPurchases)
	return err
}

// boxAndSurvivors returns the box a relay sits on and how many other relays on
// that box are still alive or pending.
func (r *Repository) boxAndSurvivors(ctx context.Context, tx pgx.Tx, relay string) (box string, survivors int, err error) {
	err = tx.QueryRow(ctx, boxAndSurvivors, relay).Scan(&box, &survivors)
	return box, survivors, err
}

// exclusionRow is one owner-listed name the hub never uses (057).
type exclusionRow struct {
	Domain    string
	Note      string
	CreatedAt time.Time
}

func (r *Repository) exclusions(ctx context.Context) ([]exclusionRow, error) {
	rows, err := r.pool.Query(ctx, selectExclusions)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []exclusionRow
	for rows.Next() {
		var e exclusionRow
		if err := rows.Scan(&e.Domain, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// insertExclusion lists domain; by is the admin, nil for the hub itself.
func (r *Repository) insertExclusion(ctx context.Context, domain, note string, by *uuid.UUID) error {
	_, err := r.pool.Exec(ctx, insertExclusion, domain, note, by)
	return err
}

// deleteExclusion unlists domain; ok is false when it was not listed.
func (r *Repository) deleteExclusion(ctx context.Context, domain string) (bool, error) {
	tag, err := r.pool.Exec(ctx, deleteExclusion, domain)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// domain reads one ledger row; ok is false when the name is not in it.
func (r *Repository) domain(ctx context.Context, name string) (Domain, bool, error) {
	var d Domain
	err := r.pool.QueryRow(ctx, selectDomain, name).Scan(&d.Domain, &d.TLD, &d.Status, &d.Box, &d.Source,
		&d.CostCents, &d.OrderID, &d.Error, &d.BoughtAt, &d.AssignedAt, &d.BurnedAt, &d.CreatedAt,
		&d.LastReprobeAt, &d.ReprobeStreak)
	if err == pgx.ErrNoRows {
		return Domain{}, false, nil
	}
	if err != nil {
		return Domain{}, false, err
	}
	return d, true, nil
}

// setBoxDomain writes a box's sealed domain ciphertext, swapping whatever was
// there (a replacement after a block).
func (r *Repository) setBoxDomain(ctx context.Context, tx pgx.Tx, box string, domainCT []byte) error {
	_, err := tx.Exec(ctx, setBoxDomain, box, domainCT)
	return err
}

// domains lists the ledger newest-first, up to limit, for the admin journal.
func (r *Repository) domains(ctx context.Context, limit int) ([]Domain, error) {
	rows, err := r.pool.Query(ctx, selectDomains, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.Domain, &d.TLD, &d.Status, &d.Box, &d.Source, &d.CostCents,
			&d.OrderID, &d.Error, &d.BoughtAt, &d.AssignedAt, &d.BurnedAt, &d.CreatedAt,
			&d.LastReprobeAt, &d.ReprobeStreak); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
