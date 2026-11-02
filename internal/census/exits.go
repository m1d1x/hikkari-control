package census

// Where a service lets its traffic out. A request through its tunnel reaches the
// hub at a token's address, and the source address is the exit. That says nothing
// about the entry: a cascade enters and leaves in different networks. The address
// recorded belongs to the service's server, and only a token's owner gets a row.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/httputil"
)

// exitBase is the hub's public origin, as a tunnel's exit reaches it.
func exitBase() string { return strings.TrimRight(os.Getenv("CENSUS_EXIT_BASE"), "/") }

// exitToken returns the owner's token, making one on first use.
func (s *Store) exitToken(ctx context.Context, owner string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var token string
	err := s.pool.QueryRow(ctx, `INSERT INTO census_exit_tokens (token, owner) VALUES ($1, $2)
		ON CONFLICT (owner) DO UPDATE SET owner = EXCLUDED.owner RETURNING token`, hex.EncodeToString(b), owner).Scan(&token)
	return token, err
}

// exitURL is empty when the hub has no public origin configured: the protocol
// is still measured, the exit is not.
func (s *Store) exitURL(ctx context.Context, owner string) (string, error) {
	base := exitBase()
	if base == "" {
		return "", nil
	}
	token, err := s.exitToken(ctx, owner)
	if err != nil {
		return "", err
	}
	return base + "/e/" + token, nil
}

// RecordExit notes a request on a token. An unknown token writes nothing.
func (s *Store) RecordExit(ctx context.Context, token, ip string) error {
	var owner string
	err := s.pool.QueryRow(ctx, `SELECT owner FROM census_exit_tokens WHERE token = $1`, token).Scan(&owner)
	if err != nil {
		return err
	}
	if !publicIPv4(ip) {
		return errors.New("not a public ipv4")
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO census_exits (owner, ip) VALUES ($1, $2)
		ON CONFLICT (owner, ip) DO UPDATE SET hits = census_exits.hits + 1, last_seen_at = now()`, owner, ip); err != nil {
		return err
	}
	s.noteNetwork(ctx, ip)
	return nil
}

// Exit is one address a service left from.
type Exit struct {
	IP      string `json:"ip"`
	ASN     int    `json:"asn"`
	Network string `json:"network"`
	Country string `json:"country"`
	Hits    int    `json:"hits"`
}

func (s *Store) exits(ctx context.Context) (map[string][]Exit, error) {
	rows, err := s.pool.Query(ctx, `SELECT x.owner, x.ip, coalesce(n.asn, 0), coalesce(n.as_name, ''), coalesce(n.country, ''), x.hits
		FROM census_exits x LEFT JOIN census_networks n ON n.ip = x.ip ORDER BY x.owner, x.hits DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Exit{}
	for rows.Next() {
		var owner string
		var e Exit
		if err := rows.Scan(&owner, &e.IP, &e.ASN, &e.Network, &e.Country, &e.Hits); err != nil {
			return nil, err
		}
		out[owner] = append(out[owner], e)
	}
	return out, rows.Err()
}

// RegisterPublicRoutes mounts the address a tunnel's exit calls. It takes no
// token of ours beyond the one in the path and answers the same either way.
func RegisterPublicRoutes(mux *http.ServeMux, s *Store) {
	mux.HandleFunc("GET /e/{token}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.RecordExit(r.Context(), r.PathValue("token"), httputil.ClientIP(r)); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("census: exit: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (s *Store) handleExitLink(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	link, err := s.exitURL(r.Context(), "service:"+name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "census exit link failed")
		return
	}
	if link == "" {
		writeErr(w, http.StatusServiceUnavailable, "CENSUS_EXIT_BASE is not set on the hub")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"open_through_the_vpn": link})
}
