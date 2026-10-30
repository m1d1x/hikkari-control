package entry

// vantages.go owns the observation point registry (`entry_vantages`, migration
// 068, roadmap 42 К3′ / П9.12).
//
// # Why slots live in Postgres while probes live in ClickHouse
//
// A prober that goes quiet stops writing rows to `entry_probes`. When the list
// of observation points was derived from `SELECT DISTINCT source_region FROM
// entry_probes`, a silent point vanished from the screen once its last row fell
// behind the time window — indistinguishable from a point that never existed.
// Keeping the slot and the machine assigned to it in Postgres makes "silent" a
// property of a known slot rather than the absence of a row.
//
// # Machine addresses stay sealed
//
// A slot can point at a box from `entry_boxes` (`box`) or carry its own host
// (`host_ct`), because an observation VPS inside Russia is not necessarily an
// entry relay box. Either way the IP or hostname is sealed with `cryptobox` at
// rest and opened only for the owner on the admin plane.
//
// # Who is speaking is the hub's answer, not the request's
//
// Every route a prober talks to (verdict ingest, the watch list, claiming and
// finishing tasks, the bench) learns the slot from the bearer token: the hub
// issues one per slot, keeps its SHA-256 in `entry_vantages.token_hash`
// (migration 074) and answers [Service.VantageByToken] only for an active
// slot. No body field names the observation point any more. It used to — one
// shared token and `sourceRegion` in the body — and that made the detector's
// "two vantages agree" and the claim's `claimed_by` the sender's word (audit
// 42-autopilot К1, В2).
//
// # Never on a machine of the fleet
//
// The owner's decision of 27.10: a box that carries relays is the population
// most likely to be seized, and a vantage on it would both judge the fleet and
// be handed the dialling credentials of the whole pool (В1). So a slot cannot
// point at a box with relays, and a box inside an active slot cannot be given
// any (AddRelay). Both sides lock the box row, so the two cannot race.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
)

// Vantage lifecycle states.
const (
	VantageActive  = "active"
	VantageRetired = "retired"
)

// ErrNoSuchVantage: no observation point slot under that key.
var ErrNoSuchVantage = errors.New("entry: no such vantage")

// Vantage is one observation point slot and the machine currently inside it.
type Vantage struct {
	Slot       string     `json:"slot"`
	Title      string     `json:"title"`
	Box        string     `json:"box,omitempty"`
	Host       string     `json:"host,omitempty"`
	Provider   string     `json:"provider"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	RetiredAt  *time.Time `json:"retiredAt,omitempty"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	UpdatedBy  string     `json:"updatedBy"`
	Unreadable bool       `json:"unreadable,omitempty"`
	JobID      *uuid.UUID `json:"jobId,omitempty"`
	// Token is the slot's bearer, present only in the answer that issued it:
	// the hub keeps its hash and can never show it again. A slot on a box
	// gets its token planted by DeployProber instead.
	Token string `json:"token,omitempty"`
}

// VantageSpec describes a slot creation or machine replacement request.
type VantageSpec struct {
	Slot     string `json:"slot"`
	Title    string `json:"title"`
	Box      string `json:"box"`
	Host     string `json:"host"`
	Provider string `json:"provider"`
}

