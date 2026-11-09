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
	"net/url"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// cascade is where an account's links dial instead of the exit relay.
type cascade struct {
	address string
	port    int
	// relays are the exit's Hikkari relays the trunk reaches, by name.
	relays map[string]bool
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
	var exitBox, entryBox string
	var port int
	err := s.pool.QueryRow(ctx,
		`SELECT exit_box, entry_box, entry_port FROM entry_cascades WHERE account_id = $1`, account).
		Scan(&exitBox, &entryBox, &port)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("subscription: cascade: %w", err)
	}
	via, ok := s.batch.(CascadeVia)
	if !ok {
		return nil, ErrNoCascadeSource
	}
	address, relays, err := via.CascadeVia(ctx, entryBox, exitBox)
	if err != nil {
		return nil, fmt.Errorf("subscription: cascade %s → %s: %w", entryBox, exitBox, err)
	}
	return &cascade{address: address, port: port, relays: relays}, nil
}

// link renders rec, already carrying the account's key, as a link through
// the entry; false for a record the cascade does not carry.
func (c *cascade) link(rec entry.RelayRecord, dup bool) (string, bool) {
	if rec.Proto != entry.ProtoHikkari || !c.relays[rec.Name] {
		return "", false
	}
	name := rec.Name
	rec.Address, rec.Port = c.address, c.port
	link, err := HikkariLink(rec, dup)
	if err != nil {
		return "", false
	}
	u, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	u.Fragment = "Hikkari " + name + " через вход"
	return u.String(), true
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
		ON CONFLICT (account_id) DO UPDATE SET exit_box = $2, entry_box = $3, entry_port = $4, created_at = now()`,
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
