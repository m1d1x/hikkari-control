package entrywire

import (
	"github.com/m1d1x/hikkari-control/internal/digitalocean"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/hetzner"
	"github.com/m1d1x/hikkari-control/internal/openstack"
	"github.com/m1d1x/hikkari-control/internal/porkbun"
	"github.com/m1d1x/hikkari-control/internal/vdsina"
	"github.com/m1d1x/hikkari-control/internal/vultr"
)

// Providers is the hub's catalog of provider adapters (roadmap 42 П9.1): what
// each kind is called, what it does, which credential fields the console asks
// for, and how saved credentials become a live client. It is the one place
// that knows both the entry package's types and the provider clients.
func Providers() entry.ProviderConnector { return providers{} }

type providers struct{}

const (
	// kindPorkbun is the registrar the hub buys relay domains through.
	kindPorkbun = "porkbun"
	// kindVultr is the hoster the hub rents relay machines from (П9.2).
	kindVultr = "vultr"
	// kindVDSina is VDSina.
	kindVDSina = "vdsina"
	// kindHetzner is Hetzner Cloud.
	kindHetzner = "hetzner"
	// kindDigitalOcean is DigitalOcean.
	kindDigitalOcean = "digitalocean"
	// kindOpenStack is OpenStack.
	kindOpenStack = "openstack"
)

func (providers) Kinds() []entry.ProviderKind {
	return []entry.ProviderKind{{
		Kind:  kindPorkbun,
		Title: "Porkbun",
		Role:  entry.RoleRegistrar,
		Fields: []entry.CredentialField{
			{Name: "api_key", Label: "api key (pk1_…)"},
			{Name: "secret_api_key", Label: "secret key (sk1_…)", Secret: true},
		},
	}, {
		Kind:  kindVultr,
		Title: "Vultr",
		Role:  entry.RoleHosting,
		Fields: []entry.CredentialField{
			{Name: "api_key", Label: "api key", Secret: true},
		},
	}, {
		Kind:  kindVDSina,
		Title: "VDSina",
		Role:  entry.RoleHosting,
		Fields: []entry.CredentialField{
			{Name: "api_token", Label: "api token", Secret: true},
			{Name: "api_url", Label: "api url (optional)", Optional: true},
		},
	}, {
		Kind:  kindHetzner,
		Title: "Hetzner Cloud",
		Role:  entry.RoleHosting,
		Fields: []entry.CredentialField{
			{Name: "api_token", Label: "api token", Secret: true},
		},
	}, {
		Kind:  kindDigitalOcean,
		Title: "DigitalOcean",
		Role:  entry.RoleHosting,
		Fields: []entry.CredentialField{
			{Name: "api_token", Label: "api token", Secret: true},
		},
	}, {
		Kind:  kindOpenStack,
		Title: "OpenStack",
		Role:  entry.RoleHosting,
		Fields: []entry.CredentialField{
			{Name: "auth_url", Label: "auth url (https://.../v3)"},
			{Name: "username", Label: "username (or app cred id)"},
			{Name: "password", Label: "password (or app cred secret)", Secret: true},
			{Name: "region", Label: "region (e.g. eu-north2)", Optional: true},
			{Name: "project", Label: "project name (optional)", Optional: true},
			{Name: "network_id", Label: "network id (optional)", Optional: true},
		},
	}}
}

func (providers) Registrar(kind string, creds map[string]string) (entry.DomainRegistrar, error) {
	switch kind {
	case kindPorkbun:
		return Registrar(porkbun.New(creds["api_key"], creds["secret_api_key"])), nil
	}
	return nil, entry.ErrUnknownProvider
}

func (providers) Hosting(kind string, creds map[string]string) (entry.BoxProvider, error) {
	switch kind {
	case kindVultr:
		return Hoster(vultr.New(creds["api_key"])), nil
	case kindVDSina:
		tok := creds["api_token"]
		if tok == "" {
			tok = creds["api_key"]
		}
		var opts []vdsina.Option
		if creds["api_url"] != "" {
			opts = append(opts, vdsina.WithBaseURL(creds["api_url"]))
		}
		return VDSinaHoster(vdsina.New(tok, opts...)), nil
	case kindHetzner:
		tok := creds["api_token"]
		if tok == "" {
			tok = creds["api_key"]
		}
		return HetznerHoster(hetzner.New(tok)), nil
	case kindDigitalOcean:
		tok := creds["api_token"]
		if tok == "" {
			tok = creds["api_key"]
		}
		return DigitalOceanHoster(digitalocean.New(tok)), nil
	case kindOpenStack:
		cfg := openstack.Config{
			AuthURL:   creds["auth_url"],
			Region:    creds["region"],
			NetworkID: creds["network_id"],
		}
		user := creds["username"]
		if user == "" {
			user = creds["app_cred_id"]
		}
		pass := creds["password"]
		if pass == "" {
			pass = creds["app_cred_secret"]
		}
		proj := creds["project"]
		if proj == "" {
			proj = creds["project_name"]
		}
		cfg.Username = user
		cfg.Password = pass
		cfg.ProjectName = proj
		cfg.ApplicationCredentialID = creds["app_cred_id"]
		cfg.ApplicationCredentialSecret = creds["app_cred_secret"]
		return OpenStackHoster(openstack.New(cfg)), nil
	}
	return nil, entry.ErrUnknownProvider
}
