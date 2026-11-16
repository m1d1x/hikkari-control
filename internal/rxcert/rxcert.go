// Package rxcert issues the certificate of a group of receivers: the relays
// that answer a client's TLS under one domain from different machines
// (hikkari-client plans/2026-11-16-second-receiver.md, P1).
//
// The client's TLS goes through the entry untouched (3.2), so a spare
// receiver must be the same server to the client: the same name, a valid
// certificate. Caddy on a receiver ordered its own over HTTP-01, which works
// only where the domain's A record points. The hub orders it instead over
// DNS-01 (a TXT record at the registrar, Porkbun) and hands it to every
// receiver of the group. DNS-01 needs no A record at all, which the hidden
// receiver of step 3 needs too.
package rxcert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mholt/acmez/v3"
	"github.com/mholt/acmez/v3/acme"
)

// The ACME directories of Let's Encrypt. Staging issues certificates nobody
// trusts, under far looser rate limits: the first run of anything new goes
// there.
const (
	LetsEncrypt        = "https://acme-v02.api.letsencrypt.org/directory"
	LetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// DNS is the registrar's record API for the zone the domain lives in.
type DNS interface {
	CreateRecord(ctx context.Context, zone, name, typ, content string, ttl int) (string, error)
	DeleteRecord(ctx context.Context, zone, id string) error
}

// Issuer orders certificates over DNS-01.
type Issuer struct {
	Directory  string
	DNS        DNS
	AccountKey crypto.Signer
	// LookupTXT reads name's TXT records the way the CA will; nil asks every
	// authoritative server of the zone (Authoritative).
	LookupTXT func(ctx context.Context, zone, name string) ([][]string, error)
	// Propagation is how long the record may take to show; 0 is 10 minutes.
	Propagation time.Duration
	HTTPClient  *http.Client
	Logger      *slog.Logger
}

// Cert is an issued certificate: the chain end-entity first and its key, both
// PEM.
type Cert struct {
	ChainPEM, KeyPEM []byte
	NotAfter         time.Time
}

// Issue orders a certificate for domain, which lives in zone (the domain as
// the registrar holds it; the same as domain for an apex).
func (is *Issuer) Issue(ctx context.Context, zone, domain string) (Cert, error) {
	zone, domain = strings.ToLower(zone), strings.ToLower(domain)
	if domain != zone && !strings.HasSuffix(domain, "."+zone) {
		return Cert{}, fmt.Errorf("rxcert: %s is not in zone %s", domain, zone)
	}
	if is.AccountKey == nil || is.DNS == nil || is.Directory == "" {
		return Cert{}, errors.New("rxcert: issuer without directory, account key or DNS")
	}
	logger := is.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	solver := &dnsSolver{is: is, zone: zone, records: map[string]string{}}
	client := acmez.Client{
		Client: &acme.Client{
			Directory:  is.Directory,
			HTTPClient: is.HTTPClient,
			Logger:     logger,
		},
		ChallengeSolvers: map[string]acmez.Solver{acme.ChallengeTypeDNS01: solver},
	}
	// An account key the CA knows answers with that account: no lookup of
	// our own, no stored account URL.
	account, err := client.NewAccount(ctx, acme.Account{TermsOfServiceAgreed: true, PrivateKey: is.AccountKey})
	if err != nil {
		return Cert{}, fmt.Errorf("rxcert: account: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Cert{}, err
	}
	certs, err := client.ObtainCertificateForSANs(ctx, account, key, []string{domain})
	if err != nil {
		return Cert{}, fmt.Errorf("rxcert: order %s: %w", domain, err)
	}
	if len(certs) == 0 {
		return Cert{}, fmt.Errorf("rxcert: order %s: no certificate", domain)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return Cert{}, err
	}
	out := Cert{ChainPEM: certs[0].ChainPEM, KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})}
	leaf, err := Leaf(out.ChainPEM)
	if err != nil {
		return Cert{}, err
	}
	if err := leaf.VerifyHostname(domain); err != nil {
		return Cert{}, fmt.Errorf("rxcert: issued certificate: %w", err)
	}
	out.NotAfter = leaf.NotAfter
	return out, nil
}

