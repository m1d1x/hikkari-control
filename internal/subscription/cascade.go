package subscription

// cascade.go is one account dialling a Hikkari exit through the cascade's
// trunk entry in Russia (hikkari-client plans/2026-11-05-ru-cascade.md):
//
//	client ─TLS, exit's domain─► entry (RU) :port ═trunk═► exit's Hikkari relay
//
// The entry terminates only TCP (and carries UDP), so the client's TLS, SNI
// and key are the exit relay's own: the link is the relay's, with the entry's
// address and port. Such an account is served only these links, so what it
// measures is the cascade and nothing else, and it holds the whole fleet
// (all_relays), so the exit relay knows its key whichever relay stands there.
//
// The link carries no node (hikkari-node): a cascade's failures are the
// entry's or the trunk's as much as the relay's, and must not count against
// the node the relay serves directly.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// cascade is where an account's links dial instead of the exit relay.
type cascade struct {
	address string
	port    int
	// flag is the exit box's country as an emoji flag, "" when its
	// location names none: the link dials Russia, the traffic leaves there.
	flag string
	// relays are the exit's Hikkari relays the trunk reaches, by name.
	relays map[string]bool
	// more are the further entries (v1.07, 088_cascade_entries.sql), in the
	// link's order after this one.
	more []cascadeEntry
}

// cascadeEntry is a further entry of a cascade: where a link dials it, the
// port of the DE country there (0 for none), and the exit relays its trunk
// reaches.
type cascadeEntry struct {
	address      string
	port, dePort int
	relays       map[string]bool
}

// CascadeVia finds the entry's address and the exit relays a cascade carries
// to (entry.Service.CascadeVia).
type CascadeVia interface {
	CascadeVia(ctx context.Context, entryBox, exitBox string) (string, map[string]bool, error)
}

// ErrNoCascadeSource: the batcher cannot say where a cascade dials.
var ErrNoCascadeSource = errors.New("subscription: no cascade source")

// cascadeOf is account's cascade, or nil when it has none.
func (s *Service) cascadeOf(ctx context.Context, account uuid.UUID) (*cascade, error) {
	var exitBox, entryBox, exitLocation string
	var port int
	err := s.pool.QueryRow(ctx, `
		SELECT c.exit_box, c.entry_box, c.entry_port, coalesce(b.location, '')
		FROM entry_cascades c LEFT JOIN entry_boxes b ON b.name = c.exit_box
		WHERE c.account_id = $1`, account).
		Scan(&exitBox, &entryBox, &port, &exitLocation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("subscription: cascade: %w", err)
	}
	c, err := s.cascadeVia(ctx, exitBox, entryBox, port, exitLocation)
	if err != nil {
		return nil, err
	}
	if c.more, err = s.cascadeEntriesOf(ctx, account, exitBox); err != nil {
		return nil, err
	}
	return c, nil
}

