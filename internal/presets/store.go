// Package presets owns the routing presets clients fetch: the groups an
// admin edits, the presets that put them on a side, the releases that freeze
// a preset into a catalog, and which release a device is served. The catalog
// format is hikkari-client's engine/core/presets/catalog.go.
package presets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Sides a group can be put on; they become the service's defaultModes.
const (
	SideBypass = "bypass"
	SideProxy  = "proxy"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrGroupInUse    = errors.New("group is in a preset")
	ErrUnknownGroup  = errors.New("unknown group")
	ErrGroupTwice    = errors.New("group is on a side twice or on both sides")
	ErrDefault       = errors.New("the default preset cannot be deleted")
	ErrNothingToShip = errors.New("preset has no groups")
	ErrNoNext        = errors.New("no release is rolling out")
	ErrExists        = errors.New("already exists")
	ErrInvalid       = errors.New("the client would reject the catalog")
)

// Rules are a group's rules in the catalog's own shape: domains and ips hold
// everywhere, processes and folders per engine platform.
type Rules struct {
	Domains   []string            `json:"domains"`
	IPs       []string            `json:"ips"`
	Processes map[string][]string `json:"processes"`
	Folders   map[string][]string `json:"folders,omitempty"`
}

type Group struct {
	ID        string
	Name      string
	Icon      string
	Rules     Rules
	UpdatedAt time.Time
}

// Service is one entry of the catalog a client fetches, field for field.
type Service struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Icon         string              `json:"icon"`
	Domains      []string            `json:"domains"`
	IPs          []string            `json:"ips"`
	Processes    map[string][]string `json:"processes"`
	Folders      map[string][]string `json:"folders,omitempty"`
	DefaultModes []string            `json:"defaultModes"`
}

type Release struct {
	ID        int64
	PresetID  string
	V         int
	Services  []Service
	CreatedBy string
	CreatedAt time.Time
}

type Preset struct {
	ID        string
	Name      string
	Default   bool
	Countries []string
	Bypass    []string
	Proxy     []string
	Disabled  []string
	// Current is what every device of the preset gets; Next, while set,
	// goes to the buckets below Stage instead.
	Current   *Release
	Next      *Release
	Stage     int
	UpdatedAt time.Time
}

// PresetDraft is what an admin edits on a preset.
type PresetDraft struct {
	Name      string
	Default   bool
	Countries []string
	Bypass    []string
	Proxy     []string
	Disabled  []string
}

type Store struct {
	pool db.Pool
}

func NewStore(pool db.Pool) *Store { return &Store{pool: pool} }

// ---------- groups ----------

func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, icon, rules, updated_at FROM preset_groups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanGroup)
}

func scanGroup(row pgx.CollectableRow) (Group, error) {
	var g Group
	var raw []byte
	if err := row.Scan(&g.ID, &g.Name, &g.Icon, &raw, &g.UpdatedAt); err != nil {
		return g, err
	}
	return g, json.Unmarshal(raw, &g.Rules)
}

// PutGroup creates the group or replaces it. Releases already made keep the
// group as it was; only the next release picks the change up.
func (s *Store) PutGroup(ctx context.Context, g Group) error {
	raw, err := json.Marshal(g.Rules)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO preset_groups (id, name, icon, rules) VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET name = $2, icon = $3, rules = $4, updated_at = now()`,
		g.ID, g.Name, g.Icon, raw)
	return err
}

// DeleteGroup refuses a group any preset's draft still holds: the next
// release of that preset would silently lose it.
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Drafts are edited under this lock too, so none can take the group
		// between the check and the delete.
		if _, err := tx.Exec(ctx, `LOCK TABLE presets IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM presets WHERE $1 = ANY(bypass) OR $1 = ANY(proxy) OR $1 = ANY(disabled))`, id).Scan(&used); err != nil {
			return err
		}
		if used {
			return ErrGroupInUse
		}
		tag, err := tx.Exec(ctx, `DELETE FROM preset_groups WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ---------- presets ----------

const presetColumns = `
	p.id, p.name, p.is_default, p.countries, p.bypass, p.proxy, p.disabled, p.stage, p.updated_at,
	c.id, c.v, c.services, c.created_by, c.created_at,
	n.id, n.v, n.services, n.created_by, n.created_at
	FROM presets p
	LEFT JOIN preset_releases c ON c.id = p.current_release
	LEFT JOIN preset_releases n ON n.id = p.next_release`

func scanPreset(row pgx.CollectableRow) (Preset, error) {
	var p Preset
	var cur, next releaseCols
	err := row.Scan(&p.ID, &p.Name, &p.Default, &p.Countries, &p.Bypass, &p.Proxy, &p.Disabled, &p.Stage, &p.UpdatedAt,
		&cur.id, &cur.v, &cur.services, &cur.by, &cur.at,
		&next.id, &next.v, &next.services, &next.by, &next.at)
	if err != nil {
		return p, err
	}
	if p.Current, err = cur.release(p.ID); err != nil {
		return p, err
	}
	p.Next, err = next.release(p.ID)
	return p, err
}

type releaseCols struct {
	id       *int64
	v        *int
	services []byte
	by       *string
	at       *time.Time
}

