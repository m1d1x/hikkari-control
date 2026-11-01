package entry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Repository is the Postgres store behind [Service] (migration 036). Every
// method that changes the pool or the registry takes the caller's
// transaction: the service decides what one unit of work is.
type Repository struct {
	pool db.Pool
}

// NewRepository builds the store over a pool.
func NewRepository(pool db.Pool) *Repository {
	return &Repository{pool: pool}
}

const (
	insertBox = `
		INSERT INTO entry_boxes (name, address_ct, provider, location, rented_until, ssh_user, proto, region, domain_ct,
		                         provider_instance_id, provider_region, plan, monthly_cents)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`
	selectBoxAccess = `
		SELECT address_ct, ssh_user, coalesce(host_key, ''), host_key_forgotten_at IS NOT NULL,
		       proto, coalesce(region, ''), retired_at IS NOT NULL, domain_ct
		FROM entry_boxes WHERE name = $1`
	// Only the first key sticks: a change is refused at dial time, and
	// forgetting the key is an operator act (ForgetHostKey), not an overwrite.
	// Returns whether the key taken replaces a forgotten one — the operator's
	// job meeting a reinstalled machine; no row when a key is on record.
	setBoxHostKey = `
		WITH prev AS (
			SELECT name, host_key_forgotten_at IS NOT NULL AS forgotten
			FROM entry_boxes WHERE name = $1 AND host_key IS NULL FOR UPDATE)
		UPDATE entry_boxes b SET host_key = $2, host_key_forgotten_at = NULL
		FROM prev WHERE b.name = prev.name
		RETURNING prev.forgotten`
	clearBoxHostKey = `UPDATE entry_boxes SET host_key = NULL, host_key_forgotten_at = now() WHERE name = $1`
	setBoxEdge      = `
		UPDATE entry_boxes SET arch = $2, edge_sha = $3, edge_seen_at = now() WHERE name = $1`
	lockBox = `
		SELECT proto, retired_at IS NOT NULL, domain_ct FROM entry_boxes WHERE name = $1 FOR UPDATE`
	relaysOnBox = `
		SELECT name, status, coalesce(died_reason, ''), identity_ct FROM entry_relays WHERE box = $1 ORDER BY id`
	// The box a relay lives on, its state, and the protocol it speaks — the
	// protocol of its box (migration 049), never a claim the relay makes
	// about itself.
	pulseSourceOfRelay = `
		SELECT r.box, r.status, b.proto FROM entry_relays r JOIN entry_boxes b ON b.name = r.box WHERE r.name = $1`
	insertPendingRelay = `
		INSERT INTO entry_relays (name, kind, box, identity_ct) VALUES ($1, $2, $3, $4)`
	// The address a relay is signed with: its box's, or while the box has an
	// active РФ-каскад front (migration 011), the front's — the client dials
	// the front, which hands the connection to the box.
	lockRelayByName = `
		SELECT r.id, r.kind, r.status, r.box, r.identity_ct,
		       coalesce((SELECT f.address_ct FROM entry_fronts f
		                 WHERE f.exit_box = b.name AND f.retired_at IS NULL), b.address_ct),
		       coalesce(b.region, '')
		FROM entry_relays r JOIN entry_boxes b ON b.name = r.box
		WHERE r.name = $1 FOR UPDATE OF r`
	// $6: admitted now (pending → alive); a re-sign keeps the state.
	setRelayRecord = `
		UPDATE entry_relays
		SET status = CASE WHEN $6 THEN 'alive' ELSE status END,
		    key_id = $2, envelope_ct = $3, issued_at = $4, expires_at = $5, signed_at = now()
		WHERE id = $1`
	// Records the hub renews (П6.10): every state that keeps one, expiring
	// before $1.
	expiringRelays = `
		SELECT name FROM entry_relays
		WHERE status IN ('alive', 'testing', 'draining', 'suspect') AND expires_at < $1
		ORDER BY expires_at`
	listRelays = `
		SELECT r.name, r.kind, r.status, r.box, r.key_id, r.issued_at, r.expires_at,
		       r.signed_at, r.created_at, r.died_at, coalesce(r.died_reason, ''),
		       (SELECT count(*) FROM entry_grants g
		        WHERE g.relay_id = r.id AND g.revoked_at IS NULL),
		       r.identity_ct
		FROM entry_relays r ORDER BY r.name`
	listBoxes = `
		SELECT name, address_ct, provider, location, rented_until, created_at,
		       arch, edge_sha, edge_seen_at, proto, coalesce(region, ''), retired_at, domain_ct,
		       provider_instance_id, provider_region, plan, monthly_cents, released_at, retired_reason
		FROM entry_boxes ORDER BY name`
	lockBoxes = `LOCK TABLE entry_boxes IN SHARE ROW EXCLUSIVE MODE`
	// Any relay not dead yet can die: a pending one on a box that failed its
	// admission probe, a testing or draining one whose box went down mid-rollout.
	markRelayDead = `
		UPDATE entry_relays SET status = 'dead', died_at = now(), died_reason = $2
		WHERE name = $1 AND status <> 'dead'
		RETURNING id`
	// A box handed back (П6.10): every relay on it not dead yet dies with one
	// reason, and the box is marked.
	retireBoxRelays = `
		UPDATE entry_relays SET status = 'dead', died_at = now(), died_reason = $2
		WHERE box = $1 AND status <> 'dead'
		RETURNING id, name`
	// The box carries the same reason as the relays that died with it (064):
	// the provider comparison asks "handed back healthy or banned", and a
	// retire that left it null would read as a machine that died of nothing.
	markBoxRetired    = `UPDATE entry_boxes SET retired_at = now(), retired_reason = $2 WHERE name = $1`
	revokeRelayGrants = `
		UPDATE entry_grants SET revoked_at = now(), revoke_reason = $2
		WHERE relay_id = ANY($1) AND revoked_at IS NULL
		RETURNING account_id`
	// A rollout takes a box's alive relays out of the pool (П6.14): testing on
	// the canary, draining on the box next in line. Only alive ones: a pending
	// relay was never handed out and has nobody to move.
	drainBoxRelays = `
		UPDATE entry_relays SET status = $2
		WHERE box = $1 AND status = 'alive'
		RETURNING id`
	restoreBoxRelays = `
		UPDATE entry_relays SET status = 'alive'
		WHERE box = $1 AND status IN ('testing', 'draining')
		RETURNING name`

	// One batch decision per account at a time: two concurrent requests must
	// not each fill the same empty slot with a different relay.
	lockAccountBatch = `
		SELECT pg_advisory_xact_lock(hashtextextended('entry-batch:' || $1::text, 0))`
	activeGrants = `
		SELECT r.id, r.envelope_ct, r.expires_at, b.proto, coalesce(b.region, '')
		FROM entry_grants g JOIN entry_relays r ON r.id = g.relay_id
		JOIN entry_boxes b ON b.name = r.box
		WHERE g.account_id = $1 AND g.revoked_at IS NULL
		ORDER BY g.granted_at, g.id`
	// The grants of one lane of an account's batch (see lane): $2 the box
	// protocol, $3 the voice region ('' for the stream lane).
	laneGrants = `
		SELECT g.id, g.granted_at, g.revoked_at, g.revoke_reason
		FROM entry_grants g JOIN entry_relays r ON r.id = g.relay_id
		JOIN entry_boxes b ON b.name = r.box
		WHERE g.account_id = $1 AND b.proto = $2 AND coalesce(b.region, '') = $3`
	everGranted = `
		SELECT EXISTS (` + laneGrants + `)`
	// Why a lane of this account's batch has empty slots: the reasons of the
	// lane's grants revoked since the lane was last granted anything, oldest
	// first. A slot whose refill already came is not in it; a partial refill
	// (too few candidates) forgets the rest, and those slots count as deaths,
	// as before П6.14.
	unrefilledReasons = `
		WITH lane AS (` + laneGrants + `)
		SELECT revoke_reason FROM lane
		WHERE revoked_at IS NOT NULL AND revoked_at > (SELECT max(granted_at) FROM lane)
		ORDER BY revoked_at, id`
	// Personal, alive, not past expiry, and not already in this account's
	// batch. A relay this account held before and that died is dead, so it
	// cannot come back through here.
	// A candidate stands on a machine the account holds nothing on yet (a
	// batch spans distinct addresses) and that is shared by fewer than $3
	// accounts (subscribers.go).
	grantCandidates = `
		SELECT r.id, r.name, b.proto, coalesce(b.region, ''), b.name
		FROM entry_relays r JOIN entry_boxes b ON b.name = r.box
		WHERE r.kind = 'personal' AND r.status = 'alive' AND r.expires_at > $2
		  AND NOT EXISTS (SELECT 1 FROM entry_grants g JOIN entry_relays gr ON gr.id = g.relay_id
		                  WHERE g.account_id = $1 AND g.revoked_at IS NULL AND gr.box = r.box)
		  AND (SELECT count(DISTINCT g.account_id) FROM entry_grants g JOIN entry_relays gr ON gr.id = g.relay_id
		       WHERE g.revoked_at IS NULL AND gr.box = r.box) < $3`
	selectAliveEnvelope = `
		SELECT envelope_ct FROM entry_relays WHERE name = $1 AND status = 'alive'`
	insertGrant = `
		INSERT INTO entry_grants (account_id, relay_id, trigger, meta_ct)
		VALUES ($1, $2, $3, $4)`
	selectAccountCreated = `
		SELECT created_at FROM hub_accounts WHERE id = $1`

	// A report counts only for a relay this account was handed (live grant or
	// not), and at most once per (account, relay, stage) per hour.
	insertFailReport = `
		INSERT INTO entry_fail_reports (account_id, relay_id, stage, observed_at, meta_ct)
		SELECT $1, r.id, $3, $4, $5
		FROM entry_relays r
		WHERE r.name = $2
		  AND EXISTS (SELECT 1 FROM entry_grants g
		              WHERE g.account_id = $1 AND g.relay_id = r.id)
		  AND NOT EXISTS (SELECT 1 FROM entry_fail_reports f
		                  WHERE f.account_id = $1 AND f.relay_id = r.id AND f.stage = $3
		                    AND f.received_at > now() - interval '1 hour')`

	// The current source list is a singleton (id = true). The bundle path reads
	// the envelope often; the import path locks issued_at to enforce newest-wins.
	selectSources = `
		SELECT envelope, expires_at FROM entry_sources WHERE id = true`
	upsertSources = `
		INSERT INTO entry_sources (id, key_id, envelope, issued_at, expires_at, imported_at)
		VALUES (true, $1, $2, $3, $4, now())
		ON CONFLICT (id) DO UPDATE
		SET key_id = $1, envelope = $2, issued_at = $3, expires_at = $4, imported_at = now()`
	selectSourcesStatus = `
		SELECT key_id, issued_at, expires_at, imported_at FROM entry_sources WHERE id = true`
)

