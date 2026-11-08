package subscription

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/capacity"
	"github.com/m1d1x/hikkari-control/internal/config"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
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
type CapacitySource interface {
	Metrics(context.Context, []string) ([]capacity.Metric, error)
}

type Service struct {
	Capacity CapacitySource
	pool     db.Pool
	batch    Batcher
	keys     []entry.Key
	now      func() time.Time
}

func New(pool db.Pool, batch Batcher, keys []entry.Key) *Service {
	return &Service{pool: pool, batch: batch, keys: keys, now: time.Now}
}

var nameRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ErrUnknown: no subscription under that token.
var ErrUnknown = errors.New("subscription: unknown token")

// ErrName: an account name that is malformed or already taken.
var ErrName = errors.New("subscription: account name")

// Add creates an account for name and a subscription for it on plan's first
// period, and returns the token — shown once, never stored in the clear.
func (s *Service) Add(ctx context.Context, name, plan string) (string, error) {
	return s.add(ctx, name, plan, false)
}

// AddTester is Add for a tester: the account holds every alive relay of the
// fleet, not a batch (hub_accounts.all_relays). Issued by hand only.
func (s *Service) AddTester(ctx context.Context, name, plan string) (string, error) {
	return s.add(ctx, name, plan, true)
}

func (s *Service) add(ctx context.Context, name, plan string, allRelays bool) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := s.plan(ctx, tx, plan, true); err != nil {
		return "", err
	}
	token, account, err := s.AddIn(ctx, tx, name, plan, "")
	if err != nil {
		return "", err
	}
	if allRelays {
		if _, err := tx.Exec(ctx, `UPDATE hub_accounts SET all_relays = true WHERE id = $1`, account); err != nil {
			return "", err
		}
	}
	return token, tx.Commit(ctx)
}

// AddIn is Add inside the caller's transaction, so a payment and the
// subscription it buys commit together (internal/billing): plan may have been
// retired since it was bought. email is the buyer's, "" for none.
func (s *Service) AddIn(ctx context.Context, tx pgx.Tx, name, plan, email string) (string, uuid.UUID, error) {
	if !nameRe.MatchString(name) {
		return "", uuid.Nil, fmt.Errorf("%w %q: want [a-z0-9_-], 1..32", ErrName, name)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", uuid.Nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sealed, err := cryptobox.EncryptString(token)
	if err != nil {
		return "", uuid.Nil, fmt.Errorf("subscription: seal token: %w", err)
	}
	p, err := s.plan(ctx, tx, plan, false)
	if err != nil {
		return "", uuid.Nil, err
	}
	// The entry pool grants relays to accounts (entry_grants.account_id →
	// hub_accounts.id), independent of administrator credentials.
	var account uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO hub_accounts (name, email) VALUES ($1, nullif($2, '')) RETURNING id`, name, email).Scan(&account); err != nil {
		if pg := (*pgconn.PgError)(nil); errors.As(err, &pg) && pg.Code == "23505" {
			return "", uuid.Nil, fmt.Errorf("%w %q: taken", ErrName, name)
		}
		return "", uuid.Nil, fmt.Errorf("subscription: account %q: %w", name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO hub_subscriptions (token_hash, token_sealed, account_id) VALUES ($1, $2, $3)`,
		tokenHash(token), sealed, account); err != nil {
		return "", uuid.Nil, err
	}
	if err := insertAccessKey(ctx, tx, account); err != nil {
		return "", uuid.Nil, err
	}
	if err := s.startPeriod(ctx, tx, account, p); err != nil {
		return "", uuid.Nil, err
	}
	return token, account, nil
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

// Account is the account a live-or-ended, unrevoked subscription token
// belongs to.
func (s *Service) Account(ctx context.Context, token string) (uuid.UUID, error) {
	return s.account(ctx, token)
}

// URL is the account's subscription link, read back from the sealed token.
// ErrUnknown for a revoked subscription or one issued before tokens were
// sealed: its link exists only where it was handed out.
func (s *Service) URL(ctx context.Context, account uuid.UUID) (string, error) {
	var sealed []byte
	err := s.pool.QueryRow(ctx, `
		SELECT token_sealed FROM hub_subscriptions
		WHERE account_id = $1 AND revoked_at IS NULL AND token_sealed IS NOT NULL`, account).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnknown
	}
	if err != nil {
		return "", err
	}
	token, err := cryptobox.DecryptString(sealed)
	if err != nil {
		return "", fmt.Errorf("subscription: open token: %w", err)
	}
	return LinkURL(token), nil
}

