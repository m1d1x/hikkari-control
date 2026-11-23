package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/m1d1x/hikkari-protocol/trunk/pipectl"
)

type trunkPointTLS struct {
	AllowLegacy bool           `json:"allow_legacy"`
	Points      []trunkPointID `json:"points"`
}

type trunkPointID struct {
	IP   netip.Addr `json:"ip"`
	Name string     `json:"name"`
	Cert string     `json:"cert"`
	Key  string     `json:"key"`
}

func parseTrunkPointTLS(raw, coreName string) (*trunkPointTLS, error) {
	if raw == "" {
		return nil, nil
	}
	doc, err := base64.StdEncoding.DecodeString(raw)
	var cfg trunkPointTLS
	if err != nil || json.Unmarshal(doc, &cfg) != nil || len(cfg.Points) < 1 || len(cfg.Points) > pipectl.MaxPoints {
		return nil, errors.New("EDGE_HIKKARI_TRUNK_POINT_TLS: invalid configuration")
	}
	ips := map[netip.Addr]bool{}
	names := map[string]bool{coreName: true}
	keys := map[string]bool{}
	for i := range cfg.Points {
		p := &cfg.Points[i]
		if !p.IP.Is4() || !pipectl.ValidPointSNI(p.Name) || ips[p.IP] || names[p.Name] {
			return nil, fmt.Errorf("trunk point TLS: invalid or repeated identity %s %q", p.IP, p.Name)
		}
		if err := p.parseCertificate(keys); err != nil {
			return nil, err
		}
		ips[p.IP], names[p.Name] = true, true
	}
	return &cfg, nil
}

func (p *trunkPointID) parseCertificate(keys map[string]bool) error {
	get := func(name string) string {
		if name == "EDGE_HIKKARI_CERT" {
			return p.Cert
		}
		return p.Key
	}
	cert, key, err := groupCertFromEnv(get, p.Name)
	if err != nil || cert == "" {
		return fmt.Errorf("trunk point TLS: invalid certificate for %s", p.Name)
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil || pair.Leaf == nil {
		return fmt.Errorf("trunk point TLS: invalid certificate for %s", p.Name)
	}
	leaf := pair.Leaf
	spki := string(leaf.RawSubjectPublicKeyInfo)
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != p.Name || len(leaf.IPAddresses) != 0 || keys[spki] {
		return fmt.Errorf("trunk point TLS: %s needs a single-name certificate and its own key", p.Name)
	}
	p.Cert, p.Key = cert, key
	keys[spki] = true
	return nil
}

func (cfg *trunkPointTLS) connectionPolicies(coreName string) []any {
	policies := make([]any, 0, len(cfg.Points)+1)
	ranges := make([]string, 0, len(cfg.Points))
	for _, p := range cfg.Points {
		ip := p.IP.String() + "/32"
		ranges = append(ranges, ip)
		policies = append(policies, map[string]any{"match": map[string]any{
			"sni": []string{p.Name}, "remote_ip": map[string]any{"ranges": []string{ip}},
		}})
	}
	core := map[string]any{"sni": []string{coreName}}
	if !cfg.AllowLegacy {
		// Keep the core's own name for its direct management probes.
		core["remote_ip"] = map[string]any{"not_ranges": ranges}
	}
	return append(policies, map[string]any{"match": core})
}

func (n naiveConfig) exitPointTLS() (*pipectl.ExitPointTLS, error) {
	if n.PointTLS == nil {
		return nil, nil
	}
	tls := &pipectl.ExitPointTLS{CoreName: n.Host, AllowLegacy: n.PointTLS.AllowLegacy}
	for _, p := range n.PointTLS.Points {
		cfg := naiveConfig{Host: p.Name, Storage: n.Storage, CertPEM: p.Cert, KeyPEM: p.Key}
		cert, key, err := cfg.groupCertFiles()
		if err != nil {
			return nil, fmt.Errorf("trunk point TLS: %s: %w", p.Name, err)
		}
		tls.Points = append(tls.Points, pipectl.ExitPointCertificate{
			BorderPoint: pipectl.BorderPoint{IP: p.IP, SNI: p.Name}, CertFile: cert, KeyFile: key,
		})
	}
	return tls, nil
}
