package downdetector

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Config is the non-secret service configuration (YAML).
type Config struct {
	Listen  string `yaml:"listen"`
	DBPath  string `yaml:"db_path"`
	Secrets string `yaml:"secrets_path"`
	// K is the minimum number of distinct installs for a group to count in a
	// verdict and for an aggregate row to be stored.
	K int `yaml:"k"`
	// TokenRatePerMin limits requests per invite token.
	TokenRatePerMin int `yaml:"token_rate_per_min"`
	// OverviewMaxHours caps GET /v1/overview?hours=.
	OverviewMaxHours int    `yaml:"overview_max_hours"`
	Nodes            []Node `yaml:"nodes"`
}

// Node is one server of the registry the outside prober watches.
type Node struct {
	Name  string `yaml:"name"`
	Host  string `yaml:"host"`
	Port  int    `yaml:"port"`
	Proto string `yaml:"proto"`
	// Slug marks one of our own nodes: its id is n:<slug> instead of the hash.
	Slug string `yaml:"slug,omitempty"`
}

func (n Node) ID() string {
	if n.Slug != "" {
		return NodeID(n.Slug)
	}
	return ServerID(n.Host, n.Port, n.Proto)
}

var reNodeSlug = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	c := Config{
		Listen:           ":8080",
		DBPath:           "data/downdetector.db",
		Secrets:          "secrets/secrets.yaml",
		K:                3,
		TokenRatePerMin:  120,
		OverviewMaxHours: 24 * 30,
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, c.validate()
}

func (c Config) validate() error {
	if c.K < 1 {
		return errors.New("config: k must be >= 1")
	}
	if c.TokenRatePerMin < 1 {
		return errors.New("config: token_rate_per_min must be >= 1")
	}
	seen := map[string]string{}
	for _, n := range c.Nodes {
		if n.Name == "" || n.Host == "" || n.Port < 1 || n.Port > 65535 || !reSlug.MatchString(n.Proto) {
			return fmt.Errorf("config: node %q: name, host, port and proto are required", n.Name)
		}
		if n.Slug != "" && !reNodeSlug.MatchString(n.Slug) {
			return fmt.Errorf("config: node %q: bad slug", n.Name)
		}
		id := n.ID()
		if other, dup := seen[id]; dup {
			return fmt.Errorf("config: nodes %q and %q have the same id", other, n.Name)
		}
		seen[id] = n.Name
	}
	return nil
}

// Targets is the registry as the prober sees it.
func (c Config) Targets() []Target {
	out := make([]Target, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		out = append(out, Target{ID: n.ID(), Name: n.Name, Host: n.Host, Port: n.Port})
	}
	return out
}

// Secrets is the decrypted secrets file (SOPS-encrypted in the repo).
type Secrets struct {
	InviteTokens []string `yaml:"invite_tokens"`
	AdminTokens  []string `yaml:"admin_tokens"`
}

func LoadSecrets(path string) (Secrets, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Secrets{}, err
	}
	var probe struct {
		Sops any `yaml:"sops"`
	}
	if yaml.Unmarshal(raw, &probe) == nil && probe.Sops != nil {
		return Secrets{}, errors.New("secrets: file is still SOPS-encrypted, decrypt it first")
	}
	var s Secrets
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return Secrets{}, fmt.Errorf("secrets: %w", err)
	}
	for _, t := range append(append([]string{}, s.InviteTokens...), s.AdminTokens...) {
		if len(t) < 16 {
			return Secrets{}, errors.New("secrets: every token must be at least 16 characters")
		}
	}
	return s, nil
}