// Leaf parses the end-entity certificate of a PEM chain.
func Leaf(chainPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(chainPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("rxcert: chain has no certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// NewAccountKey makes an ACME account key and its PEM.
func NewAccountKey() (crypto.Signer, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// ParseKey reads a key NewAccountKey or Issue wrote.
func ParseKey(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, errors.New("rxcert: not an EC private key")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// dnsSolver puts the challenge's TXT record at the registrar and waits until
// every authoritative server of the zone answers with it.
type dnsSolver struct {
	is   *Issuer
	zone string

	mu      sync.Mutex
	records map[string]string // challenge token → record id
}

func (s *dnsSolver) name(chal acme.Challenge) string {
	fqdn := strings.TrimSuffix(strings.ToLower(chal.DNS01TXTRecordName()), ".")
	return strings.TrimSuffix(strings.TrimSuffix(fqdn, s.zone), ".")
}

func (s *dnsSolver) Present(ctx context.Context, chal acme.Challenge) error {
	id, err := s.is.DNS.CreateRecord(ctx, s.zone, s.name(chal), "TXT", chal.DNS01KeyAuthorization(), 600)
	if err != nil {
		return fmt.Errorf("rxcert: TXT %s: %w", chal.DNS01TXTRecordName(), err)
	}
	s.mu.Lock()
	s.records[chal.Token] = id
	s.mu.Unlock()
	return nil
}

// Wait holds the challenge until the record shows: a CA that asks before it
// does fails the order, and Let's Encrypt counts failed validations.
func (s *dnsSolver) Wait(ctx context.Context, chal acme.Challenge) error {
	limit := s.is.Propagation
	if limit <= 0 {
		limit = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	lookup := s.is.LookupTXT
	if lookup == nil {
		lookup = Authoritative
	}
	fqdn := strings.TrimSuffix(chal.DNS01TXTRecordName(), ".")
	want := chal.DNS01KeyAuthorization()
	var last error
	for {
		answers, err := lookup(ctx, s.zone, fqdn)
		if err == nil && allHave(answers, want) {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			if last == nil {
				last = errors.New("record not seen")
			}
			return fmt.Errorf("rxcert: TXT %s not on every authoritative server within %s: %w", fqdn, limit, last)
		case <-time.After(5 * time.Second):
		}
	}
}

func allHave(answers [][]string, want string) bool {
	if len(answers) == 0 {
		return false
	}
	for _, txts := range answers {
		found := false
		for _, t := range txts {
			if t == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *dnsSolver) CleanUp(ctx context.Context, chal acme.Challenge) error {
	s.mu.Lock()
	id, ok := s.records[chal.Token]
	delete(s.records, chal.Token)
	s.mu.Unlock()
	if !ok || id == "" {
		return nil
	}
	return s.is.DNS.DeleteRecord(ctx, s.zone, id)
}

// Authoritative asks every authoritative server of zone for name's TXT
// records, one answer per server: a record one server has and another has
// not yet is not there for the CA.
func Authoritative(ctx context.Context, zone, name string) ([][]string, error) {
	servers, err := net.DefaultResolver.LookupNS(ctx, zone)
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, fmt.Errorf("no NS for %s", zone)
	}
	var out [][]string
	for _, ns := range servers {
		host := strings.TrimSuffix(ns.Host, ".")
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(host, "53"))
		}}
		txts, err := r.LookupTXT(ctx, name)
		if err != nil {
			var dnsErr *net.DNSError
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				txts = nil
			} else {
				return nil, fmt.Errorf("%s: %w", host, err)
			}
		}
		out = append(out, txts)
	}
	return out, nil
}
