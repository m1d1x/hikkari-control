package entry

// traffic.go keeps what each account's message socket carried, and through
// which machine it came (roadmap 42 П9.9): the per-person half of the
// console's traffic figures. The relays' pulse (pulse.go) says how much a box
// carried and nothing about who; this says how much one person moved and
// nothing about what.
//
// # Why the gateway counts, and not the relay
//
// A relay must not know who is on it (plan 42 §0.1) — it keeps no session
// list, and the pulse carries counters only. The gateway is the one place
// where a byte and an account meet anyway: the socket is signed in. So the
// gateway counts the bytes of its own socket and hands the hub the account
// and the peer address; the hub turns the account into the same keyed
// pseudonym entry_reach uses and the address into a box name, and neither the
// account nor the address reaches the dataset.
//
// # How "through a relay" is known
//
// A stream relay forwards the client's TCP to the gateway from the box's own
// address, so a socket whose peer is a box's address came through that box.
// That is read off the hub's own record (the sealed address of every machine
// in service), not believed from the client: a client that lied could only
// move its own row between columns, and it cannot claim a box it did not use.
//
// # What this is not
//
// It is the message socket only. Voice and files take other paths (the SFU,
// the media relay, the CDN) and are not in these rows; the console names that
// rather than presenting the socket as the whole of a person's traffic.

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// doorCacheTTL is how long the address → box map is trusted. A box admitted a
// minute ago is attributed from its next flush on; a minute of its sockets
// landing in the direct column is the whole cost.
const doorCacheTTL = time.Minute

// UserTrafficReport is one flush of one socket, as the gateway made it.
type UserTrafficReport struct {
	Account uuid.UUID
	// PeerIP is the socket's peer as the gateway saw it. Resolved to a box
	// here and dropped.
	PeerIP string
	// BytesUp is what the client sent since the previous flush, BytesDown
	// what it received. Deltas, not totals: the gateway produces them itself
	// and loses nothing between two flushes of one socket.
	BytesUp   int64
	BytesDown int64
}

// UserTrafficRecord is one row of the dataset, as the sink takes it.
type UserTrafficRecord struct {
	TS time.Time
	// Account is the pseudonym, never the account id — the same one
	// entry_reach carries, so the two datasets count the same people.
	Account string
	// Box is the machine the socket came through; empty for a direct one.
	Box       string
	BytesUp   int64
	BytesDown int64
}

// UserTrafficSink takes the rows into the dataset (ClickHouse
// entry_user_traffic). Declared here on the consumer side; nil drops them.
type UserTrafficSink interface {
	InsertEntryUserTraffic(ctx context.Context, rows []UserTrafficRecord) error
}

// SetUserTrafficSink wires the dataset. Called once at startup.
func (s *Service) SetUserTrafficSink(sink UserTrafficSink) { s.traffic = sink }

// doorCache is the few-minute memory of which address is which box.
type doorCache struct {
	mu    sync.Mutex
	at    time.Time
	have  bool
	boxes map[string]string
}

// RecordUserTraffic keeps what sockets carried. A flush that carried nothing
// is not a row.
func (s *Service) RecordUserTraffic(ctx context.Context, reports []UserTrafficReport) error {
	if s.traffic == nil || len(reports) == 0 {
		return nil
	}
	doors, err := s.doorsByAddress(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	rows := make([]UserTrafficRecord, 0, len(reports))
	for _, r := range reports {
		up, down := nonNegative(r.BytesUp), nonNegative(r.BytesDown)
		if up == 0 && down == 0 {
			continue
		}
		rows = append(rows, UserTrafficRecord{
			TS:        now,
			Account:   s.reachPseudonym(r.Account),
			Box:       doors[canonicalIP(r.PeerIP)],
			BytesUp:   up,
			BytesDown: down,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return s.traffic.InsertEntryUserTraffic(ctx, rows)
}

// doorsByAddress maps the address of every machine in service to its name,
// from the cache when it is fresh.
func (s *Service) doorsByAddress(ctx context.Context) (map[string]string, error) {
	s.doors.mu.Lock()
	if s.doors.have && s.now().Sub(s.doors.at) < doorCacheTTL {
		m := s.doors.boxes
		s.doors.mu.Unlock()
		return m, nil
	}
	s.doors.mu.Unlock()

	rows, err := s.pool.Query(ctx, `SELECT name, address_ct FROM entry_boxes WHERE retired_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var name string
		var ct []byte
		if err := rows.Scan(&name, &ct); err != nil {
			return nil, err
		}
		plain, err := cryptobox.Decrypt(ct)
		if err != nil {
			// A seal the key cannot open names no address; that box's
			// sockets read as direct, which understates the relay column
			// rather than inventing it.
			continue
		}
		if ip := canonicalIP(string(plain)); ip != "" {
			m[ip] = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.doors.mu.Lock()
	s.doors.boxes, s.doors.at, s.doors.have = m, s.now(), true
	s.doors.mu.Unlock()
	return m, nil
}

// canonicalIP is an address in the one spelling both sides compare: an IPv4
// mapped into IPv6 is the IPv4. Empty when it is not an address.
func canonicalIP(s string) string {
	ip := net.ParseIP(s)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}
