package rxcert

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mholt/acmez/v3/acme"
)

type fakeDNS struct {
	mu      sync.Mutex
	records map[string][2]string // id → name, content
	next    int
}

func (f *fakeDNS) CreateRecord(_ context.Context, zone, name, typ, content string, ttl int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if typ != "TXT" || ttl < 600 {
		panic("unexpected record")
	}
	f.next++
	id := strings.Repeat("1", f.next)
	f.records[id] = [2]string{name, content}
	return id, nil
}

func (f *fakeDNS) DeleteRecord(_ context.Context, zone, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, id)
	return nil
}

func (f *fakeDNS) txt(zone, fqdn string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.records {
		name := r[0] + "." + zone
		if r[0] == "" {
			name = zone
		}
		if name == fqdn {
			out = append(out, r[1])
		}
	}
	return out
}

func challenge(t *testing.T, domain string) acme.Challenge {
	t.Helper()
	key, _, err := NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	acct := acme.Account{PrivateKey: key}
	thumb, err := acct.Thumbprint()
	if err != nil {
		t.Fatal(err)
	}
	return acme.Challenge{Type: acme.ChallengeTypeDNS01, Token: "tok-" + domain, KeyAuthorization: "tok-" + domain + "." + thumb,
		Identifier: acme.Identifier{Type: "dns", Value: domain}}
}

// The record goes under the zone by its own name, the wait lets go only once
// every authoritative server answers with it, and the clean-up takes it back.
func TestTheRecordIsPutWaitedForAndTakenBack(t *testing.T) {
	dns := &fakeDNS{records: map[string][2]string{}}
	var mu sync.Mutex
	lagging := true // the second server has not got it yet
	is := &Issuer{DNS: dns, Propagation: 20 * time.Second, LookupTXT: func(_ context.Context, zone, name string) ([][]string, error) {
		mu.Lock()
		defer mu.Unlock()
		got := dns.txt(zone, name)
		if lagging {
			lagging = false
			return [][]string{got, nil}, nil
		}
		return [][]string{got, got}, nil
	}}
	for _, tc := range []struct{ zone, domain, name string }{
		{"otter9119.lol", "otter9119.lol", "_acme-challenge"},
		{"otter9119.lol", "rx.otter9119.lol", "_acme-challenge.rx"},
	} {
		s := &dnsSolver{is: is, zone: tc.zone, records: map[string]string{}}
		chal := challenge(t, tc.domain)
		if got := s.name(chal); got != tc.name {
			t.Fatalf("%s: record name %q, want %q", tc.domain, got, tc.name)
		}
		if err := s.Present(context.Background(), chal); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := s.Wait(context.Background(), chal); err != nil {
			t.Fatal(err)
		}
		if tc.domain == "otter9119.lol" && time.Since(start) < 4*time.Second {
			t.Fatalf("the wait let go while a server still lacked the record")
		}
		if got := dns.txt(tc.zone, "_acme-challenge."+tc.domain); len(got) != 1 || got[0] != chal.DNS01KeyAuthorization() {
			t.Fatalf("%s: TXT %v", tc.domain, got)
		}
		if err := s.CleanUp(context.Background(), chal); err != nil {
			t.Fatal(err)
		}
		if got := dns.txt(tc.zone, "_acme-challenge."+tc.domain); len(got) != 0 {
			t.Fatalf("%s: record left after clean-up: %v", tc.domain, got)
		}
	}
}

// A record that never shows fails the wait at the limit rather than letting
// the CA look and fail the order.
func TestARecordThatNeverShowsFailsTheWait(t *testing.T) {
	is := &Issuer{DNS: &fakeDNS{records: map[string][2]string{}}, Propagation: 50 * time.Millisecond,
		LookupTXT: func(context.Context, string, string) ([][]string, error) { return [][]string{nil}, nil }}
	s := &dnsSolver{is: is, zone: "otter9119.lol", records: map[string]string{}}
	if err := s.Wait(context.Background(), challenge(t, "otter9119.lol")); err == nil {
		t.Fatal("wait passed without the record")
	}
}

func TestADomainOutsideTheZoneIsRefused(t *testing.T) {
	key, _, _ := NewAccountKey()
	is := &Issuer{Directory: LetsEncryptStaging, DNS: &fakeDNS{}, AccountKey: key}
	if _, err := is.Issue(context.Background(), "otter9119.lol", "otter9119.lol.evil.com"); err == nil {
		t.Fatal("issued outside the zone")
	}
}

func TestTheAccountKeyRoundTrips(t *testing.T) {
	_, pemKey, err := NewAccountKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseKey(pemKey); err != nil {
		t.Fatal(err)
	}
}
