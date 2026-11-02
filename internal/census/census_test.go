package census

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/support"
)

func TestParseCymru(t *testing.T) {
	n, err := parseOrigin("213877 12345 | 1.2.3.0/24 | NL | ripencc | 2023-01-01")
	if err != nil || n.ASN != 213877 || n.Country != "NL" {
		t.Fatalf("%+v %v", n, err)
	}
	if _, err := parseOrigin("garbage"); err == nil {
		t.Fatal("want an error")
	}
	if got := parseASName("213877 | NL | ripencc | 2023-01-01 | U1-AS, NL"); got != "U1-AS, NL" {
		t.Fatal(got)
	}
}

func TestPublicIPv4(t *testing.T) {
	for ip, want := range map[string]bool{"8.8.8.8": true, "10.0.0.1": false, "198.18.0.5": false, "100.64.1.1": false, "127.0.0.1": false, "::1": false, "x": false} {
		if publicIPv4(ip) != want {
			t.Errorf("%s: want %v", ip, want)
		}
	}
}

func TestVerdicts(t *testing.T) {
	row := func(asn int, ours bool, opened int) ReportRow {
		return ReportRow{ASN: asn, Ours: ours, Addr: map[string]Count{"spb": {Opened: opened, Total: 12}}}
	}
	r := Build([]ReportRow{row(1, true, 0), row(1, false, 5), row(2, true, 0), row(2, false, 0), row(3, false, 5)})
	want := map[int]string{1: "чужой жив, наш мёртв", 2: "оба мертвы", 3: "сравнивать не с чем"}
	for _, v := range r.Verdicts {
		if !strings.HasPrefix(v.Reads, want[v.ASN]) {
			t.Errorf("asn %d: %q", v.ASN, v.Reads)
		}
	}
	if r.Rows[0].ASN != 1 || r.Rows[0].Ours {
		t.Fatalf("their entry must stand first in its network: %+v", r.Rows[0])
	}
}

func dbStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	adminURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if adminURL == "" {
		t.Skip("no test DB — set TEST_DATABASE_URL to run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("hikkari_census_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cryptobox.ConfigureEphemeralForTests()
	s := NewStore(pool, support.NewMemTesterStore())
	s.lookup = func(context.Context, string) (Network, error) {
		return Network{ASN: 216071, Name: "VDSINA", Country: "NL"}, nil
	}
	return s, pool
}

func TestStoreRoundTrip(t *testing.T) {
	s, pool := dbStore(t)
	ctx := context.Background()
	const subURL = "https://provider.example/sub/SECRET-TOKEN"
	res, err := s.Add(ctx, "svc", subURL, []Entry{
		{Label: "one", Host: "8.8.8.8", Port: 443, Protocol: "vless", Transport: "tcp", Security: "reality"},
		{Label: "two", Host: "8.8.4.4", Port: 8443, Protocol: "hysteria2", Transport: "tcp", Security: "tls"},
	})
	if err != nil || res.Entries != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	var ct []byte
	if err := pool.QueryRow(ctx, `SELECT url_ct FROM census_subscriptions`).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ct), "SECRET-TOKEN") {
		t.Fatal("URL stored in the clear")
	}
	addr, _ := cryptobox.EncryptString("9.9.9.9")
	if _, err := pool.Exec(ctx, `INSERT INTO entry_boxes (name, address_ct, provider, location, proto) VALUES ('ams3', $1, 'vdsina', 'ams', 'vless-reality-vision')`, addr); err != nil {
		t.Fatal(err)
	}

	targets, err := s.Targets(ctx, "spb")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 { // the UDP entry has no TCP door
		t.Fatalf("targets = %+v", targets)
	}
	var ms []Measurement
	for i, tg := range targets {
		ms = append(ms, Measurement{Target: tg.Label, Vantage: "spb", Kind: "tcp", Opened: (1 - i) * 12, Total: 12})
	}
	if err := s.Record(ctx, ms); err != nil {
		t.Fatal(err)
	}
	if again, err := s.Targets(ctx, "spb"); err != nil || len(again) != 0 {
		t.Fatalf("measured within the hour must not be due: %+v %v", again, err)
	}
	if other, err := s.Targets(ctx, "tester-002"); err != nil || len(other) != 2 {
		t.Fatalf("another vantage is due: %+v %v", other, err)
	}

	rep, err := s.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 3 || len(rep.Verdicts) != 1 {
		t.Fatalf("%+v", rep)
	}
	if !strings.HasPrefix(rep.Verdicts[0].Reads, "чужой жив, наш мёртв") {
		t.Fatalf("%+v", rep.Verdicts[0])
	}

	// A service with no import: an address, its network and a TCP count.
	if _, err := s.AddAddresses(ctx, "sota", []string{"1.1.1.1:443"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAddresses(ctx, "sota", []string{"10.0.0.1:443"}); err == nil {
		t.Fatal("a private address must be refused")
	}

	// A sealed profile comes due once, and is not stored in the clear.
	secret := json.RawMessage(`{"type":"vless","server":"8.8.8.8","uuid":"SECRET-UUID"}`)
	if _, err := s.Add(ctx, "cfg", subURL, []Entry{{Label: "c", Host: "8.8.8.8", Port: 2053, Protocol: "vless", Transport: "tcp", Security: "tls", Config: secret}}); err != nil {
		t.Fatal(err)
	}
	var cfg []byte
	if err := pool.QueryRow(ctx, `SELECT config_ct FROM census_entries WHERE config_ct IS NOT NULL`).Scan(&cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "SECRET-UUID") {
		t.Fatal("profile stored in the clear")
	}
	plain, n, err := s.protoDue(ctx, "tester:002")
	if err != nil || n != 1 || !strings.Contains(string(plain), "SECRET-UUID") {
		t.Fatalf("due: %d %v", n, err)
	}
	report, err := s.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(report); strings.Contains(string(b), "SECRET-UUID") {
		t.Fatal("report leaks a profile")
	}
	var label string
	if err := pool.QueryRow(ctx, `SELECT 'sub' || id || '-' || port FROM census_entries WHERE config_ct IS NOT NULL`).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, []Measurement{{Target: label, Vantage: "tester:002", Kind: "proto", Opened: 1, Total: 3}}); err != nil {
		t.Fatal(err)
	}
	if _, n, err := s.protoDue(ctx, "tester:002"); err != nil || n != 0 {
		t.Fatalf("tried within the hour must not be due: %d %v", n, err)
	}

	// An exit: a known token records the source, an unknown one writes nothing.
	t.Setenv("CENSUS_EXIT_BASE", "https://hub.example/")
	link, err := s.exitURL(ctx, "service:sota")
	if err != nil || !strings.HasPrefix(link, "https://hub.example/e/") {
		t.Fatalf("%q %v", link, err)
	}
	if again, _ := s.exitURL(ctx, "service:sota"); again != link {
		t.Fatalf("token must stay: %q %q", again, link)
	}
	token := strings.TrimPrefix(link, "https://hub.example/e/")
	if err := s.RecordExit(ctx, token, "8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExit(ctx, "nope", "8.8.8.8"); err == nil {
		t.Fatal("an unknown token must not record")
	}
	report, err = s.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.ServiceExits["sota"]; len(got) != 1 || got[0].ASN != 216071 {
		t.Fatalf("%+v", report.ServiceExits)
	}

	// The provider drops an entry: it is gone, and so is not measured.
	res, err = s.Add(ctx, "svc", subURL, []Entry{{Label: "two", Host: "8.8.4.4", Port: 8443, Protocol: "hysteria2", Transport: "tcp", Security: "tls"}})
	if err != nil || res.Gone != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}
