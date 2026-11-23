package subscription

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type countryPorts struct {
	de         int
	relay, sni string
}

// Both cores must accept the grants of the relay encoded in the existing
// cascade profile. A target record signed for a hidden core is never needed.
const countryProfileRoute = `
	FROM entry_country_egress e JOIN trunk_entries t USING (entry_box)
	JOIN entry_relays nl ON nl.box = t.exit_box AND nl.status = 'alive'
	JOIN entry_relays de ON de.box = e.de_box AND de.status = 'alive'
	JOIN entry_relays src ON src.id = nl.keys_from AND src.id = de.keys_from
	WHERE c.entry_box = e.entry_box AND c.entry_port = e.nl_port AND c.exit_box = src.box
	AND e.applied_sha <> '' AND e.sni <> ''`

func (s *Service) countryPortsOf(ctx context.Context, account uuid.UUID) (countryPorts, error) {
	var ports countryPorts
	err := s.pool.QueryRow(ctx, `
		SELECT e.de_port, src.name, e.sni FROM entry_cascades c
		JOIN entry_relays src ON src.id = c.country_relay
		JOIN entry_country_egress e ON e.entry_box = c.entry_box AND e.nl_port = c.entry_port
		WHERE c.account_id = $1 AND e.applied_sha <> ''`, account).Scan(&ports.de, &ports.relay, &ports.sni)
	if errors.Is(err, pgx.ErrNoRows) {
		return countryPorts{}, nil
	}
	return ports, err
}

// SetCountryProfiles enables only a ready route for the named account.
func (s *Service) SetCountryProfiles(ctx context.Context, name string, on bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE entry_cascades c SET country_relay = CASE WHEN $2 THEN (SELECT DISTINCT src.id `+countryProfileRoute+`) END
		FROM hub_accounts a WHERE a.id = c.account_id AND a.name = $1 AND
		(NOT $2 OR EXISTS (SELECT 1 `+countryProfileRoute+`))`, name, on)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w %q: нужен каскад с готовыми странами и общим источником ключей", ErrName, name)
	}
	return nil
}

func (p countryPorts) links(nl string) []string {
	if p.de == 0 {
		return []string{nl}
	}
	u, err := url.Parse(nl)
	if err != nil {
		return []string{nl}
	}
	host := u.Hostname()
	q := u.Query()
	q.Set("sni", p.sni)
	q.Set("egress", "nl")
	u.RawQuery = q.Encode()
	nl = u.String()
	u.Host = net.JoinHostPort(host, strconv.Itoa(p.de))
	u.Fragment = "🇩🇪 Германия"
	q.Set("egress", "de")
	u.RawQuery = q.Encode()
	return []string{nl, u.String()}
}

// Country status is an authenticated, cheap read, independent of grants and
// subscription refresh pacing. An expired sample leaves the status unknown.
func (s *Service) serveCountryStatus(w http.ResponseWriter, r *http.Request, account uuid.UUID) {
	var down string
	err := s.pool.QueryRow(r.Context(), `
        SELECT e.down_country FROM entry_cascades c
        JOIN entry_country_egress e ON e.entry_box = c.entry_box AND e.nl_port = c.entry_port
        WHERE c.account_id = $1 AND c.country_relay IS NOT NULL
            AND e.applied_sha <> '' AND e.health_at > now() - interval '15 seconds'
            AND e.down_country IS NOT NULL`, account).Scan(&down)
	if err == nil {
		w.Header().Set("Hikkari-Country-Down", down)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