// storedRelay is one relay locked for a transaction, with its box's sealed
// address: what signing it needs.
type storedRelay struct {
	id         int64
	kind       string
	status     string
	box        string
	identityCT []byte
	addressCT  []byte
	// region is the box's voice region; "" on a stream box.
	region string
}

// lockRelay returns the relay under `name`, locked for the transaction, or nil.
func (r *Repository) lockRelay(ctx context.Context, tx pgx.Tx, name string) (*storedRelay, error) {
	var s storedRelay
	err := tx.QueryRow(ctx, lockRelayByName, name).
		Scan(&s.id, &s.kind, &s.status, &s.box, &s.identityCT, &s.addressCT, &s.region)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// setRecord stores a fresh record; admit also makes the relay alive.
func (r *Repository) setRecord(ctx context.Context, tx pgx.Tx, id int64, admit bool, keyID string, envelopeCT []byte, issued, expires time.Time) error {
	_, err := tx.Exec(ctx, setRelayRecord, id, keyID, envelopeCT, issued, expires, admit)
	return err
}

// expiringRelays names the relays whose record runs out before `before`,
// soonest first.
func (r *Repository) expiringRelays(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := r.pool.Query(ctx, expiringRelays, before)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// insertBox records a box; domainCT is nil on every box but an XHTTP one.
func (r *Repository) insertBox(ctx context.Context, tx pgx.Tx, b BoxSpec, addressCT, domainCT []byte) error {
	var region *string
	if b.Region != "" {
		region = &b.Region
	}
	// An empty instance id and an absent price are both NULL, not "" and 0: a
	// hand-rented machine has no instance and no known rent, and zero would
	// read as free in the fleet's bill.
	var instanceID *string
	if b.InstanceID != "" {
		instanceID = &b.InstanceID
	}
	var monthly *int
	if b.MonthlyCents > 0 {
		monthly = &b.MonthlyCents
	}
	_, err := tx.Exec(ctx, insertBox, b.Name, addressCT, b.Provider, b.Location, b.RentedUntil, b.SSHUser, b.Proto,
		region, domainCT, instanceID, b.ProviderRegion, b.Plan, monthly)
	return err
}

// lockBox returns the box's protocol, the row locked so relay names on it are
// handed out one at a time; ok=false when there is no such box.
// lockedBox is what a box's row lock tells the caller.
type lockedBox struct {
	proto   string
	retired bool
	// domainCT is an XHTTP box's sealed domain, nil on the others.
	domainCT []byte
}

func (r *Repository) lockBox(ctx context.Context, tx pgx.Tx, name string) (b lockedBox, ok bool, err error) {
	err = tx.QueryRow(ctx, lockBox, name).Scan(&b.proto, &b.retired, &b.domainCT)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedBox{}, false, nil
	}
	return b, err == nil, err
}

type boxRelay struct {
	name       string
	status     string
	diedReason string
	identityCT []byte
}

func (r *Repository) relaysOnBox(ctx context.Context, tx pgx.Tx, box string) ([]boxRelay, error) {
	rows, err := tx.Query(ctx, relaysOnBox, box)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []boxRelay
	for rows.Next() {
		var b boxRelay
		if err := rows.Scan(&b.name, &b.status, &b.diedReason, &b.identityCT); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *Repository) insertPendingRelay(ctx context.Context, tx pgx.Tx, name, kind, box string, identityCT []byte) error {
	_, err := tx.Exec(ctx, insertPendingRelay, name, kind, box, identityCT)
	return err
}

// PoolRelay is one row of the pool as the owner sees it. Address comes from
// the box, port and protocol from the sealed identity, opened when the console
// asks (see [Service.List]); credentials never leave the seal.
type PoolRelay struct {
	Name         string     `json:"name"`
	Kind         string     `json:"kind"`
	Status       string     `json:"status"`
	Box          string     `json:"box"`
	KeyID        string     `json:"keyId,omitempty"`
	IssuedAt     *time.Time `json:"issuedAt,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	SignedAt     *time.Time `json:"signedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	DiedAt       *time.Time `json:"diedAt,omitempty"`
	DiedReason   string     `json:"diedReason,omitempty"`
	ActiveGrants int        `json:"activeGrants"`
	Address      string     `json:"address"`
	Port         int        `json:"port"`
	Proto        string     `json:"proto"`
	// Covers are the cover sites a Reality relay races, in race order; empty
	// on the other protocols (see shortOfCovers).
	Covers []string `json:"covers,omitempty"`
	// Unreadable: the server key could not open the identity or the box
	// address (sealed under a key since rotated away).
	Unreadable bool `json:"unreadable,omitempty"`

	identityCT []byte
}

func (r *Repository) list(ctx context.Context) ([]PoolRelay, error) {
	rows, err := r.pool.Query(ctx, listRelays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PoolRelay
	for rows.Next() {
		var p PoolRelay
		var keyID *string
		if err := rows.Scan(&p.Name, &p.Kind, &p.Status, &p.Box, &keyID, &p.IssuedAt, &p.ExpiresAt,
			&p.SignedAt, &p.CreatedAt, &p.DiedAt, &p.DiedReason, &p.ActiveGrants, &p.identityCT); err != nil {
			return nil, err
		}
		if keyID != nil {
			p.KeyID = *keyID
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PoolBox is one machine of the inventory as the owner sees it, address
// opened (see [Service.Boxes]).
type PoolBox struct {
	Name        string     `json:"name"`
	Address     string     `json:"address"`
	Provider    string     `json:"provider"`
	Location    string     `json:"location"`
	RentedUntil *time.Time `json:"rentedUntil,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	// Arch and EdgeSHA are the relay build the hub last saw or put on the box
	// (П6.14); empty when it never looked.
	Arch       string     `json:"arch,omitempty"`
	EdgeSHA    string     `json:"edgeSha,omitempty"`
	EdgeSeenAt *time.Time `json:"edgeSeenAt,omitempty"`
	// Proto is what the box speaks; Region the voice region of a Hysteria2 box.
	Proto  string `json:"proto"`
	Region string `json:"region,omitempty"`
	// RetiredAt: handed back to the provider (П6.10); nil while in service.
	RetiredAt *time.Time `json:"retiredAt,omitempty"`
	// Domain is an XHTTP box's own domain (П4в), opened from its seal like
	// the address; empty on the other protocols.
	Domain string `json:"domain,omitempty"`
	// InstanceID, ProviderRegion, Plan and MonthlyCents are the rental as the
	// hoster sold it (П9.2). Empty on a machine rented by hand: the hub can
	// neither reboot nor hand that one back, and does not know its price.
	InstanceID     string `json:"instanceId,omitempty"`
	ProviderRegion string `json:"providerRegion,omitempty"`
	Plan           string `json:"plan,omitempty"`
	MonthlyCents   *int   `json:"monthlyCents,omitempty"`
	// Relays on this box, in name order, filled by the service.
	Relays []string `json:"relays"`
	// Unreadable: the server key could not open the address.
	Unreadable bool `json:"unreadable,omitempty"`

	addressCT []byte
	domainCT  []byte
	// releasedAt: the hoster confirmed the machine gone, and with it the
	// address. retiredReason: why it was handed back.
	releasedAt    *time.Time
	retiredReason *string
}

// holdsAddress is whether the box still rules its address out for a new box.
// A hoster hands a released address to its next order, so a released machine
// holds it only if the censor is why it left: a fresh box there would be born
// banned.
func (b PoolBox) holdsAddress() bool {
	return b.releasedAt == nil || (b.retiredReason != nil && slices.Contains(CensorDeaths, *b.retiredReason))
}

func (r *Repository) boxes(ctx context.Context, q db.DBTX) ([]PoolBox, error) {
	rows, err := q.Query(ctx, listBoxes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PoolBox
	for rows.Next() {
		var b PoolBox
		var arch, sha, instanceID *string
		if err := rows.Scan(&b.Name, &b.addressCT, &b.Provider, &b.Location, &b.RentedUntil,
			&b.CreatedAt, &arch, &sha, &b.EdgeSeenAt, &b.Proto, &b.Region, &b.RetiredAt, &b.domainCT,
			&instanceID, &b.ProviderRegion, &b.Plan, &b.MonthlyCents, &b.releasedAt, &b.retiredReason); err != nil {
			return nil, err
		}
		if arch != nil {
			b.Arch = *arch
		}
		if sha != nil {
			b.EdgeSHA = *sha
		}
		if instanceID != nil {
			b.InstanceID = *instanceID
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// lockBoxes serialises box creation: the one-box-per-address rule is checked
// against the whole table (addresses are sealed), which a row lock cannot cover.
func (r *Repository) lockBoxes(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, lockBoxes)
	return err
}

// markDead kills the relay `name` (any state but dead) and revokes its live
// grants, returning the accounts that held one — the holders whose batch just
// lost a slot; ok=false when no such relay is still around.
func (r *Repository) markDead(ctx context.Context, tx pgx.Tx, name, reason string) (holders []uuid.UUID, ok bool, err error) {
	var id int64
	err = tx.QueryRow(ctx, markRelayDead, name, reason).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	holders, err = r.revokeGrants(ctx, tx, []int64{id}, revokeRelayDead)
	if err != nil {
		return nil, false, err
	}
	return holders, true, nil
}

// retireBox kills every relay on the box not dead yet with `reason`, revokes
// their live grants and marks the box retired; returns the relays that died
// and who held them.
func (r *Repository) retireBox(ctx context.Context, tx pgx.Tx, box, reason string) (names []string, holders []uuid.UUID, err error) {
	rows, err := tx.Query(ctx, retireBoxRelays, box, reason)
	if err != nil {
		return nil, nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids, names = append(ids, id), append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(ids) > 0 {
		if holders, err = r.revokeGrants(ctx, tx, ids, revokeRelayDead); err != nil {
			return nil, nil, err
		}
	}
	if _, err := tx.Exec(ctx, markBoxRetired, box, reason); err != nil {
		return nil, nil, err
	}
	return names, holders, nil
}

// Why a grant was revoked (entry_grants.revoke_reason).
const (
	revokeRelayDead = "relay_dead"
	revokeDrained   = "drained"
)

// revokeGrants revokes the live grants of the relays and returns their
// holders, each once.
func (r *Repository) revokeGrants(ctx context.Context, tx pgx.Tx, relays []int64, reason string) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, revokeRelayGrants, relays, reason)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, err
	}
	seen := make(map[uuid.UUID]bool, len(all))
	holders := make([]uuid.UUID, 0, len(all))
	for _, a := range all {
		if !seen[a] {
			seen[a] = true
			holders = append(holders, a)
		}
	}
	return holders, nil
}

// drainBox moves the box's alive relays to `status` (testing or draining) and
// revokes their live grants as drained; returns how many relays moved and who
// held them.
func (r *Repository) drainBox(ctx context.Context, tx pgx.Tx, box, status string) (moved int, holders []uuid.UUID, err error) {
	rows, err := tx.Query(ctx, drainBoxRelays, box, status)
	if err != nil {
		return 0, nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, nil, err
	}
	if len(ids) == 0 {
		return 0, nil, nil
	}
	holders, err = r.revokeGrants(ctx, tx, ids, revokeDrained)
	return len(ids), holders, err
}

// restoreBox puts the box's testing and draining relays back in the pool.
func (r *Repository) restoreBox(ctx context.Context, box string) ([]string, error) {
	rows, err := r.pool.Query(ctx, restoreBoxRelays, box)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *Repository) unrefilledReasons(ctx context.Context, tx pgx.Tx, account uuid.UUID, l lane) ([]string, error) {
	rows, err := tx.Query(ctx, unrefilledReasons, account, l.proto, l.region)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

type grantedRelay struct {
	relayID    int64
	envelopeCT []byte
	expiresAt  time.Time
	lane       lane
}

// lane is the part of a batch a relay fills: the stream relays (Reality), or
// the voice relays of one region. Each lane has its own number of slots, so a
// voice entry never takes a chat path's place.
type lane struct {
	proto  string
	region string
}

func (r *Repository) lockBatch(ctx context.Context, tx pgx.Tx, account uuid.UUID) error {
	_, err := tx.Exec(ctx, lockAccountBatch, account)
	return err
}

func (r *Repository) activeGrants(ctx context.Context, tx pgx.Tx, account uuid.UUID) ([]grantedRelay, error) {
	rows, err := tx.Query(ctx, activeGrants, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []grantedRelay
	for rows.Next() {
		var g grantedRelay
		if err := rows.Scan(&g.relayID, &g.envelopeCT, &g.expiresAt, &g.lane.proto, &g.lane.region); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *Repository) everGranted(ctx context.Context, tx pgx.Tx, account uuid.UUID, l lane) (bool, error) {
	var ever bool
	err := tx.QueryRow(ctx, everGranted, account, l.proto, l.region).Scan(&ever)
	return ever, err
}

type candidate struct {
	id   int64
	name string
	lane lane
	// box is the machine the relay stands on: one per batch (pick).
	box string
}

func (r *Repository) candidates(ctx context.Context, tx pgx.Tx, account uuid.UUID, now time.Time) ([]candidate, error) {
	rows, err := tx.Query(ctx, grantCandidates, account, now, SubscribersPerBox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.name, &c.lane.proto, &c.lane.region, &c.box); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) accountCreated(ctx context.Context, tx pgx.Tx, account uuid.UUID) (time.Time, error) {
	var at time.Time
	err := tx.QueryRow(ctx, selectAccountCreated, account).Scan(&at)
	return at, err
}

// insertFailReport stores one report; false when it was not kept (a relay this
// account never held, or a repeat within the hour).
func (r *Repository) insertFailReport(ctx context.Context, account uuid.UUID, relay, stage string, observedAt time.Time, metaCT []byte) (bool, error) {
	tag, err := r.pool.Exec(ctx, insertFailReport, account, relay, stage, observedAt, metaCT)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) insertGrant(ctx context.Context, tx pgx.Tx, account uuid.UUID, relayID int64, trigger string, metaCT []byte) error {
	_, err := tx.Exec(ctx, insertGrant, account, relayID, trigger, metaCT)
	return err
}

// storedSources is the current source list as the operator sees it: its
// version, lifetime and when it landed — no envelope bytes.
type storedSources struct {
	keyID      string
	issuedAt   time.Time
	expiresAt  time.Time
	importedAt time.Time
}

// currentSources returns the stored signed source-list envelope and its expiry,
// or ok=false when none is imported. Read-only: the bundle path reads it on
// every non-unchanged answer.
func (r *Repository) currentSources(ctx context.Context) (envelope []byte, expiresAt time.Time, ok bool, err error) {
	err = r.pool.QueryRow(ctx, selectSources).Scan(&envelope, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	return envelope, expiresAt, true, nil
}

// upsertSources writes the singleton row (insert or replace).
func (r *Repository) upsertSources(ctx context.Context, tx pgx.Tx, keyID string, envelope []byte, issuedAt, expiresAt time.Time) error {
	_, err := tx.Exec(ctx, upsertSources, keyID, envelope, issuedAt, expiresAt)
	return err
}

// sourcesStatus is the current list for the operator, or nil when none.
func (r *Repository) sourcesStatus(ctx context.Context) (*storedSources, error) {
	var s storedSources
	err := r.pool.QueryRow(ctx, selectSourcesStatus).Scan(&s.keyID, &s.issuedAt, &s.expiresAt, &s.importedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// pulseSource is what a relay's pulse is checked and filed against.
type pulseSource struct {
	box, status, proto string
}

// pulseSource reads the box a relay lives on, its state and its protocol.
// [ErrNoSuchRelay] when the pool holds no relay of that name.
func (r *Repository) pulseSource(ctx context.Context, name string) (pulseSource, error) {
	var s pulseSource
	if err := r.pool.QueryRow(ctx, pulseSourceOfRelay, name).Scan(&s.box, &s.status, &s.proto); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pulseSource{}, ErrNoSuchRelay
		}
		return pulseSource{}, fmt.Errorf("entry: pulse source of relay %s: %w", name, err)
	}
	return s, nil
}