// cascadeEntriesOf is account's further entries to exitBox, in order.
func (s *Service) cascadeEntriesOf(ctx context.Context, account uuid.UUID, exitBox string) ([]cascadeEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.entry_box, e.entry_port, coalesce(ce.de_port, 0)
		FROM entry_cascade_entries e
		LEFT JOIN entry_country_egress ce ON ce.entry_box = e.entry_box AND ce.nl_port = e.entry_port AND ce.applied_sha <> ''
		WHERE e.account_id = $1 ORDER BY e.position`, account)
	if err != nil {
		return nil, fmt.Errorf("subscription: cascade entries: %w", err)
	}
	type row struct {
		box          string
		port, dePort int
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.box, &x.port, &x.dePort)
	})
	if err != nil {
		return nil, fmt.Errorf("subscription: cascade entries: %w", err)
	}
	via, ok := s.batch.(CascadeVia)
	if !ok && len(list) > 0 {
		return nil, ErrNoCascadeSource
	}
	var more []cascadeEntry
	for _, x := range list {
		address, relays, err := via.CascadeVia(ctx, x.box, exitBox)
		if err != nil {
			// A further entry the fleet cannot say leaves the link: entry 0
			// still serves, as it did before the entry was added.
			applog.Error(ctx, "subscription", "cascade entry left out",
				applog.F("entry_box", x.box), applog.F("exit_box", exitBox), applog.F("err", err.Error()))
			continue
		}
		more = append(more, cascadeEntry{address: address, port: x.port, dePort: x.dePort, relays: relays})
	}
	return more, nil
}

// cascadeVia is the cascade from entryBox at port to exitBox's relays.
func (s *Service) cascadeVia(ctx context.Context, exitBox, entryBox string, port int, exitLocation string) (*cascade, error) {
	via, ok := s.batch.(CascadeVia)
	if !ok {
		return nil, ErrNoCascadeSource
	}
	address, relays, err := via.CascadeVia(ctx, entryBox, exitBox)
	if err != nil {
		return nil, fmt.Errorf("subscription: cascade %s → %s: %w", entryBox, exitBox, err)
	}
	return &cascade{address: address, port: port, relays: relays, flag: countryFlag(exitLocation)}, nil
}

// link renders rec, already carrying the account's key, as a link through
// the entry; false for a record the cascade does not carry. label is the
// exit relay's node label ("амстердам-001"), the name every link reads by;
// a relay with no node yet is named by itself. The node's id stays off the
// link (see above), only its name is taken, numbered by cascadeName: the same
// relay served directly reads the same label, and a tester's log names the
// profile it dialled only by this name.
//
// entries puts the further entries that reach rec's relay on the link
// (v1.07): only for a client that said it dials them, as an older one refuses
// a link with a parameter it does not know.
func (c *cascade) link(rec entry.RelayRecord, label string, dup, entries bool) (string, bool) {
	if rec.Proto != entry.ProtoHikkari || !c.relays[rec.Name] {
		return "", false
	}
	name := label
	if name == "" {
		name = rec.Name
	}
	rec.Address, rec.Port = c.address, c.port
	link, err := HikkariLink(rec, dup)
	if err != nil {
		return "", false
	}
	u, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	u.Fragment = strings.TrimSpace(c.flag + " " + CascadeName(name))
	if entries {
		if more := c.entries(rec.Name, false); more != "" {
			q := u.Query()
			q.Set("entries", more)
			u.RawQuery = q.Encode()
		}
	}
	return u.String(), true
}

// entries is entries= for relay's link: the further entries whose trunk
// reaches it, host:port by commas, each at its DE country's port when de;
// an entry with no DE country there is left out of a DE link.
func (c *cascade) entries(relay string, de bool) string {
	var list []string
	for _, e := range c.more {
		port := e.port
		if de {
			port = e.dePort
		}
		if !e.relays[relay] || port == 0 {
			continue
		}
		list = append(list, net.JoinHostPort(e.address, strconv.Itoa(port)))
	}
	return strings.Join(list, ",")
}

// CascadeName is a node's label as its cascade reads: the number's first
// digit says the way in, 0 straight and 1 through the cascade
// ("нидерланды-002" → "нидерланды-102"). A number that does not start with 0
// gets the 1 in front ("-123" → "-1123"); a label with no number is kept.
func CascadeName(label string) string {
	i := strings.LastIndexByte(label, '-')
	num := label[i+1:]
	if i < 0 || num == "" || strings.Trim(num, "0123456789") != "" {
		return label
	}
	if num[0] == '0' {
		return label[:i+1] + "1" + num[1:]
	}
	return label[:i+1] + "1" + num
}

// countryFlag is the emoji flag of a box row's location ("Amsterdam, NL"),
// whose last part is an ISO 3166-1 alpha-2 code when the hoster names the
// country; "" otherwise. The client shows the flag instead of geolocating
// the address it dials, and older clients drop it from the name.
func countryFlag(location string) string {
	parts := strings.Split(location, ",")
	code := strings.ToUpper(strings.TrimSpace(parts[len(parts)-1]))
	if len(parts) < 2 || len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return string([]rune{0x1f1e6 + rune(code[0]-'A'), 0x1f1e6 + rune(code[1]-'A')})
}

// SetCascade makes the account name's subscription dial exitBox's Hikkari
// relays through the trunk entry on entryBox at port, and only them.
func (s *Service) SetCascade(ctx context.Context, name, exitBox, entryBox string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("subscription: cascade port %d", port)
	}
	via, ok := s.batch.(CascadeVia)
	if !ok {
		return ErrNoCascadeSource
	}
	if _, _, err := via.CascadeVia(ctx, entryBox, exitBox); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	var account uuid.UUID
	if err := tx.QueryRow(ctx, `UPDATE hub_accounts SET all_relays = true WHERE name = $1 RETURNING id`, name).Scan(&account); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w %q: no such account", ErrName, name)
		}
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO entry_cascades (account_id, exit_box, entry_box, entry_port) VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id) DO UPDATE SET exit_box = $2, entry_box = $3, entry_port = $4, created_at = now(),
		country_relay = CASE WHEN (entry_cascades.exit_box, entry_cascades.entry_box, entry_cascades.entry_port) = ($2, $3, $4)
		                     THEN entry_cascades.country_relay END`,
		account, exitBox, entryBox, port); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DropCascade returns the account name to the links of its relays. It keeps
