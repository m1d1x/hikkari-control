package entry

import (
	"context"
	"crypto"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/rxcert"
)

// The receiver group's certificate (hikkari-client
// plans/2026-11-16-second-receiver.md, P1). Every Hikkari relay answering
// under one domain, on whatever machine, serves the same certificate, which
// the hub orders over DNS-01 at the registrar the domain was bought at and
// renders into the relay's env. A domain without one keeps Caddy's own order
// on the box.

// TXTRegistrar is a registrar that can put DNS records other than the apex A
// record: the TXT of a DNS-01 challenge. Porkbun's adapter is one.
type TXTRegistrar interface {
	CreateRecord(ctx context.Context, zone, name, typ, content string, ttl int) (string, error)
	DeleteRecord(ctx context.Context, zone, id string) error
}

// ErrNoTXTRegistrar: the registrar the hub has cannot write TXT records, so
// it cannot prove a domain over DNS-01.
var ErrNoTXTRegistrar = errors.New("entry: the registrar cannot write TXT records")

// receiverCertRenewBefore is how long before expiry a group's certificate is
// renewed: Let's Encrypt's 90 days leave two months of it, and a failed
// renewal a month of daily retries.
const receiverCertRenewBefore = 30 * 24 * time.Hour

// ReceiverCertInterval is how often the hub looks for certificates to renew.
const ReceiverCertInterval = 12 * time.Hour

// IssuedReceiverCert is what an order gave: the leaf's expiry, and whether it
// was kept for the receivers (a staging one never is).
type IssuedReceiverCert struct {
	Domain    string
	Directory string
	NotAfter  time.Time
	Kept      bool
}

