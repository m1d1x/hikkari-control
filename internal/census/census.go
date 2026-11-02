// Package census measures the entries of VPN subscriptions the owner paid for,
// next to our own relays, to tell bad settings from bad servers. It holds
// addresses and counts; a subscription's URL is kept only under cryptobox, and
// profile secrets never reach it. Nothing here is served to clients.
package census

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/support"
)

// MinInterval is the floor between measurements of one target from one vantage.
// A little under the hour, so an hourly run does not skip itself.
const MinInterval = 55 * time.Minute

// udpProtocols have no TCP door: a TCP count would say nothing about them.
var udpProtocols = map[string]bool{"hysteria2": true, "hysteria": true, "tuic": true, "wireguard": true, "awg": true, "amneziawg": true}

// Entry is one server of a subscription, as the parser reports it.
type Entry struct {
	Label     string `json:"label"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Protocol  string `json:"protocol"`
	Transport string `json:"transport"`
	Security  string `json:"security"`
	SNI       string `json:"sni,omitempty"`
	// Config is the sing-box outbound, secrets included. It is sealed on save
	// and never leaves in a report or a target list.
	Config json.RawMessage `json:"config,omitempty"`
}

// Target is one address to measure.
type Target struct {
	Label string `json:"label"`
	Addr  string `json:"addr"`
}

// Measurement is one count from one vantage.
type Measurement struct {
	Target  string `json:"target"`
	Vantage string `json:"vantage"`
	Kind    string `json:"kind"`
	Opened  int    `json:"opened"`
	Total   int    `json:"total"`
}

// Network is what Team Cymru says about an address.
type Network struct {
	ASN     int
	Name    string
	Country string
}

// Lookup names the network of an address.
type Lookup func(ctx context.Context, ip string) (Network, error)

// Store is the census in Postgres.
type Store struct {
	pool   *pgxpool.Pool
	tester support.TesterStore
	lookup Lookup
	now    func() time.Time
}

// NewStore returns a store that asks Team Cymru over DNS.
func NewStore(pool *pgxpool.Pool, tester support.TesterStore) *Store {
	return &Store{pool: pool, tester: tester, lookup: CymruLookup, now: time.Now}
}

// publicIPv4 is the rule probe_targets applies on both ends.
func publicIPv4(s string) bool {
	ip, err := netip.ParseAddr(s)
	if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("198.18.0.0/15")} {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func resolveIPv4(ctx context.Context, host string) string {
	if _, err := netip.ParseAddr(host); err == nil {
		if publicIPv4(host) {
			return host
		}
		return ""
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return ""
	}
	for _, ip := range ips {
		if publicIPv4(ip.String()) {
			return ip.String()
		}
	}
	return ""
}

// AddResult says what a save did, with no secrets in it.
type AddResult struct {
	Subscription string `json:"subscription"`
	Entries      int    `json:"entries"`
	Gone         int    `json:"gone"`
	Unresolved   int    `json:"unresolved"`
}

// Add saves a subscription and its current entries. An entry the provider no
// longer lists is marked gone, not deleted: its lifetime is the measurement.
func (s *Store) Add(ctx context.Context, name, subURL string, entries []Entry) (AddResult, error) {
	name = strings.TrimSpace(name)
	if name == "" || subURL == "" {
		return AddResult{}, errors.New("name and url are required")
	}
	ct, err := cryptobox.EncryptString(subURL)
	if err != nil {
		return AddResult{}, err
	}
	res := AddResult{Subscription: name, Entries: len(entries)}
	ips := make([]string, len(entries))
	for i, e := range entries {
		if ips[i] = resolveIPv4(ctx, e.Host); ips[i] == "" {
			res.Unresolved++
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AddResult{}, err
	}
	defer tx.Rollback(ctx)
	var subID int64
	if err := tx.QueryRow(ctx, `INSERT INTO census_subscriptions (name, url_ct) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET url_ct = EXCLUDED.url_ct RETURNING id`, name, ct).Scan(&subID); err != nil {
		return AddResult{}, err
	}
	kept := make([]int64, 0, len(entries))
	for i, e := range entries {
		var id int64
		var config []byte
		if len(e.Config) > 0 {
			if config, err = cryptobox.Encrypt(e.Config); err != nil {
				return AddResult{}, err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO census_entries (subscription_id, label, host, port, protocol, transport, security, sni, ip, config_ct)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (subscription_id, host, port, protocol, transport) DO UPDATE
			SET label = EXCLUDED.label, security = EXCLUDED.security, sni = EXCLUDED.sni, ip = EXCLUDED.ip, config_ct = EXCLUDED.config_ct, gone_at = NULL
			RETURNING id`, subID, e.Label, e.Host, e.Port, e.Protocol, e.Transport, e.Security, e.SNI, ips[i], config).Scan(&id); err != nil {
			return AddResult{}, err
		}
		kept = append(kept, id)
	}
	tag, err := tx.Exec(ctx, `UPDATE census_entries SET gone_at = now()
		WHERE subscription_id = $1 AND gone_at IS NULL AND NOT (id = ANY($2))`, subID, kept)
	if err != nil {
		return AddResult{}, err
	}
	res.Gone = int(tag.RowsAffected())
	if err := tx.Commit(ctx); err != nil {
		return AddResult{}, err
	}
	for _, ip := range ips {
		s.noteNetwork(ctx, ip)
	}
	return res, nil
}