// VantageSlots returns every registered observation point slot, active first,
// with the machine address opened for the owner. When a slot references a box
// in `entry_boxes` and has no standalone host or provider of its own, it
// inherits both from that box.
func (s *Service) VantageSlots(ctx context.Context) ([]Vantage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.slot, v.title, COALESCE(v.box, ''),
		       COALESCE(v.host_ct, b.address_ct),
		       CASE WHEN v.provider <> '' THEN v.provider ELSE COALESCE(b.provider, '') END,
		       v.status, v.created_at, v.retired_at, v.updated_at, v.updated_by
		FROM entry_vantages v
		LEFT JOIN entry_boxes b ON b.name = v.box
		ORDER BY (v.status = 'active') DESC, v.created_at ASC, v.slot ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("entry: vantage slots: %w", err)
	}
	defer rows.Close()

	out := []Vantage{}
	for rows.Next() {
		v, err := scanVantage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// VantageSlot reads one slot by its key.
func (s *Service) VantageSlot(ctx context.Context, slot string) (Vantage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT v.slot, v.title, COALESCE(v.box, ''),
		       COALESCE(v.host_ct, b.address_ct),
		       CASE WHEN v.provider <> '' THEN v.provider ELSE COALESCE(b.provider, '') END,
		       v.status, v.created_at, v.retired_at, v.updated_at, v.updated_by
		FROM entry_vantages v
		LEFT JOIN entry_boxes b ON b.name = v.box
		WHERE v.slot = $1
	`, slot)
	if err != nil {
		return Vantage{}, fmt.Errorf("entry: vantage slot %s: %w", slot, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Vantage{}, err
		}
		return Vantage{}, fmt.Errorf("%w: %s", ErrNoSuchVantage, slot)
	}
	return scanVantage(rows)
}

func scanVantage(rows pgx.Rows) (Vantage, error) {
	var (
		v      Vantage
		hostCT []byte
	)
	if err := rows.Scan(
		&v.Slot, &v.Title, &v.Box, &hostCT, &v.Provider,
		&v.Status, &v.CreatedAt, &v.RetiredAt, &v.UpdatedAt, &v.UpdatedBy,
	); err != nil {
		return Vantage{}, err
	}
	if len(hostCT) > 0 {
		plain, err := cryptobox.Decrypt(hostCT)
		if err != nil {
			v.Unreadable = true
		} else {
			v.Host = string(plain)
		}
	}
	return v, nil
}

// CreateVantage opens a new observation point slot (or re-opens a retired one
// under the same key). When spec.Slot is empty, the hub picks the next system
// slot letter ("a", "b", "c", ...). If an active slot already exists under the
// given key, the call is refused: replacing the machine in an active slot must
// go through SaveVantage, which sits behind the step-up gate.
func (s *Service) CreateVantage(ctx context.Context, spec VantageSpec, actor string) (Vantage, error) {
	if strings.TrimSpace(spec.Slot) == "" {
		next, err := s.nextVantageSlot(ctx)
		if err != nil {
			return Vantage{}, err
		}
		spec.Slot = next
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Vantage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	spec, hostCT, boxPtr, err := s.prepareVantageSpec(ctx, tx, spec)
	if err != nil {
		return Vantage{}, err
	}

	var existingStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM entry_vantages WHERE slot = $1 FOR UPDATE`, spec.Slot).Scan(&existingStatus)
	switch {
	case err == nil && existingStatus == VantageActive:
		return Vantage{}, fmt.Errorf("%w: слот %s уже заведён — смену машины делай через замену в слоте", ErrRejected, spec.Slot)
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return Vantage{}, err
	}

	token, hash, err := newVantageToken()
	if err != nil {
		return Vantage{}, err
	}
	now := s.now()
	_, err = tx.Exec(ctx, `
		INSERT INTO entry_vantages (slot, title, box, host_ct, provider, status, created_at, retired_at, updated_at, updated_by, token_hash)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, NULL, $6, $7, $8)
		ON CONFLICT (slot) DO UPDATE SET
			title      = EXCLUDED.title,
			box        = EXCLUDED.box,
			host_ct    = EXCLUDED.host_ct,
			provider   = EXCLUDED.provider,
			status     = 'active',
			retired_at = NULL,
			updated_at = EXCLUDED.updated_at,
			updated_by = EXCLUDED.updated_by,
			token_hash = EXCLUDED.token_hash
	`, spec.Slot, spec.Title, boxPtr, hostCT, spec.Provider, now, clip(actor, 128), hash)
	if err != nil {
		return Vantage{}, fmt.Errorf("entry: create vantage %s: %w", spec.Slot, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Vantage{}, err
	}
	v, err := s.VantageSlot(ctx, spec.Slot)
	v.Token = token
	return v, err
}

// nextVantageSlot returns the first system slot code ("a", "b", "c", ...) that
// is not currently active.
func (s *Service) nextVantageSlot(ctx context.Context) (string, error) {
	rows, err := s.pool.Query(ctx, `SELECT slot FROM entry_vantages WHERE status = 'active'`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	active := map[string]bool{}
	for rows.Next() {
		var slot string
		if err := rows.Scan(&slot); err != nil {
			return "", err
		}
		active[slot] = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for i := 0; i < 702; i++ {
		cand := slotCode(i)
		if !active[cand] {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%w: все системные слоты заняты", ErrRejected)
}

func slotCode(i int) string {
	if i < 26 {
		return string(rune('a' + i))
	}
	hi := (i / 26) - 1
	lo := i % 26
	return string([]rune{rune('a' + hi), rune('a' + lo)})
}

// boxUsedByActiveVantage reports whether any active slot currently points at box.
func (s *Service) boxUsedByActiveVantage(ctx context.Context, box string) (bool, error) {
	var used bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM entry_vantages WHERE status = 'active' AND box = $1)
	`, box).Scan(&used)
	return used, err
}

// SaveVantage puts or replaces the machine inside a slot (and reactivates the
// slot if it had been retired). When Title is omitted on an existing slot, the
// slot's existing human name is kept so a machine swap does not wipe the label.
//
// A new machine gets a new token and the old one stops working the moment
// this commits: the machine that left the slot must not keep its voice. So
// does a slot that has none yet (made before migration 074, or retired). A
// save that leaves the machine where it was keeps the token, so renaming a
// slot does not cut its prober off.
func (s *Service) SaveVantage(ctx context.Context, spec VantageSpec, actor string) (Vantage, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Vantage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	spec, hostCT, boxPtr, err := s.prepareVantageSpec(ctx, tx, spec)
	if err != nil {
		return Vantage{}, err
	}
	same, err := sameVantageMachine(ctx, tx, spec.Slot, boxPtr, hostCT)
	if err != nil {
		return Vantage{}, err
	}
	var token string
	var hash []byte
	if !same {
		if token, hash, err = newVantageToken(); err != nil {
			return Vantage{}, err
		}
	}

	now := s.now()
	_, err = tx.Exec(ctx, `
		INSERT INTO entry_vantages (slot, title, box, host_ct, provider, status, created_at, retired_at, updated_at, updated_by, token_hash)
		VALUES ($1, $2, $3, $4, $5, 'active', $6, NULL, $6, $7, $8)
		ON CONFLICT (slot) DO UPDATE SET
			title      = CASE WHEN EXCLUDED.title <> '' THEN EXCLUDED.title ELSE entry_vantages.title END,
			box        = EXCLUDED.box,
			host_ct    = EXCLUDED.host_ct,
			provider   = EXCLUDED.provider,
			status     = 'active',
			retired_at = NULL,
			updated_at = EXCLUDED.updated_at,
			updated_by = EXCLUDED.updated_by,
			token_hash = COALESCE(EXCLUDED.token_hash, entry_vantages.token_hash)
	`, spec.Slot, spec.Title, boxPtr, hostCT, spec.Provider, now, clip(actor, 128), hash)
	if err != nil {
		return Vantage{}, fmt.Errorf("entry: save vantage %s: %w", spec.Slot, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Vantage{}, err
	}
	v, err := s.VantageSlot(ctx, spec.Slot)
	v.Token = token
	return v, err
}

// sameVantageMachine says the slot is active, has a token, and already holds
// this machine: the same box, and the same standalone host once opened (the
// sealed bytes differ on every seal, so they cannot be compared).
func sameVantageMachine(ctx context.Context, tx pgx.Tx, slot string, box *string, hostCT []byte) (bool, error) {
	var (
		oldBox      *string
		oldHostCT   []byte
		active, has bool
	)
	err := tx.QueryRow(ctx, `
		SELECT box, host_ct, status = 'active', token_hash IS NOT NULL
		FROM entry_vantages WHERE slot = $1 FOR UPDATE`, slot).Scan(&oldBox, &oldHostCT, &active, &has)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || !active || !has {
		return false, err
	}
	if (oldBox == nil) != (box == nil) || (box != nil && *oldBox != *box) {
		return false, nil
	}
	if len(oldHostCT) == 0 || len(hostCT) == 0 {
		return len(oldHostCT) == len(hostCT), nil
	}
	was, err := cryptobox.Decrypt(oldHostCT)
	if err != nil {
		return false, nil
	}
	now, err := cryptobox.Decrypt(hostCT)
	if err != nil {
		return false, err
	}
	return string(was) == string(now), nil
}

// RetireVantage takes a slot out of service without erasing its history.
func (s *Service) RetireVantage(ctx context.Context, slot, actor string) (Vantage, error) {
	slot = strings.ToLower(strings.TrimSpace(slot))
	if !regionRe.MatchString(slot) {
		return Vantage{}, fmt.Errorf("%w: ключ слота %q должен быть вида ru-spb", ErrRejected, slot)
	}
	now := s.now()
	tag, err := s.pool.Exec(ctx, `
		UPDATE entry_vantages
		SET status     = 'retired',
		    token_hash = NULL,
		    retired_at = COALESCE(retired_at, $2),
		    updated_at = $2,
		    updated_by = $3
		WHERE slot = $1
	`, slot, now, clip(actor, 128))
	if err != nil {
		return Vantage{}, fmt.Errorf("entry: retire vantage %s: %w", slot, err)
	}
	if tag.RowsAffected() == 0 {
		return Vantage{}, fmt.Errorf("%w: %s", ErrNoSuchVantage, slot)
	}
	return s.VantageSlot(ctx, slot)
}

// prepareVantageSpec validates a VantageSpec, resolves a linked fleet box if
// given, and seals the machine host under cryptobox. A linked box is locked
// for the rest of tx and refused if it carries relays: see the file comment.
func (s *Service) prepareVantageSpec(ctx context.Context, tx pgx.Tx, spec VantageSpec) (VantageSpec, []byte, *string, error) {
	spec.Slot = strings.ToLower(strings.TrimSpace(spec.Slot))
	spec.Title = clip(strings.TrimSpace(spec.Title), 128)
	spec.Box = strings.TrimSpace(spec.Box)
	spec.Host = strings.ToLower(strings.TrimSpace(spec.Host))
	spec.Provider = clip(strings.TrimSpace(spec.Provider), 64)

	if !regionRe.MatchString(spec.Slot) {
		return VantageSpec{}, nil, nil, fmt.Errorf(
			"%w: ключ слота %q должен состоять из строчных латинских букв, цифр и дефиса (до 32 знаков, например ru-spb)",
			ErrRejected, spec.Slot)
	}

	var (
		hostCT []byte
		boxPtr *string
	)
	if spec.Box != "" {
		var (
			boxAddrCT   []byte
			boxProvider string
			retired     bool
		)
		err := tx.QueryRow(ctx, `
			SELECT address_ct, provider, retired_at IS NOT NULL
			FROM entry_boxes
			WHERE name = $1
			FOR UPDATE
		`, spec.Box).Scan(&boxAddrCT, &boxProvider, &retired)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return VantageSpec{}, nil, nil, fmt.Errorf("%w: %s", ErrNoSuchBox, spec.Box)
		case err != nil:
			return VantageSpec{}, nil, nil, err
		case retired:
			return VantageSpec{}, nil, nil, fmt.Errorf("%w: %s", ErrBoxRetired, spec.Box)
		}
		if err := refuseFleetBox(ctx, tx, spec.Box); err != nil {
			return VantageSpec{}, nil, nil, err
		}
		boxName := spec.Box
		boxPtr = &boxName
		hostCT = boxAddrCT
		if spec.Provider == "" {
			spec.Provider = boxProvider
		}
	}

	if spec.Host != "" {
		if net.ParseIP(spec.Host) == nil && !domainRe.MatchString(spec.Host) {
			return VantageSpec{}, nil, nil, fmt.Errorf(
				"%w: хост %q должен быть IP-адресом или доменным именем", ErrRejected, spec.Host)
		}
		ct, err := cryptobox.Encrypt([]byte(spec.Host))
		if err != nil {
			return VantageSpec{}, nil, nil, err
		}
		hostCT = ct
	}

	return spec, hostCT, boxPtr, nil
}

// boxCarriesRelays: any relay ever minted on the box, dead or alive. A box
// whose relays died is still a machine of the fleet, provisioned with the
// fleet's secrets and one repair away from carrying relays again.
const boxCarriesRelays = `SELECT EXISTS (SELECT 1 FROM entry_relays WHERE box = $1)`

// boxServesVantage: an active slot points at the box.
const boxServesVantage = `SELECT EXISTS (SELECT 1 FROM entry_vantages WHERE box = $1 AND status = 'active')`

// refuseFleetBox refuses a box of the fleet as an observation point (owner's
// decision 27.10, audit 42-autopilot В1). The caller holds the box row locked.
func refuseFleetBox(ctx context.Context, q pgx.Tx, box string) error {
	var fleet bool
	if err := q.QueryRow(ctx, boxCarriesRelays, box).Scan(&fleet); err != nil {
		return err
	}
	if fleet {
		return fmt.Errorf("%w: на машине %s есть релеи — точка наблюдения на машине флота запрещена: захваченная машина судила бы флот и получала бы адреса всего пула",
			ErrRejected, box)
	}
	return nil
}

// refuseVantageBox is refuseFleetBox from the other side: a box inside an
// active slot gets no relays. The caller holds the box row locked.
func refuseVantageBox(ctx context.Context, q pgx.Tx, box string) error {
	var serves bool
	if err := q.QueryRow(ctx, boxServesVantage, box).Scan(&serves); err != nil {
		return err
	}
	if serves {
		return fmt.Errorf("%w: машина %s — точка наблюдения, релеев на ней не будет", ErrRejected, box)
	}
	return nil
}

// newVantageToken makes a slot's bearer and the hash the hub keeps of it.
func newVantageToken() (token string, hash []byte, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	token = hex.EncodeToString(raw[:])
	return token, vantageTokenHash(token), nil
}

func vantageTokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// proberEnv renders /etc/nekko-entry-prober/prober.env for slot: what
// DeployProber plants on a box and what the admin screen shows once for a
// standalone VPS. apiURL is the hub's public base URL; xrayBin is left out
// when empty (the prober then runs "xray" from PATH).
func proberEnv(apiURL, slot, token, xrayBin string) string {
	var env strings.Builder
	env.WriteString("# Written by the nekko hub for observation slot " + slot + ". Not the source of truth.\n")
	env.WriteString("ENTRY_PROBE_TUNNEL_URL=" + apiURL + "/healthz\n")
	env.WriteString("PROBE_INGEST_URL=" + apiURL + "/api/v1/internal/entry-probes\n")
	env.WriteString("PROBE_INGEST_TOKEN=" + token + "\n")
	if xrayBin != "" {
		env.WriteString("ENTRY_PROBE_XRAY_BIN=" + xrayBin + "\n")
	}
	return env.String()
}

// IssueVantageToken gives an active slot a new token and returns it; the one
// it had stops working. For a slot whose machine is not the hub's to reach
// (a standalone host): the owner carries the token to it by hand.
func (s *Service) IssueVantageToken(ctx context.Context, slot string) (string, error) {
	token, hash, err := newVantageToken()
	if err != nil {
		return "", err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE entry_vantages SET token_hash = $2, updated_at = $3
		WHERE slot = $1 AND status = 'active'`, slot, hash, s.now())
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("%w: %s (или он выведен)", ErrNoSuchVantage, slot)
	}
	return token, nil
}

// issueBoxVantageToken is IssueVantageToken for DeployProber: the slot must
// be active and hold box, and box must carry no relays. The box row is locked
// so a relay cannot be minted on it in between (AddRelay locks it too).
func (s *Service) issueBoxVantageToken(ctx context.Context, slot, box string) (string, error) {
	return s.boxVantageToken(ctx, slot, box, true)
}

// mayIssueBoxVantageToken runs issueBoxVantageToken's checks and issues
// nothing: the answer a deploy needs before it takes the box's job lock.
func (s *Service) mayIssueBoxVantageToken(ctx context.Context, slot, box string) error {
	_, err := s.boxVantageToken(ctx, slot, box, false)
	return err
}

func (s *Service) boxVantageToken(ctx context.Context, slot, box string, issue bool) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM entry_boxes WHERE name = $1 FOR UPDATE`, box); err != nil {
		return "", err
	}
	if err := refuseFleetBox(ctx, tx, box); err != nil {
		return "", err
	}
	if !issue {
		var holds bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM entry_vantages WHERE slot = $1 AND box = $2 AND status = 'active')`,
			slot, box).Scan(&holds); err != nil {
			return "", err
		}
		if !holds {
			return "", fmt.Errorf("%w: действующий слот %s не держит машину %s", ErrRejected, slot, box)
		}
		return "", nil
	}
	token, hash, err := newVantageToken()
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE entry_vantages SET token_hash = $3, updated_at = $4
		WHERE slot = $1 AND box = $2 AND status = 'active'`, slot, box, hash, s.now())
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("%w: действующий слот %s не держит машину %s", ErrRejected, slot, box)
	}
	return token, tx.Commit(ctx)
}

// VantageByToken answers which active slot a bearer token belongs to, or
// ErrNoSuchVantage. This is the only way a prober's request gets a slot.
func (s *Service) VantageByToken(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrNoSuchVantage
	}
	var slot string
	err := s.pool.QueryRow(ctx, `
		SELECT slot FROM entry_vantages WHERE token_hash = $1 AND status = 'active'`,
		vantageTokenHash(token)).Scan(&slot)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoSuchVantage
	}
	return slot, err
}

// activeVantageSet returns the slots that are active in Postgres. Verdicts
// are read only from these: a region nobody registered is the sender's word,
// and a slot just retired must stop counting while its rows age out of the
// window. An allowlist — the registry says who may speak, not who may not.
func (s *Service) activeVantageSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT slot FROM entry_vantages WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var slot string
		if err := rows.Scan(&slot); err != nil {
			return nil, err
		}
		out[slot] = true
	}
	return out, rows.Err()
}

// noteAdmissionPoll records the admission worker's control-plane contact,
// including a successful empty poll. Throttling avoids a WAL write per poll.
func (s *Service) noteAdmissionPoll(ctx context.Context, q db.DBTX, slot string) error {
	_, err := q.Exec(ctx, `UPDATE entry_vantages SET last_admission_poll_at = $2
		WHERE slot = $1 AND status = 'active'
		  AND (last_admission_poll_at IS NULL OR last_admission_poll_at < $2::timestamptz - interval '10 seconds')`, slot, s.now())
	return err
}