func (c releaseCols) release(preset string) (*Release, error) {
	if c.id == nil {
		return nil, nil
	}
	r := &Release{ID: *c.id, PresetID: preset, V: *c.v, CreatedBy: *c.by, CreatedAt: *c.at}
	return r, json.Unmarshal(c.services, &r.Services)
}

func (s *Store) Presets(ctx context.Context) ([]Preset, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+presetColumns+` ORDER BY p.is_default DESC, p.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanPreset)
}

func (s *Store) Preset(ctx context.Context, id string) (Preset, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+presetColumns+` WHERE p.id = $1`, id)
	if err != nil {
		return Preset{}, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, scanPreset)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// CreatePreset adds a preset with an empty draft. Making it the default takes
// the flag from the preset that had it.
func (s *Store) CreatePreset(ctx context.Context, id string, d PresetDraft) error {
	return s.writePreset(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO presets (id, name, is_default, countries, bypass, proxy, disabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, d.Name, d.Default, nonNil(d.Countries), nonNil(d.Bypass), nonNil(d.Proxy), nonNil(d.Disabled))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "presets_pkey" {
			return ErrExists
		}
		return err
	}, d)
}

// UpdatePreset replaces the draft and the assignment. The draft reaches no
// device until Release; a changed assignment moves devices at once.
func (s *Store) UpdatePreset(ctx context.Context, id string, d PresetDraft) error {
	return s.writePreset(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE presets SET name = $2, is_default = $3, countries = $4, bypass = $5, proxy = $6, disabled = $7, updated_at = now()
			WHERE id = $1`,
			id, d.Name, d.Default, nonNil(d.Countries), nonNil(d.Bypass), nonNil(d.Proxy), nonNil(d.Disabled))
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	}, d)
}

func (s *Store) writePreset(ctx context.Context, write func(pgx.Tx) error, d PresetDraft) error {
	if err := checkSides(d.Bypass, d.Proxy, d.Disabled); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serializes with DeleteGroup and with other writers of the default.
		if _, err := tx.Exec(ctx, `LOCK TABLE presets IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		var known int
		ids := append(slices.Clone(d.Bypass), append(slices.Clone(d.Proxy), d.Disabled...)...)
		if len(ids) > 0 {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM preset_groups WHERE id = ANY($1)`, ids).Scan(&known); err != nil {
				return err
			}
			if known != len(ids) {
				return ErrUnknownGroup
			}
		}
		if d.Default {
			if _, err := tx.Exec(ctx, `UPDATE presets SET is_default = false WHERE is_default`); err != nil {
				return err
			}
		}
		if err := write(tx); err != nil {
			return err
		}
		return bumpEpoch(ctx, tx)
	})
}

func checkSides(bypass, proxy, disabled []string) error {
	seen := map[string]bool{}
	for _, id := range append(slices.Clone(bypass), append(slices.Clone(proxy), disabled...)...) {
		if seen[id] {
			return ErrGroupTwice
		}
		seen[id] = true
	}
	return nil
}

func (s *Store) DeletePreset(ctx context.Context, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var isDefault bool
		err := tx.QueryRow(ctx, `SELECT is_default FROM presets WHERE id = $1 FOR UPDATE`, id).Scan(&isDefault)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if isDefault {
			return ErrDefault
		}
		// The preset points at its releases and they at it; clear its side
		// first so the cascade has nothing left pointing in.
		if _, err := tx.Exec(ctx, `UPDATE presets SET current_release = NULL, next_release = NULL WHERE id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM presets WHERE id = $1`, id); err != nil {
			return err
		}
		return bumpEpoch(ctx, tx)
	})
}

// ---------- rollout ----------

// Release freezes the preset's draft, with each group as it is now, into a
// new release that rolls out at the given stage. A release still rolling
// out is replaced: its devices fall back to current or get the new one. A
// preset never released before has no current, so buckets above the stage
// keep the client's bundled catalog.
func (s *Store) Release(ctx context.Context, id string, stage int, by string) (Release, error) {
	var r Release
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var bypass, proxy, disabled []string
		err := tx.QueryRow(ctx, `SELECT bypass, proxy, disabled FROM presets WHERE id = $1 FOR UPDATE`, id).Scan(&bypass, &proxy, &disabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if len(bypass)+len(proxy)+len(disabled) == 0 {
			return ErrNothingToShip
		}
		services, err := freeze(ctx, tx, bypass, proxy, disabled)
		if err != nil {
			return err
		}
		// Groups are checked when saved, but a release is the last gate
		// before devices: a group saved before a check existed, or the
		// catalog's size, is caught here.
		if err := ValidateCatalog(services); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		raw, err := json.Marshal(services)
		if err != nil {
			return err
		}
		r = Release{PresetID: id, Services: services, CreatedBy: by}
		if err := tx.QueryRow(ctx, `
			INSERT INTO preset_releases (preset_id, v, services, created_by)
			VALUES ($1, (SELECT coalesce(max(v), 0) + 1 FROM preset_releases WHERE preset_id = $1), $2, $3)
			RETURNING id, v, created_at`, id, raw, by).Scan(&r.ID, &r.V, &r.CreatedAt); err != nil {
			return err
		}
		if err := setRollout(ctx, tx, id, r.ID, stage); err != nil {
			return err
		}
		return bumpEpoch(ctx, tx)
	})
	return r, err
}

