package entrywire

import (
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/porkbun"
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
	// kindVultr is the hoster the hub rents relay machines from (П9.2). A
	// second hoster is another case in this file and nothing else — the seam
	// it plugs into is entry.BoxProvider, and no migration knows the name.
	kindVultr = "vultr"
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
			// One field, and it is the secret: Vultr authenticates with a
			// bearer token alone. The masked hint the console shows is then
			// cut from the token's own head and tail (credsHint), because
			// there is no public half to cut it from.
			{Name: "api_key", Label: "api key", Secret: true},
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
	}
	return nil, entry.ErrUnknownProvider
}
