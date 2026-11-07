package knobcheck

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// What reached a node, from the edge's hello tap (edge/hellotap.go): per relay
// and edge run, how many connections brought each hk under each cover. It is
// held to the release's own hellos: the hk the release's core sends for the
// fingerprint the node hands out. Everyone who connects is counted, probers
// and scanners too, so the share of the expected hk is read, not a verdict:
// a node our clients reach whose expected hk is missing, while another hash
// no core sends dominates its covers, is a path that rewrites hellos.

// ReleaseCore is the core revision the release ships (hikkari-client
// testdata/hello/reference.json); releaseHellos are its hk per fingerprint.
// Re-record both with a new core: they are what the node's counts are named by.
const ReleaseCore = "c06e742b5717"

var releaseHellos = map[string]string{
	"360":        "h1a028e2f5c9a5dc21",
	"android":    "h1b16c973fd5fb56a7",
	"chrome":     "h1e431ec7444af069a",
	"chrome_155": "h1121e2b99d2744911",
	"edge":       "h186e519f72a3bd01a",
	"firefox":    "h1a7f29bb57cd8c350",
	"go":         "h11c409d9ffa0e9507",
	"ios":        "h122df09a9d9832ecc",
	"qq":         "h10afd45ddf31c8b3a",
	"safari":     "h1c4b9dfb6a023e46d",
}

// knownHellos names hashes that are no core fingerprint but seen for sure
// (hikkari-client testdata/hello/browsers.json).
var knownHellos = map[string]string{
	"h13ba089e2b415f898": "chrome+key_share=classical",
	"h1e08906f1ba730b11": "browser:chromium-152",
}

// helloName is what an hk is known as, "" when it is none of ours.
func helloName(hk string) string {
	for fp, h := range releaseHellos {
		if h == hk {
			return "fp:" + fp
		}
	}
	return knownHellos[hk]
}

var (
	reTapHK    = regexp.MustCompile(`^(h1[0-9a-f]{16}|unread|unparsed|other)$`)
	reTapCover = regexp.MustCompile(`^(-|[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?)$`)
	reTapBoot  = regexp.MustCompile(`^[0-9a-f]{16,32}$`)
)

// maxTapRows bounds one pulse: the tap keeps at most 512 pairs and an other.
const maxTapRows = 1024

// RecordHellos files one edge run's totals (entry.EdgeHellos). A pair that
// does not read as the tap writes it is dropped, the rest kept: a pulse is
// never refused for its counters.
func (s *Store) RecordHellos(ctx context.Context, relay, boot string, counts map[string]map[string]int64) error {
	if !reTapBoot.MatchString(boot) {
		return nil
	}
	n := 0
	for cover, byHK := range counts {
		if !reTapCover.MatchString(cover) {
			continue
		}
		for hk, conns := range byHK {
			if !reTapHK.MatchString(hk) || conns < 0 {
				continue
			}
			if n++; n > maxTapRows {
				return nil
			}
			if _, err := s.pool.Exec(ctx, `
				INSERT INTO knob_edge_hellos (relay, boot, cover, hk, conns) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (relay, boot, cover, hk) DO UPDATE
				SET conns = greatest(knob_edge_hellos.conns, EXCLUDED.conns), updated_at = now()`,
				relay, boot, cover, hk, conns); err != nil {
				return fmt.Errorf("record edge hellos: %w", err)
			}
		}
	}
	return nil
}

// EdgeHello is one hash a node received, summed over the runs in the window.
type EdgeHello struct {
	Cover string `json:"cover"`
	HK    string `json:"hk"`
	Name  string `json:"name,omitempty"` // fp:chrome_155, browser:…, or none
	Conns int64  `json:"conns"`
}

// EdgeNode is what one relay received, with what it should have.
type EdgeNode struct {
	Relay   string `json:"relay"`
	Profile string `json:"profile,omitempty"`
	// FP is what the node hands out to a subscriber of no known network, and
	// Expect the hk the release's core sends for it.
	FP     string      `json:"fp,omitempty"`
	Expect string      `json:"expect,omitempty"`
	Core   string      `json:"core"`
	Total  int64       `json:"total"`
	Hit    int64       `json:"expected"` // connections with Expect
	Hellos []EdgeHello `json:"hellos"`
}

// EdgeHellos reads what the relays received in runs updated since since,
// biggest counts first; relay "" is every relay.
func (s *Store) EdgeHellos(ctx context.Context, relay string, since time.Time) ([]EdgeNode, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT h.relay, h.cover, h.hk, sum(h.conns)::bigint,
		       coalesce(n.label, ''), coalesce(n.fingerprint, '')
		FROM knob_edge_hellos h
		LEFT JOIN entry_relays r ON r.name = h.relay
		LEFT JOIN entry_nodes n ON n.relay_id = r.id
		WHERE h.updated_at > $1 AND ($2 = '' OR h.relay = $2)
		GROUP BY h.relay, h.cover, h.hk, n.label, n.fingerprint
		ORDER BY h.relay, 4 DESC`, since, relay)
	if err != nil {
		return nil, fmt.Errorf("read edge hellos: %w", err)
	}
	defer rows.Close()
	byRelay := map[string]*EdgeNode{}
	var order []string
	for rows.Next() {
		var h EdgeHello
		var name, label, fp string
		if err := rows.Scan(&name, &h.Cover, &h.HK, &h.Conns, &label, &fp); err != nil {
			return nil, err
		}
		n := byRelay[name]
		if n == nil {
			if fp == "" && label != "" {
				fp = "chrome" // the link's default (DeclaredFor)
			}
			n = &EdgeNode{Relay: name, Profile: label, FP: fp, Expect: releaseHellos[fp], Core: ReleaseCore, Hellos: []EdgeHello{}}
			byRelay[name] = n
			order = append(order, name)
		}
		h.Name = helloName(h.HK)
		n.Hellos = append(n.Hellos, h)
		n.Total += h.Conns
		if h.HK == n.Expect {
			n.Hit += h.Conns
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(order)
	out := make([]EdgeNode, 0, len(order))
	for _, name := range order {
		out = append(out, *byRelay[name])
	}
	return out, nil
}