// AddAddresses saves a service with no importable subscription: addresses the
// operator saw its app use. They get a network and a TCP count, no protocol.
func (s *Store) AddAddresses(ctx context.Context, name string, addrs []string) (AddResult, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(addrs) == 0 {
		return AddResult{}, errors.New("name and addresses are required")
	}
	type parsed struct {
		ip   string
		port int
	}
	list := make([]parsed, 0, len(addrs))
	for _, a := range addrs {
		ap, err := netip.ParseAddrPort(strings.TrimSpace(a))
		if err != nil || !publicIPv4(ap.Addr().String()) || ap.Port() == 0 {
			return AddResult{}, fmt.Errorf("%q: want a public ipv4:port", a)
		}
		list = append(list, parsed{ap.Addr().String(), int(ap.Port())})
	}
	var subID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO census_subscriptions (name) VALUES ($1)
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name RETURNING id`, name).Scan(&subID); err != nil {
		return AddResult{}, err
	}
	for _, p := range list {
		if _, err := s.pool.Exec(ctx, `INSERT INTO census_entries (subscription_id, label, host, port, protocol, transport, security, ip)
			VALUES ($1, $2, $2, $3, 'unknown', 'unknown', 'unknown', $2)
			ON CONFLICT (subscription_id, host, port, protocol, transport) DO UPDATE SET gone_at = NULL`, subID, p.ip, p.port); err != nil {
			return AddResult{}, err
		}
		s.noteNetwork(ctx, p.ip)
	}
	return AddResult{Subscription: name, Entries: len(list)}, nil
}

// noteNetwork fills the cache once per address. A failed lookup leaves the
// network blank in the report and is tried again next time.
func (s *Store) noteNetwork(ctx context.Context, ip string) {
	if ip == "" {
		return
	}
	var known bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM census_networks WHERE ip = $1)`, ip).Scan(&known); err != nil || known {
		return
	}
	n, err := s.lookup(ctx, ip)
	if err != nil {
		return
	}
	_, _ = s.pool.Exec(ctx, `INSERT INTO census_networks (ip, asn, as_name, country) VALUES ($1, $2, $3, $4)
		ON CONFLICT (ip) DO NOTHING`, ip, n.ASN, n.Name, n.Country)
}

// row is one target before measurements are joined to it.
type row struct {
	Service, Entry, Label, IP, Protocol, Transport, Security string
	Port                                                     int
	Ours                                                     bool
	Since                                                    time.Time
}

func (r row) tcp() bool { return r.IP != "" && !udpProtocols[strings.ToLower(r.Protocol)] }

