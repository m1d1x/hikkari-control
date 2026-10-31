package subscription

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// Batcher is the entry pool as a subscription sees it: an account's batch of
// signed relay records.
type Batcher interface {
	BundleFor(ctx context.Context, account uuid.UUID, meta entry.GrantMeta, haveVersion string) (entry.Bundle, error)
}

// Service issues subscriptions and serves them. A subscription is a random
// token tied to one account of the entry pool; only the token's SHA-256 is
// stored, so the table gives no working link away.
type Service struct {
	pool  db.Pool
	batch Batcher
	keys  []entry.Key
	now   func() time.Time
}

func New(pool db.Pool, batch Batcher, keys []entry.Key) *Service {
	return &Service{pool: pool, batch: batch, keys: keys, now: time.Now}
}

var nameRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ErrUnknown: no subscription under that token.
var ErrUnknown = errors.New("subscription: unknown token")

// Add creates an account for name and a subscription for it on plan's first
// period, and returns the token — shown once, never stored.
func (s *Service) Add(ctx context.Context, name, plan string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("subscription: name %q: want [a-z0-9_-], 1..32", name)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	p, err := s.plan(ctx, tx, plan)
	if err != nil {
		return "", err
	}
	// The entry pool grants relays to accounts (entry_grants.account_id →
	// hub_accounts.id), independent of administrator credentials.
	var account uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO hub_accounts (name) VALUES ($1) RETURNING id`, name).Scan(&account); err != nil {
		return "", fmt.Errorf("subscription: account %q: %w", name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO hub_subscriptions (token_hash, account_id) VALUES ($1, $2)`, tokenHash(token), account); err != nil {
		return "", err
	}
	if err := insertAccessKey(ctx, tx, account); err != nil {
		return "", err
	}
	if err := s.startPeriod(ctx, tx, account, p); err != nil {
		return "", err
	}
	return token, tx.Commit(ctx)
}

// Revoke ends name's subscription. The account and its grants stay, so the
// pool's bookkeeping is untouched; the link stops working at once.
func (s *Service) Revoke(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE hub_subscriptions SET revoked_at = now()
		WHERE revoked_at IS NULL AND account_id = (SELECT id FROM hub_accounts WHERE name = $1)`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnknown
	}
	return nil
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (s *Service) account(ctx context.Context, token string) (uuid.UUID, error) {
	var account uuid.UUID
	err := s.pool.QueryRow(ctx,
		`SELECT account_id FROM hub_subscriptions WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash(token)).Scan(&account)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrUnknown
	}
	return account, err
}

// Links is the subscriber's batch as client links, in the batch's order.
// The default format stays compatible with ordinary VPN clients.
func (s *Service) Links(ctx context.Context, token string) ([]string, error) {
	return s.LinksFor(ctx, token, "")
}

// LinksFor opts into the full Hysteria2 contract only for an explicit Xray
// subscription. A sing-box client must not import a link while dropping pins.
func (s *Service) LinksFor(ctx context.Context, token, engine string) ([]string, error) {
	account, err := s.account(ctx, token)
	if err != nil {
		return nil, err
	}
	return s.links(ctx, account, engine)
}

func (s *Service) links(ctx context.Context, account uuid.UUID, engine string) ([]string, error) {
	key, err := s.accessKey(ctx, account)
	if err != nil {
		return nil, err
	}
	b, err := s.batch.BundleFor(ctx, account, entry.GrantMeta{}, "")
	if err != nil {
		return nil, err
	}
	nodes := map[string]string{}
	if registry, ok := s.batch.(interface {
		NodeSlugs(context.Context) (map[string]string, error)
	}); ok {
		nodes, err = registry.NodeSlugs(ctx)
		if err != nil {
			return nil, err
		}
	}
	now := s.now()
	links := make([]string, 0, len(b.Relays))
	for _, raw := range b.Relays {
		rec, err := entry.OpenRecord(raw, s.keys, now)
		if err != nil {
			// A record the hub cannot open is the hub's own fault, not the
			// subscriber's: logged, and the rest of the batch still served.
			applog.Error(ctx, "subscription", "relay record not opened", applog.F("err", err.Error()))
			continue
		}
		if rec.Proto == entry.ProtoHysteria2 && engine != "xray" {
			continue
		}
		link, err := Link(withKey(rec, key))
		if err == nil && nodes[rec.Name] != "" {
			u, _ := url.Parse(link)
			q := u.Query()
			q.Set("hikkari-node", "n:"+nodes[rec.Name])
			u.RawQuery = q.Encode()
			u.Fragment = "Hikkari " + nodes[rec.Name]
			link = u.String()
		}
		if err != nil {
			continue
		}
		links = append(links, link)
	}
	return links, nil
}

// Handler serves GET /sub/{token}: base64 of the links, one per line — the
// common subscription format. The token is the only credential; an unknown
// one gets the same 404 as a wrong path.
func (s *Service) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")
		if len(token) < 16 || len(token) > 64 {
			http.NotFound(w, r)
			return
		}
		engine := r.URL.Query().Get("engine")
		if engine != "" && engine != "xray" {
			http.Error(w, "unsupported subscription engine", http.StatusBadRequest)
			return
		}
		account, err := s.account(r.Context(), token)
		if errors.Is(err, ErrUnknown) {
			http.NotFound(w, r)
			return
		}
		var links []string
		var terms Terms
		if err == nil {
			terms, err = s.terms(r.Context(), account)
		}
		if err == nil {
			links, err = s.links(r.Context(), account, engine)
		}
		if err != nil {
			applog.Error(r.Context(), "subscription", "batch failed", applog.F("err", err.Error()))
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte("Hikkari")))
		w.Header().Set("Profile-Update-Interval", "1")
		// The links stay served after the period ends or the traffic runs
		// out: the relays no longer let the key through (keys.go), and the
		// client reads why from here.
		w.Header().Set("Subscription-Userinfo", terms.userinfo())
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))))
	}
}