// the whole fleet: SetCascade does not know what the account held before.
func (s *Service) DropCascade(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM entry_cascades c USING hub_accounts a
		WHERE a.id = c.account_id AND a.name = $1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w %q: no cascade", ErrName, name)
	}
	return nil
}

// SetCascadeEntry adds the trunk entry on entryBox at port as a further entry
// of the account name's cascade (v1.07): its links name it in entries=, after
// those added before, for a client that dials several entries. The entry's
// trunk must reach the cascade's exit. It returns the entry's number in the
// link (entry 0 is the cascade's own); adding one already there is a no-op.
func (s *Service) SetCascadeEntry(ctx context.Context, name, entryBox string, port int) (int, error) {
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("subscription: cascade entry port %d", port)
	}
	var account uuid.UUID
	var exitBox, firstBox string
	var firstPort int
	err := s.pool.QueryRow(ctx, `
		SELECT c.account_id, c.exit_box, c.entry_box, c.entry_port
		FROM entry_cascades c JOIN hub_accounts a ON a.id = c.account_id WHERE a.name = $1`, name).
		Scan(&account, &exitBox, &firstBox, &firstPort)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w %q: no cascade", ErrName, name)
	}
	if err != nil {
		return 0, err
	}
	if firstBox == entryBox && firstPort == port {
		return 0, fmt.Errorf("subscription: %s:%d is the cascade's entry 0", entryBox, port)
	}
	via, ok := s.batch.(CascadeVia)
	if !ok {
		return 0, ErrNoCascadeSource
	}
	if _, _, err := via.CascadeVia(ctx, entryBox, exitBox); err != nil {
		return 0, err
	}
	var position int
	err = s.pool.QueryRow(ctx, `
		WITH added AS (
			INSERT INTO entry_cascade_entries (account_id, position, entry_box, entry_port)
			SELECT $1, coalesce(max(position), 0) + 1, $2, $3 FROM entry_cascade_entries WHERE account_id = $1
			ON CONFLICT (account_id, entry_box, entry_port) DO NOTHING
			RETURNING position)
		SELECT position FROM added
		UNION ALL
		SELECT position FROM entry_cascade_entries WHERE account_id = $1 AND entry_box = $2 AND entry_port = $3
		LIMIT 1`, account, entryBox, port).Scan(&position)
	return position, err
}

// DropCascadeEntry takes entryBox's entries off the account name's cascade;
// the rest keep their numbers' order.
func (s *Service) DropCascadeEntry(ctx context.Context, name, entryBox string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM entry_cascade_entries e USING hub_accounts a
		WHERE a.id = e.account_id AND a.name = $1 AND e.entry_box = $2`, name, entryBox)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w %q: no cascade entry on %s", ErrName, name, entryBox)
	}
	return nil
}