// freeze copies the groups into catalog services: bypass first, then proxy, then disabled,
// each side in draft order. Disabled groups have empty DefaultModes so they are off for all users by default.
func freeze(ctx context.Context, tx pgx.Tx, bypass, proxy, disabled []string) ([]Service, error) {
	allIDs := append(slices.Clone(bypass), append(slices.Clone(proxy), disabled...)...)
	rows, err := tx.Query(ctx, `SELECT id, name, icon, rules, updated_at FROM preset_groups WHERE id = ANY($1)`, allIDs)
	if err != nil {
		return nil, err
	}
	groups, err := pgx.CollectRows(rows, scanGroup)
	if err != nil {
		return nil, err
	}
	byID := map[string]Group{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	var out []Service
	sides := []struct {
		modes []string
		ids   []string
	}{
		{[]string{SideBypass}, bypass},
		{[]string{SideProxy}, proxy},
		{[]string{}, disabled},
	}
	for _, side := range sides {
		for _, id := range side.ids {
			g, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("%w %q", ErrUnknownGroup, id)
			}
			out = append(out, Service{
				ID: g.ID, Name: g.Name, Icon: g.Icon,
				Domains: nonNil(g.Rules.Domains), IPs: nonNil(g.Rules.IPs),
				Processes: nonNilMap(g.Rules.Processes), Folders: g.Rules.Folders,
				DefaultModes: nonNil(side.modes),
			})
		}
	}
	return out, nil
}

// SetStage moves the rollout to any stage, back or forward; 100 makes the
// release current for everyone.
func (s *Store) SetStage(ctx context.Context, id string, stage int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var next *int64
		err := tx.QueryRow(ctx, `SELECT next_release FROM presets WHERE id = $1 FOR UPDATE`, id).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if next == nil {
			return ErrNoNext
		}
		if err := setRollout(ctx, tx, id, *next, stage); err != nil {
			return err
		}
		return bumpEpoch(ctx, tx)
	})
}

// Revert drops the release rolling out: its devices go back to current at a
// higher version, which the client accepts.
func (s *Store) Revert(ctx context.Context, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE presets SET next_release = NULL, stage = 0, updated_at = now() WHERE id = $1 AND next_release IS NOT NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := s.presetExists(ctx, tx, id); err != nil {
				return err
			}
			return ErrNoNext
		}
		return bumpEpoch(ctx, tx)
	})
}

func (s *Store) presetExists(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM presets WHERE id = $1)`, id).Scan(&ok); err != nil {
		return false, err
	}
	if !ok {
		return false, ErrNotFound
	}
	return true, nil
}

func setRollout(ctx context.Context, tx pgx.Tx, id string, release int64, stage int) error {
	if stage < 0 || stage > 100 {
		return fmt.Errorf("stage %d is not a percent", stage)
	}
	q := `UPDATE presets SET next_release = $2, stage = $3, updated_at = now() WHERE id = $1`
	if stage == 100 {
		q = `UPDATE presets SET current_release = $2, next_release = NULL, stage = 0, updated_at = now() WHERE id = $1`
		_, err := tx.Exec(ctx, q, id, release)
		return err
	}
	_, err := tx.Exec(ctx, q, id, release, stage)
	return err
}

func bumpEpoch(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE preset_epoch SET value = value + 1`)
	return err
}

// ---------- serving ----------

// Served is the catalog one device gets.
type Served struct {
	Version  int64
	Release  int64
	Services []Service
}

// Serve picks the preset claiming the country, else the default, and its
// next release for buckets below the stage, else current. ok is false when
// there is nothing released for the device: it keeps its bundled catalog.
func (s *Store) Serve(ctx context.Context, country string, bucket int) (Served, bool, error) {
	var sv Served
	var raw []byte
	err := s.pool.QueryRow(ctx, `
		WITH e AS (SELECT value FROM preset_epoch),
		p AS (
			SELECT * FROM presets
			WHERE $1 = ANY(countries) OR is_default
			ORDER BY ($1 = ANY(countries)) DESC
			LIMIT 1
		)
		SELECT e.value, r.id, r.services
		FROM e, p JOIN preset_releases r
		  ON r.id = CASE WHEN p.next_release IS NOT NULL AND $2 < p.stage THEN p.next_release ELSE p.current_release END`,
		country, bucket).Scan(&sv.Version, &sv.Release, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return sv, false, nil
	}
	if err != nil {
		return sv, false, err
	}
	return sv, true, json.Unmarshal(raw, &sv.Services)
}

// Epoch is the current catalog version, for change events.
func (s *Store) Epoch(ctx context.Context) (int64, error) {
	var v int64
	err := s.pool.QueryRow(ctx, `SELECT value FROM preset_epoch`).Scan(&v)
	return v, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilMap(m map[string][]string) map[string][]string {
	if m == nil {
		return map[string][]string{}
	}
	return m
}