// IssueReceiverCert orders the certificate of domain's receiver group. Only a
// domain the hub bought (entry_domains, source purchased) is proved: its
// zone is at the registrar the hub holds the key of. With staging the order
// goes to Let's Encrypt's staging directory and the certificate is thrown
// away: nobody trusts it, so no receiver may serve it.
func (s *Service) IssueReceiverCert(ctx context.Context, domain string, staging bool) (IssuedReceiverCert, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	zone, err := s.receiverZone(ctx, domain)
	if err != nil {
		return IssuedReceiverCert{}, err
	}
	b := s.currentBuyer()
	if b == nil || b.reg == nil {
		return IssuedReceiverCert{}, ErrNoRegistrar
	}
	dns, ok := b.reg.(TXTRegistrar)
	if !ok {
		return IssuedReceiverCert{}, ErrNoTXTRegistrar
	}
	// A staging order is a rehearsal and writes nothing: a throwaway
	// account, a certificate thrown away.
	directory := rxcert.LetsEncrypt
	var key crypto.Signer
	if staging {
		directory = rxcert.LetsEncryptStaging
		key, _, err = rxcert.NewAccountKey()
	} else {
		key, err = s.acmeAccountKey(ctx, directory)
	}
	if err != nil {
		return IssuedReceiverCert{}, err
	}
	is := &rxcert.Issuer{Directory: directory, DNS: dns, AccountKey: key, HTTPClient: &http.Client{Timeout: time.Minute}}
	cert, err := is.Issue(ctx, zone, domain)
	if err != nil {
		return IssuedReceiverCert{}, err
	}
	out := IssuedReceiverCert{Domain: domain, Directory: directory, NotAfter: cert.NotAfter}
	if staging {
		return out, nil
	}
	keyCT, err := cryptobox.Encrypt(cert.KeyPEM)
	if err != nil {
		return IssuedReceiverCert{}, err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO entry_receiver_certs (domain, directory, chain_pem, key_ct, not_after)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (domain) DO UPDATE SET directory = $2, chain_pem = $3, key_ct = $4, not_after = $5, issued_at = now()`,
		domain, directory, string(cert.ChainPEM), keyCT, cert.NotAfter); err != nil {
		return IssuedReceiverCert{}, err
	}
	out.Kept = true
	return out, nil
}

// receiverZone is the zone domain is proved in: a domain the hub bought, or
// a name under one.
func (s *Service) receiverZone(ctx context.Context, domain string) (string, error) {
	if err := checkRelayDomain(domain); err != nil {
		return "", err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT domain FROM entry_domains
		WHERE source = 'purchased' AND status IN ('owned', 'assigned')`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var zone string
		if err := rows.Scan(&zone); err != nil {
			return "", err
		}
		if domain == zone || strings.HasSuffix(domain, "."+zone) {
			return zone, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%w: %s is not a domain the hub bought", ErrRejected, domain)
}

// acmeAccountKey is the hub's account key at directory, made and kept on
// first use.
func (s *Service) acmeAccountKey(ctx context.Context, directory string) (crypto.Signer, error) {
	var ct []byte
	err := s.pool.QueryRow(ctx, `SELECT key_ct FROM entry_acme_accounts WHERE directory = $1`, directory).Scan(&ct)
	if err == nil {
		plain, err := cryptobox.Decrypt(ct)
		if err != nil {
			return nil, err
		}
		return rxcert.ParseKey(plain)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	_, keyPEM, err := rxcert.NewAccountKey()
	if err != nil {
		return nil, err
	}
	if ct, err = cryptobox.Encrypt(keyPEM); err != nil {
		return nil, err
	}
	// Two first uses at once: the one that lost reads the winner's key.
	if _, err := s.pool.Exec(ctx, `INSERT INTO entry_acme_accounts (directory, key_ct) VALUES ($1, $2) ON CONFLICT (directory) DO NOTHING`, directory, ct); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `SELECT key_ct FROM entry_acme_accounts WHERE directory = $1`, directory).Scan(&ct); err != nil {
		return nil, err
	}
	plain, err := cryptobox.Decrypt(ct)
	if err != nil {
		return nil, err
	}
	return rxcert.ParseKey(plain)
}

// receiverCertEnv is the env a Hikkari relay on domain serves the group's
// certificate with; nil when the hub holds none for it.
func (s *Service) receiverCertEnv(ctx context.Context, domain string) (map[string]string, error) {
	var chain string
	var keyCT []byte
	var notAfter time.Time
	err := s.pool.QueryRow(ctx, `SELECT chain_pem, key_ct, not_after FROM entry_receiver_certs WHERE domain = $1`,
		strings.ToLower(domain)).Scan(&chain, &keyCT, &notAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// An expired one is worse than Caddy's own order: no client accepts it.
	if !s.clock().Before(notAfter) {
		return nil, nil
	}
	key, err := cryptobox.Decrypt(keyCT)
	if err != nil {
		return nil, err
	}
	b64 := base64.StdEncoding.EncodeToString
	return map[string]string{"EDGE_HIKKARI_CERT": b64([]byte(chain)), "EDGE_HIKKARI_KEY": b64(key)}, nil
}

// ReceiverCert is a kept certificate, for the owner's command.
type ReceiverCert struct {
	Domain    string
	Directory string
	NotAfter  time.Time
	IssuedAt  time.Time
}

// ReceiverCerts lists the kept certificates.
func (s *Service) ReceiverCerts(ctx context.Context) ([]ReceiverCert, error) {
	rows, err := s.pool.Query(ctx, `SELECT domain, directory, not_after, issued_at FROM entry_receiver_certs ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReceiverCert
	for rows.Next() {
		var c ReceiverCert
		if err := rows.Scan(&c.Domain, &c.Directory, &c.NotAfter, &c.IssuedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReceiverBoxes are the live boxes with a Hikkari relay on domain: where a
// new certificate of the group goes.
func (s *Service) ReceiverBoxes(ctx context.Context, domain string) ([]string, error) {
	boxes, err := s.receiverRelayBoxes(ctx, domain)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.core_box FROM entry_trunk_point_tls_names p JOIN entry_boxes b ON b.name=p.core_box
		WHERE p.domain=$1 AND b.retired_at IS NULL ORDER BY p.core_box`, strings.ToLower(domain))
	if err != nil {
		return nil, err
	}
	cores, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, core := range cores {
		if !slices.Contains(boxes, core) {
			boxes = append(boxes, core)
		}
	}
	return boxes, nil
}

func (s *Service) receiverRelayBoxes(ctx context.Context, domain string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT name FROM entry_boxes WHERE proto = $1 AND retired_at IS NULL ORDER BY name`, ProtoHikkari)
	if err != nil {
		return nil, err
	}
	var boxes []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		boxes = append(boxes, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	domain = strings.ToLower(domain)
	var out []string
	for _, b := range boxes {
		relays, err := s.relayIdentities(ctx, b)
		if err != nil {
			return nil, err
		}
		for _, r := range relays {
			if h := r.id.Hikkari; h != nil && strings.EqualFold(h.Host, domain) {
				out = append(out, b)
				break
			}
		}
	}
	return out, nil
}

// RunReceiverCertRenewal renews every kept certificate a month before it
// expires and moves each box of its group onto the new one (reconfigure:
// the relay's new generation takes the env, the old one drains, hitless).
// A failure is logged and retried next round.
func (s *Service) RunReceiverCertRenewal(ctx context.Context, interval time.Duration, reconfigure func(ctx context.Context, box string) error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.renewReceiverCerts(ctx, reconfigure)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) renewReceiverCerts(ctx context.Context, reconfigure func(ctx context.Context, box string) error) {
	certs, err := s.ReceiverCerts(ctx)
	if err != nil {
		log.Printf("entry: receiver certificates: %v", err)
		return
	}
	for _, c := range certs {
		if c.NotAfter.Sub(s.clock()) > receiverCertRenewBefore {
			continue
		}
		issued, err := s.IssueReceiverCert(ctx, c.Domain, false)
		if err != nil {
			log.Printf("entry: renew the receiver certificate of %s (expires %s): %v", c.Domain, c.NotAfter.Format(time.DateOnly), err)
			continue
		}
		log.Printf("entry: receiver certificate of %s renewed, valid to %s", c.Domain, issued.NotAfter.Format(time.DateOnly))
		boxes, err := s.receiverRelayBoxes(ctx, c.Domain)
		if err != nil {
			log.Printf("entry: receivers of %s: %v", c.Domain, err)
			continue
		}
		for _, b := range boxes {
			if err := reconfigure(ctx, b); err != nil {
				log.Printf("entry: %s onto the renewed certificate of %s: %v", b, c.Domain, err)
			}
		}
	}
	s.reconfigurePointCerts(ctx, reconfigure)
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