func (s *Store) rows(ctx context.Context) ([]row, error) {
	var out []row
	rows, err := s.pool.Query(ctx, `SELECT sub.name, e.id, e.label, e.ip, e.port, e.protocol, e.transport, e.security, e.first_seen_at
		FROM census_entries e JOIN census_subscriptions sub ON sub.id = e.subscription_id
		WHERE e.gone_at IS NULL ORDER BY e.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r row
		var id int64
		if err := rows.Scan(&r.Service, &id, &r.Entry, &r.IP, &r.Port, &r.Protocol, &r.Transport, &r.Security, &r.Since); err != nil {
			return nil, err
		}
		r.Label = fmt.Sprintf("sub%d-%d", id, r.Port)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A box's newest relay says its port, dead or not: a banned relay is what the
	// comparison is for. Reality may have moved off 443 after a port ban.
	boxes, err := s.pool.Query(ctx, `SELECT b.name, b.address_ct, b.proto, b.created_at,
			(SELECT r.identity_ct FROM entry_relays r WHERE r.box = b.name ORDER BY r.id DESC LIMIT 1)
		FROM entry_boxes b WHERE b.retired_at IS NULL ORDER BY b.name`)
	if err != nil {
		return nil, err
	}
	defer boxes.Close()
	for boxes.Next() {
		var name, proto string
		var ct, identity []byte
		var since time.Time
		if err := boxes.Scan(&name, &ct, &proto, &since, &identity); err != nil {
			return nil, err
		}
		addr, err := cryptobox.DecryptString(ct)
		if err != nil {
			return nil, fmt.Errorf("box %s: %w", name, err)
		}
		port := relayPort(identity)
		r := row{Service: "hikkari", Entry: name, Label: fmt.Sprintf("%s-%d", name, port), Port: port, Ours: true, Since: since, Security: "tls"}
		if publicIPv4(addr) {
			r.IP = addr
		}
		r.Protocol, r.Transport = boxProtocol(proto)
		if r.Protocol == "vless" && proto == "vless-reality-vision" {
			r.Security = "reality"
		}
		out = append(out, r)
	}
	return out, boxes.Err()
}

// relayPort reads the port out of a sealed relay identity, nothing else of it.
func relayPort(identity []byte) int {
	if len(identity) > 0 {
		if plain, err := cryptobox.Decrypt(identity); err == nil {
			var id struct {
				Port int `json:"port"`
			}
			if json.Unmarshal(plain, &id) == nil && id.Port > 0 {
				return id.Port
			}
		}
	}
	return 443
}

func boxProtocol(proto string) (protocol, transport string) {
	switch proto {
	case "vless-reality-vision":
		return "vless", "tcp"
	case "vless-xhttp":
		return "vless", "xhttp"
	}
	return proto, "udp"
}

// Targets lists what is due from a vantage: TCP targets with a public address
// not measured there within MinInterval.
func (s *Store) Targets(ctx context.Context, vantage string) ([]Target, error) {
	all, err := s.rows(ctx)
	if err != nil {
		return nil, err
	}
	recent := map[string]bool{}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT target FROM census_measurements
		WHERE vantage = $1 AND kind = 'tcp' AND measured_at > $2`, vantage, s.now().Add(-MinInterval))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		recent[t] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Target{}
	for _, r := range all {
		if r.tcp() && !recent[r.Label] {
			s.noteNetwork(ctx, r.IP)
			out = append(out, Target{Label: r.Label, Addr: net.JoinHostPort(r.IP, strconv.Itoa(r.Port))})
		}
	}
	return out, nil
}

// MaxProtoTargets is one command's share: each profile takes up to 20 s of the
// client's two minutes.
const MaxProtoTargets = 5

// protoDue lists sealed profiles not tried from a vantage within MinInterval.
func (s *Store) protoDue(ctx context.Context, vantage string) ([]byte, int, error) {
	rows, err := s.pool.Query(ctx, `SELECT e.id, e.port, e.config_ct FROM census_entries e
		WHERE e.gone_at IS NULL AND e.config_ct IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM census_measurements m
		    WHERE m.target = 'sub' || e.id || '-' || e.port AND m.vantage = $1 AND m.kind = 'proto' AND m.measured_at > $2)
		ORDER BY e.id LIMIT $3`, vantage, s.now().Add(-MinInterval), MaxProtoTargets)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	type target struct {
		Label    string          `json:"label"`
		Outbound json.RawMessage `json:"outbound"`
		ExitURL  string          `json:"exit_url,omitempty"`
	}
	var targets []target
	for rows.Next() {
		var id int64
		var port int
		var ct []byte
		if err := rows.Scan(&id, &port, &ct); err != nil {
			return nil, 0, err
		}
		plain, err := cryptobox.Decrypt(ct)
		if err != nil {
			return nil, 0, err
		}
		label := fmt.Sprintf("sub%d-%d", id, port)
		exit, err := s.exitURL(ctx, label)
		if err != nil {
			return nil, 0, err
		}
		targets = append(targets, target{label, plain, exit})
	}
	if err := rows.Err(); err != nil || len(targets) == 0 {
		return nil, 0, err
	}
	plain, err := json.Marshal(map[string]any{"targets": targets})
	return plain, len(targets), err
}