// RenewURL is the page that sells token's subscription its next period.
func RenewURL(token string) string {
	return config.Default().BaseURL() + "/pay#renew=" + token
}

// LinkURL is the subscription link of token.
func LinkURL(token string) string {
	return config.Default().SubURL() + "/sub/" + token
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

// LinksFor opts into the full Hysteria2 contract (finalmask, certificate pin)
// only for an explicit Xray subscription; every other client gets the
// salamander link pinned by public key (SingBoxHysteriaLink), never one with
// its pin dropped.
func (s *Service) LinksFor(ctx context.Context, token, engine string) ([]string, error) {
	account, err := s.account(ctx, token)
	if err != nil {
		return nil, err
	}
	return s.links(ctx, account, engine, awgCaps{}, 0, false, false)
}

// featuresHeader is how a client says what it can dial beyond what every client
// can. AmneziaWG needs it: the clients released before it write a config their
// sing-box refuses as a whole, so one such link would take every other server
// down with it. A client that does not say gets the rest.
const featuresHeader = "Hikkari-Features"

const featureAmneziaWG = "amneziawg"

// featureAmneziaWG31 is a client that carries AmneziaWG 3.1's parameters. One
// that only says featureAmneziaWG refuses a 3.1 config (hikkari-client 1.0.52)
// or, older, drops the parameters and gets no answer; it gets the 2.0 relays.
const featureAmneziaWG31 = "amneziawg-3.1"

// awgCaps is which AmneziaWG relays a client can dial.
type awgCaps struct{ v20, v31 bool }

// dials reports whether a client with these caps can dial rec.
func (c awgCaps) dials(rec entry.RelayRecord) bool {
	if rec.AwgParams != nil && rec.AwgParams.AwgV3.IsSet() {
		return c.v31
	}
	return c.v20
}

// wantsAwg reads featuresHeader, a comma-separated list. 3.1 is said on top of
// AmneziaWG, never instead of it.
func wantsAwg(h http.Header) awgCaps {
	var c awgCaps
	for _, f := range strings.Split(h.Get(featuresHeader), ",") {
		switch strings.TrimSpace(f) {
		case featureAmneziaWG:
			c.v20 = true
		case featureAmneziaWG31:
			c.v31 = true
		}
	}
	c.v31 = c.v31 && c.v20
	return c
}

// networkHeader is the subscriber's network as the client resolves it from
// the database embedded in it (hikkari-client engine/core/netinfo): «AS12389».
// The hub never sees the subscriber's address, so this is the only way it
// learns the network; a node's knobs for that network then stand in for its
// own (entry_node_network_knobs). Absent or malformed is no known network.
const networkHeader = "Hikkari-Network"

// networkOf reads networkHeader; 0 is no known network.
func networkOf(h http.Header) uint32 {
	v, ok := strings.CutPrefix(strings.TrimSpace(h.Get(networkHeader)), "AS")
	if !ok || v == "" || v[0] == '0' {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// hikkari: the client's core speaks Hikkari v1.01 (feature "hikkari"); its
// Naive relays that carry the datagram channel get hikkari+v1:// links.
func (s *Service) links(ctx context.Context, account uuid.UUID, engine string, awg awgCaps, asn uint32, naive, hikkari bool) ([]string, error) {
	key, err := s.accessKey(ctx, account)
	if err != nil {
		return nil, err
	}
	b, err := s.batch.BundleFor(ctx, account, entry.GrantMeta{}, "")
	if err != nil {
		return nil, err
	}
	nodes := map[string]entry.NodeName{}
	if registry, ok := s.batch.(interface {
		NodeNamesFor(context.Context, uint32) (map[string]entry.NodeName, error)
	}); ok {
		nodes, err = registry.NodeNamesFor(ctx, asn)
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
		if rec.Proto == entry.ProtoNaive && (!naive || engine == "xray") {
			continue
		}
		render := Link
		if rec.Proto == entry.ProtoNaive && hikkari && rec.NaiveParams != nil && rec.NaiveParams.UDP {
			render = HikkariLink
		}
		if rec.Proto == entry.ProtoHysteria2 && engine != "xray" {
			render = SingBoxHysteriaLink
		}
		if rec.Proto == entry.ProtoAmneziaWG {
			if !awg.dials(rec) {
				continue
			}
			if err := s.ensureAwgKey(ctx, account, &key); err != nil {
				return nil, err
			}
			// Named like every other link: the node's name as its title, its
			// slug on the link for the downdetector and the capacity metrics.
			render = awgLinkFor(nodes[rec.Name])
		}
		link, err := render(withKey(rec, key))
		if err == nil {
			link = namedLink(link, nodes[rec.Name], coversOf(rec))
		}
		if err != nil {
			continue
		}
		links = append(links, link)
	}
	return links, nil
}

// coversOf is the Reality covers a relay serves; none for other protocols.
func coversOf(rec entry.RelayRecord) []string {
	if rec.Proto == entry.ProtoVlessRealityVision && rec.RealityParams != nil {
		return rec.RealityParams.Covers
	}
	return nil
}

// namedLink puts a relay's logical node on its link: the name a subscriber
// reads, the id the downdetector keys on and the node's knobs (fingerprint,
// SNI, ALPN), all surviving the relay's replacement. A relay with no node yet
// keeps the link it was rendered with. covers are the relay's Reality covers:
// the node's SNI is taken only when the relay serves it, since a name the
// relay does not accept would make the link dead rather than different.
func namedLink(link string, node entry.NodeName, covers []string) string {
	u, err := url.Parse(link)
	if err != nil || node.Slug == "" {
		return link
	}
	q := u.Query()
	q.Set("hikkari-node", "n:"+node.Slug)
	// Only a link that names a fingerprint takes the node's own: one without
	// (Hysteria2) has no TLS fingerprint to choose.
	if node.Fingerprint != "" && q.Get("fp") != "" {
		q.Set("fp", node.Fingerprint)
	}
	if node.SNI != "" && q.Get("security") == "reality" && slices.Contains(covers, node.SNI) {
		q.Set("sni", node.SNI)
	}
	// ALPN is the client's offer in its ClientHello; only TLS links have one.
	if node.ALPN != "" && u.Scheme == "vless" && (q.Get("security") == "reality" || q.Get("security") == "tls") {
		q.Set("alpn", node.ALPN)
	}
	u.RawQuery = q.Encode()
	if node.Label != "" {
		u.Fragment = node.Label
	}
	return u.String()
}

// Handler serves GET /sub/{token}: base64 of the links, one per line — the
// common subscription format. The token is the only credential; an unknown
// one gets the same 404 as a wrong path.
func (s *Service) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Add("Vary", "Accept, Hikkari-Features")
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
		if err == nil && wantsSubscriptionPage(r) {
			s.servePage(w, r, token)
			return
		}
		if err == nil {
			links, err = s.links(r.Context(), account, engine, wantsAwg(r.Header), networkOf(r.Header), hasNaiveFeature(r.Header), hasFeature(r.Header, "hikkari"))
		}
		if err != nil {
			applog.Error(r.Context(), "subscription", "batch failed", applog.F("err", err.Error()))
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		var relayMetrics []capacity.Metric
		if s.Capacity != nil {
			keys := make([]string, 0, len(links))
			for _, link := range links {
				if key := capacity.Key(link); key != "" {
					keys = append(keys, key)
				}
			}
			metricContext, cancelMetrics := context.WithTimeout(r.Context(), 250*time.Millisecond)
			metrics, capacityErr := s.Capacity.Metrics(metricContext, keys)
			cancelMetrics()
			if capacityErr == nil {
				relayMetrics = metrics
				w.Header().Set(capacity.Header, capacity.Encode(metrics))
			} else {
				applog.Error(r.Context(), "capacity", "subscription summary unavailable")
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte("hikkari")))
		w.Header().Set("Profile-Update-Interval", "1")
		// The links stay served after the period ends or the traffic runs
		// out: the relays no longer let the key through (keys.go), and the
		// client reads why from here.
		w.Header().Set("Subscription-Userinfo", terms.userinfo())
		// Where the subscriber renews: the landing's checkout, told which
		// subscription to extend in the fragment, which reaches no server.
		w.Header().Set("Profile-Web-Page-Url", RenewURL(token))
		if slices.Contains(strings.FieldsFunc(r.Header.Get("Hikkari-Features"), func(r rune) bool { return r == ',' || r == ' ' }), "relay-metrics-v2") {
			body, err := json.Marshal(struct {
				Version int               `json:"version"`
				Links   []string          `json:"links"`
				Metrics []capacity.Metric `json:"metrics"`
			}{2, links, relayMetrics})
			if err == nil {
				body, err = sealSubscription(token, body)
			}
			if err != nil {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			// No plaintext measurement header on the sealed response.
			w.Header().Del(capacity.Header)
			w.Header().Set("Content-Type", metricsContentType)
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n")))))
	}
}

func hasNaiveFeature(h http.Header) bool { return hasFeature(h, "naive") }

func hasFeature(h http.Header, want string) bool {
	for _, feature := range strings.Split(h.Get(featuresHeader), ",") {
		if strings.TrimSpace(feature) == want {
			return true
		}
	}
	return false
}
