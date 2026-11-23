package entry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const trunkPointTLSEnvKey = "EDGE_HIKKARI_TRUNK_POINT_TLS"

type pointTLSBinding struct {
	Box, Name string
}

func (s *Service) pointTLSBindings(ctx context.Context, core string) ([]pointTLSBinding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT point_box, domain FROM entry_trunk_point_tls_names
		WHERE core_box = $1 ORDER BY point_box`, core)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []pointTLSBinding
	for rows.Next() {
		var point pointTLSBinding
		if err := rows.Scan(&point.Box, &point.Name); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

// SetCorePointTLS records the names served by a core's trunk relay.
func (s *Service) SetCorePointTLS(ctx context.Context, core string, port int, allowLegacy bool, names map[string]string) error {
	if len(names) == 0 || len(names) > trunkPipePointsMax {
		return fmt.Errorf("%w: требуется от 1 до %d имён точек", ErrRejected, trunkPipePointsMax)
	}
	relays, err := s.relayIdentities(ctx, core)
	if err != nil {
		return err
	}
	host := ""
	for _, rel := range relays {
		if h := rel.id.Hikkari; rel.id.Port == port && h != nil && h.TrunkPipeKey != "" {
			host = h.Host
			break
		}
	}
	if host == "" {
		return fmt.Errorf("%w: %s:%d не слушает магистраль hk-exit", ErrRejected, core, port)
	}
	if cert, err := s.receiverCertEnv(ctx, host); err != nil || cert == nil {
		return fmt.Errorf("ядру %s нужен сертификат от hub: %w", host, errors.Join(ErrRejected, err))
	}
	seen := map[string]bool{host: true}
	addresses := map[netip.Addr]bool{}
	for box, name := range names {
		if box == core || seen[name] {
			return fmt.Errorf("%w: повтор имени %q или ядро в списке точек", ErrRejected, name)
		}
		if _, err := s.receiverZone(ctx, name); err != nil {
			return err
		}
		cert, err := s.receiverCertEnv(ctx, name)
		if err != nil || cert == nil {
			return fmt.Errorf("%s: нужен действующий сертификат от hub: %w", name, errors.Join(ErrRejected, err))
		}
		ip, err := s.boxIPv4(ctx, box)
		if err != nil {
			return err
		}
		if addresses[ip] {
			return fmt.Errorf("%w: у точек повторяется адрес %s", ErrRejected, ip)
		}
		seen[name], addresses[ip] = true, true
	}
	return s.writeCorePointTLS(ctx, core, port, allowLegacy, names)
}

func (s *Service) writeCorePointTLS(ctx context.Context, core string, port int, legacy bool, names map[string]string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		INSERT INTO entry_trunk_point_tls (core_box, relay_port, allow_legacy) VALUES ($1, $2, $3)
		ON CONFLICT (core_box) DO UPDATE SET relay_port=$2, allow_legacy=$3`, core, port, legacy); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM entry_trunk_point_tls_names WHERE core_box=$1`, core); err != nil {
		return err
	}
	for box, name := range names {
		if _, err = tx.Exec(ctx, `
			INSERT INTO entry_trunk_point_tls_names (core_box, point_box, domain) VALUES ($1, $2, $3)`, core, box, name); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ClearCorePointTLS removes the listener configuration after callers rolled back.
func (s *Service) ClearCorePointTLS(ctx context.Context, core string) error {
	out, err := s.pool.Exec(ctx, `
		DELETE FROM entry_trunk_point_tls t WHERE core_box=$1 AND NOT EXISTS (
			SELECT 1 FROM entry_trunk_point_tls_uses WHERE core_box=t.core_box)`, core)
	if err != nil {
		return err
	}
	if out.RowsAffected() == 0 {
		return fmt.Errorf("%w: нет настройки на %s или вход ещё пользуется именами точек", ErrRejected, core)
	}
	return nil
}

func (s *Service) corePointTLSEnv(ctx context.Context, core string, port int) (string, error) {
	var cfg struct {
		AllowLegacy bool         `json:"allow_legacy"`
		Points      []coreTLSPEM `json:"points"`
	}
	err := s.pool.QueryRow(ctx, `
		SELECT allow_legacy FROM entry_trunk_point_tls WHERE core_box=$1 AND relay_port=$2`, core, port).Scan(&cfg.AllowLegacy)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	points, err := s.pointTLSBindings(ctx, core)
	if err != nil {
		return "", err
	}
	for _, point := range points {
		ip, err := s.boxIPv4(ctx, point.Box)
		if err != nil {
			return "", err
		}
		cert, err := s.receiverCertEnv(ctx, point.Name)
		if err != nil || cert == nil {
			return "", fmt.Errorf("%s: нужен действующий сертификат: %w", point.Name, errors.Join(ErrRejected, err))
		}
		cfg.Points = append(cfg.Points, coreTLSPEM{ip, point.Name, cert["EDGE_HIKKARI_CERT"], cert["EDGE_HIKKARI_KEY"]})
	}
	raw, err := json.Marshal(cfg)
	return base64.StdEncoding.EncodeToString(raw), err
}

type coreTLSPEM struct {
	IP   netip.Addr `json:"ip"`
	Name string     `json:"name"`
	Cert string     `json:"cert"`
	Key  string     `json:"key"`
}

func (s *Service) pointSNI(ctx context.Context, box, core string, stand bool, boxes, ips []string) (map[string]string, error) {
	var enabled []string
	err := s.pool.QueryRow(ctx, `
		SELECT points FROM entry_trunk_point_tls_uses WHERE entry_box=$1 AND core_box=$2 AND stand=$3`, box, core, stand).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.pointSNIFor(ctx, core, boxes, ips, enabled)
}

func (s *Service) pointTLSWires(ctx context.Context, raw, box, core string, stand bool, boxes, ips []string) (string, error) {
	names, err := s.pointSNI(ctx, box, core, stand, boxes, ips)
	if err != nil {
		return "", err
	}
	return withNamedPoints(raw, ips, names)
}

func withNamedPoints(raw string, ips []string, names map[string]string) (string, error) {
	doc, err := withPoints([]byte(raw), ips)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &fields); err != nil {
		return "", err
	}
	if len(names) == 0 {
		delete(fields, "point_sni")
	} else {
		b, err := json.Marshal(names)
		if err != nil {
			return "", err
		}
		fields["point_sni"] = b
	}
	b, err := json.Marshal(fields)
	return string(b) + "\n", err
}

func (s *Service) pointSNIFor(ctx context.Context, core string, boxes, ips, enabled []string) (map[string]string, error) {
	out := map[string]string{}
	if len(enabled) == 0 {
		return out, nil
	}
	bindings, err := s.pointTLSBindings(ctx, core)
	if err != nil {
		return nil, err
	}
	for i, box := range boxes {
		if !slices.Contains(enabled, box) {
			continue
		}
		at := slices.IndexFunc(bindings, func(b pointTLSBinding) bool { return b.Box == box })
		if at < 0 {
			return nil, fmt.Errorf("%w: %s не имеет TLS-имени на %s", ErrRejected, box, core)
		}
		out[ips[i]] = bindings[at].Name
	}
	return out, nil
}

// SetTrunkPointTLS switches only the named points of one entry's stand or people.
func (p *Provisioner) SetTrunkPointTLS(ctx context.Context, box string, stand bool, enabled []string) error {
	pair, err := p.svc.trunkPairOf(ctx, box)
	if err != nil {
		return err
	}
	core, points, path, write := pair.Exit, pair.Points, trunkEntryPipeWires, trunkEntryWiresScript
	if stand {
		if pair.Pipe == nil {
			return fmt.Errorf("%w: на %s нет стенда", ErrRejected, box)
		}
		core, points, path, write = pair.PipeExit(), pair.PipePoints, trunkPipeWires, trunkPipeWiresScript
	}
	for _, point := range enabled {
		if !slices.Contains(points, point) {
			return fmt.Errorf("%w: %s отсутствует среди точек %s", ErrRejected, point, box)
		}
	}
	ips, err := p.svc.pointAddresses(ctx, points)
	if err != nil {
		return err
	}
	names, err := p.svc.pointSNIFor(ctx, core, points, ips, enabled)
	if err != nil {
		return err
	}
	r, _, err := p.provokeRemote(ctx, box)
	if err != nil {
		return err
	}
	defer r.Close()
	raw, err := r.Run(ctx, "cat "+path, nil)
	if err != nil {
		return err
	}
	file, err := withNamedPoints(raw, ips, names)
	if err != nil {
		return err
	}
	if _, err := r.Run(ctx, write, strings.NewReader(file)); err != nil {
		return err
	}
	if len(enabled) == 0 {
		_, err = p.svc.pool.Exec(ctx, `DELETE FROM entry_trunk_point_tls_uses WHERE entry_box=$1 AND stand=$2`, box, stand)
	} else {
		_, err = p.svc.pool.Exec(ctx, `
			INSERT INTO entry_trunk_point_tls_uses (entry_box, core_box, stand, points) VALUES ($1,$2,$3,$4)
			ON CONFLICT (entry_box, stand) DO UPDATE SET core_box=$2, points=$4`, box, core, stand, enabled)
	}
	return err
}

type pendingPointCertificate struct {
	core, domain string
	issued       time.Time
}

// Retry delivery of an issued certificate until the core's reconfigure succeeds.
func (s *Service) reconfigurePointCerts(ctx context.Context, reconfigure func(context.Context, string) error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.core_box, p.domain, c.issued_at FROM entry_trunk_point_tls_names p
		JOIN entry_receiver_certs c USING (domain) JOIN entry_boxes b ON b.name = p.core_box
		WHERE p.applied_issued_at IS DISTINCT FROM c.issued_at AND b.retired_at IS NULL
		ORDER BY p.core_box, p.domain`)
	if err != nil {
		log.Printf("entry: pending point certificates: %v", err)
		return
	}
	pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pendingPointCertificate, error) {
		var c pendingPointCertificate
		err := row.Scan(&c.core, &c.domain, &c.issued)
		return c, err
	})
	if err != nil {
		log.Printf("entry: pending point certificates: %v", err)
		return
	}
	outcomes := map[string]error{}
	for _, c := range pending {
		outcome, seen := outcomes[c.core]
		if !seen {
			outcome = reconfigure(ctx, c.core)
			outcomes[c.core] = outcome
		}
		if outcome != nil {
			log.Printf("entry: point certificate %s on %s: %v", c.domain, c.core, outcome)
			continue
		}
		if _, err := s.pool.Exec(ctx, `
			UPDATE entry_trunk_point_tls_names p SET applied_issued_at=$3 FROM entry_receiver_certs c
			WHERE p.core_box=$1 AND p.domain=$2 AND c.domain=p.domain AND c.issued_at=$3`, c.core, c.domain, c.issued); err != nil {
			log.Printf("entry: record point certificate %s on %s: %v", c.domain, c.core, err)
		}
	}
}