// Record saves counts.
func (s *Store) Record(ctx context.Context, ms []Measurement) error {
	batch := &pgx.Batch{}
	for _, m := range ms {
		if m.Target == "" || m.Vantage == "" || (m.Kind != "tcp" && m.Kind != "proto") || m.Total <= 0 || m.Opened < 0 || m.Opened > m.Total {
			return fmt.Errorf("bad measurement for %q", m.Target)
		}
		batch.Queue(`INSERT INTO census_measurements (target, vantage, kind, opened, total, measured_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			m.Target, m.Vantage, m.Kind, m.Opened, m.Total, s.now())
	}
	return s.pool.SendBatch(ctx, batch).Close()
}

// Count is the latest count from a vantage.
type Count struct {
	Opened int       `json:"opened"`
	Total  int       `json:"total"`
	At     time.Time `json:"at"`
}

// ReportRow is one line of the table.
type ReportRow struct {
	Service     string           `json:"service"`
	Entry       string           `json:"entry"`
	Label       string           `json:"label"`
	Ours        bool             `json:"ours"`
	ASN         int              `json:"asn"`
	Network     string           `json:"network"`
	Country     string           `json:"country"`
	Protocol    string           `json:"protocol"`
	Transport   string           `json:"transport"`
	Security    string           `json:"security"`
	Addr        map[string]Count `json:"addr_alive"`
	Proto       map[string]Count `json:"proto_alive"`
	AliveHours  *float64         `json:"alive_hours"`
	Exits       []Exit           `json:"exits,omitempty"`
	NotMeasured string           `json:"not_measured,omitempty"`
}

// Verdict compares us and them in one network from one vantage.
type Verdict struct {
	ASN        int    `json:"asn"`
	Network    string `json:"network"`
	Vantage    string `json:"vantage"`
	TheirAlive int    `json:"their_alive"`
	TheirDead  int    `json:"their_dead"`
	OurAlive   int    `json:"our_alive"`
	OurDead    int    `json:"our_dead"`
	Reads      string `json:"reads"`
}

// Report is the whole answer.
type Report struct {
	Rows     []ReportRow `json:"rows"`
	Verdicts []Verdict   `json:"verdicts"`
	Caveat   string      `json:"caveat"`
	// ServiceExits are exits of services with no import, by service name.
	ServiceExits map[string][]Exit `json:"service_exits,omitempty"`
}

// Report joins targets to their latest counts, one network next to itself.
func (s *Store) Report(ctx context.Context) (Report, error) {
	all, err := s.rows(ctx)
	if err != nil {
		return Report{}, err
	}
	nets := map[string]Network{}
	rows, err := s.pool.Query(ctx, `SELECT ip, asn, as_name, country FROM census_networks`)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var ip string
		var n Network
		if err := rows.Scan(&ip, &n.ASN, &n.Name, &n.Country); err != nil {
			return Report{}, err
		}
		nets[ip] = n
	}
	if err := rows.Err(); err != nil {
		return Report{}, err
	}
	latest := map[string]map[string]map[string]Count{} // target, kind, vantage
	m, err := s.pool.Query(ctx, `SELECT DISTINCT ON (target, kind, vantage) target, kind, vantage, opened, total, measured_at
		FROM census_measurements ORDER BY target, kind, vantage, measured_at DESC`)
	if err != nil {
		return Report{}, err
	}
	defer m.Close()
	for m.Next() {
		var target, kind, vantage string
		var c Count
		if err := m.Scan(&target, &kind, &vantage, &c.Opened, &c.Total, &c.At); err != nil {
			return Report{}, err
		}
		if latest[target] == nil {
			latest[target] = map[string]map[string]Count{}
		}
		if latest[target][kind] == nil {
			latest[target][kind] = map[string]Count{}
		}
		latest[target][kind][vantage] = c
	}
	if err := m.Err(); err != nil {
		return Report{}, err
	}
	spans := map[string]float64{}
	sp, err := s.pool.Query(ctx, `SELECT target, EXTRACT(EPOCH FROM max(measured_at) - min(measured_at)) / 3600
		FROM census_measurements WHERE opened > 0 GROUP BY target`)
	if err != nil {
		return Report{}, err
	}
	defer sp.Close()
	for sp.Next() {
		var t string
		var h float64
		if err := sp.Scan(&t, &h); err != nil {
			return Report{}, err
		}
		spans[t] = h
	}
	if err := sp.Err(); err != nil {
		return Report{}, err
	}
	exits, err := s.exits(ctx)
	if err != nil {
		return Report{}, err
	}
	out := make([]ReportRow, 0, len(all))
	for _, r := range all {
		n := nets[r.IP]
		rr := ReportRow{Service: r.Service, Entry: r.Entry, Label: r.Label, Ours: r.Ours, ASN: n.ASN, Network: n.Name, Country: n.Country,
			Protocol: r.Protocol, Transport: r.Transport, Security: r.Security,
			Addr: latest[r.Label]["tcp"], Proto: latest[r.Label]["proto"], Exits: exits[r.Label]}
		if h, ok := spans[r.Label]; ok {
			rr.AliveHours = &h
		}
		switch {
		case r.IP == "":
			rr.NotMeasured = "адрес не разрешился в публичный IPv4"
		case !r.tcp():
			rr.NotMeasured = "UDP-протокол: TCP-счёт к нему неприменим, смотри proto_alive"
		}
		out = append(out, rr)
	}
	rep := Build(out)
	for owner, list := range exits {
		if name, ok := strings.CutPrefix(owner, "service:"); ok {
			if rep.ServiceExits == nil {
				rep.ServiceExits = map[string][]Exit{}
			}
			rep.ServiceExits[name] = list
		}
	}
	return rep, nil
}

// Build sorts the rows and reads each network. A vantage reads a target alive
// when any of its flows opened; only rows that have a count there are compared.
func Build(rows []ReportRow) Report {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ASN != rows[j].ASN {
			return rows[i].ASN < rows[j].ASN
		}
		return !rows[i].Ours && rows[j].Ours
	})
	type key struct {
		asn     int
		vantage string
	}
	verdicts := map[key]*Verdict{}
	for _, r := range rows {
		if r.ASN == 0 {
			continue
		}
		for vantage, c := range r.Addr {
			k := key{r.ASN, vantage}
			v := verdicts[k]
			if v == nil {
				v = &Verdict{ASN: r.ASN, Network: r.Network, Vantage: vantage}
				verdicts[k] = v
			}
			switch {
			case r.Ours && c.Opened > 0:
				v.OurAlive++
			case r.Ours:
				v.OurDead++
			case c.Opened > 0:
				v.TheirAlive++
			default:
				v.TheirDead++
			}
		}
	}
	list := make([]Verdict, 0, len(verdicts))
	for _, v := range verdicts {
		v.Reads = reads(*v)
		list = append(list, *v)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].ASN != list[j].ASN {
			return list[i].ASN < list[j].ASN
		}
		return list[i].Vantage < list[j].Vantage
	})
	return Report{Rows: rows, Verdicts: list,
		Caveat: "Сравнение по адресу, не по протоколу: живой TCP не значит живой туннель. Вход в той же сети не значит тот же маршрут."}
}

func reads(v Verdict) string {
	theirs, ours := v.TheirAlive+v.TheirDead, v.OurAlive+v.OurDead
	switch {
	case theirs == 0 || ours == 0:
		return "сравнивать не с чем"
	case v.TheirAlive > 0 && v.OurAlive == 0:
		return "чужой жив, наш мёртв: наши адреса или настройки"
	case v.TheirAlive == 0 && v.OurAlive == 0:
		return "оба мертвы: адрес или сеть"
	case v.TheirAlive == 0:
		return "наш жив, чужой мёртв"
	}
	return "оба живы"
}

// CymruLookup asks Team Cymru over DNS; it needs no key.
func CymruLookup(ctx context.Context, ip string) (Network, error) {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() {
		return Network{}, fmt.Errorf("not ipv4: %q", ip)
	}
	b := a.As4()
	txt, err := net.DefaultResolver.LookupTXT(ctx, fmt.Sprintf("%d.%d.%d.%d.origin.asn.cymru.com", b[3], b[2], b[1], b[0]))
	if err != nil {
		return Network{}, err
	}
	if len(txt) == 0 {
		return Network{}, errors.New("cymru: no origin")
	}
	n, err := parseOrigin(txt[0])
	if err != nil {
		return Network{}, err
	}
	if names, err := net.DefaultResolver.LookupTXT(ctx, fmt.Sprintf("AS%d.asn.cymru.com", n.ASN)); err == nil && len(names) > 0 {
		n.Name = parseASName(names[0])
	}
	return n, nil
}

// parseOrigin reads «213877 | 1.2.3.0/24 | NL | ripencc | 2023-01-01». A prefix
// announced by several ASes lists them; the first stands for it.
func parseOrigin(s string) (Network, error) {
	f := strings.Split(s, "|")
	if len(f) < 3 {
		return Network{}, fmt.Errorf("cymru: %q", s)
	}
	asn, err := strconv.Atoi(strings.Fields(f[0] + " x")[0])
	if err != nil || asn <= 0 {
		return Network{}, fmt.Errorf("cymru: %q", s)
	}
	return Network{ASN: asn, Country: strings.TrimSpace(f[2])}, nil
}

// parseASName reads «213877 | NL | ripencc | 2023-01-01 | U1-AS, NL».
func parseASName(s string) string {
	f := strings.Split(s, "|")
	return strings.TrimSpace(f[len(f)-1])
}

// RegisterOpsRoutes mounts the census on the ops plane.
func RegisterOpsRoutes(mux *http.ServeMux, s *Store, gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST /api/v1/ops/census/subscriptions", gate("ops.census.add", s.handleAdd))
	mux.HandleFunc("POST /api/v1/ops/census/addresses", gate("ops.census.add", s.handleAddresses))
	mux.HandleFunc("POST /api/v1/ops/census/proto", gate("ops.census.proto", s.handleProto))
	mux.HandleFunc("POST /api/v1/ops/census/exit-link", gate("ops.census.add", s.handleExitLink))
	mux.HandleFunc("GET /api/v1/ops/census/targets", gate("", s.handleTargets))
	mux.HandleFunc("POST /api/v1/ops/census/measurements", gate("ops.census.measure", s.handleRecord))
	mux.HandleFunc("GET /api/v1/ops/census/report", gate("ops.census.read", s.handleReport))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Store) handleAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string  `json:"name"`
		URL     string  `json:"url"`
		Entries []Entry `json:"entries"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body") // the decoder's text can quote the URL
		return
	}
	res, err := s.Add(r.Context(), req.Name, req.URL, req.Entries)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "census add failed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Store) handleAddresses(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string   `json:"name"`
		Addresses []string `json:"addresses"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	res, err := s.AddAddresses(r.Context(), req.Name, req.Addresses)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleProto queues the next due profiles for a tester. Its answer names the
// command and how many profiles rode it; zero means nothing is due.
func (s *Store) handleProto(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Who     string `json:"who"`
		Vantage string `json:"vantage"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil || req.Who == "" || req.Vantage == "" {
		writeErr(w, http.StatusBadRequest, "who and vantage are required")
		return
	}
	plain, n, err := s.protoDue(r.Context(), req.Vantage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "census proto failed")
		return
	}
	if n == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"targets": 0})
		return
	}
	cmd, version, err := support.QueueProto(r.Context(), s.tester, req.Who, plain)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": n, "nonce": cmd.Nonce, "kind": cmd.Kind, "state": cmd.State,
		"created_at": cmd.CreatedAt, "alias": req.Who, "version": version})
}

func (s *Store) handleTargets(w http.ResponseWriter, r *http.Request) {
	vantage := r.URL.Query().Get("vantage")
	if vantage == "" {
		writeErr(w, http.StatusBadRequest, "vantage is required")
		return
	}
	out, err := s.Targets(r.Context(), vantage)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Store) handleRecord(w http.ResponseWriter, r *http.Request) {
	var ms []Measurement
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ms); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if err := s.Record(r.Context(), ms); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"recorded": len(ms)})
}

func (s *Store) handleReport(w http.ResponseWriter, r *http.Request) {
	out, err := s.Report(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
