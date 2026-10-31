package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/digitalocean"
	"github.com/m1d1x/hikkari-control/internal/entrywire"
	"github.com/m1d1x/hikkari-control/internal/hetzner"
	"github.com/m1d1x/hikkari-control/internal/openstack"
	"github.com/m1d1x/hikkari-control/internal/vdsina"
	"github.com/m1d1x/hikkari-control/internal/vultr"
)

var apiURL = flag.String("api-url", "", "API URL override (optional)")

func main() {
	origEnv := map[string]string{
		"VULTR_API_KEY":      os.Getenv("VULTR_API_KEY"),
		"HETZNER_TOKEN":     os.Getenv("HETZNER_TOKEN"),
		"DIGITALOCEAN_TOKEN": os.Getenv("DIGITALOCEAN_TOKEN"),
		"DO_TOKEN":           os.Getenv("DO_TOKEN"),
		"VDSINA_TOKEN":       os.Getenv("VDSINA_TOKEN"),
		"OPENSTACK_AUTH_URL": os.Getenv("OPENSTACK_AUTH_URL"),
	}
	loadEnv()

	provider := flag.String("provider", "", "Provider kind: 'vdsina', 'vultr', 'hetzner', 'digitalocean', 'openstack'")
	key := flag.String("key", "", "API key / token (universal, works for any provider)")
	token := flag.String("token", "", "API token / key (universal, works for any provider)")

	vultrKey := flag.String("vultr-key", "", "Vultr API key (or set VULTR_API_KEY)")
	hetznerToken := flag.String("hetzner-token", "", "Hetzner Cloud API token (or set HETZNER_TOKEN)")
	doToken := flag.String("do-token", "", "DigitalOcean API token (or set DIGITALOCEAN_TOKEN)")
	vdsinaToken := flag.String("vdsina-token", "", "VDSina API token (or set VDSINA_TOKEN)")

	authURL := flag.String("auth-url", "", "OpenStack Keystone Auth URL (or OPENSTACK_AUTH_URL)")
	appID := flag.String("app-id", "", "OpenStack Application Credential ID (or OPENSTACK_APP_CRED_ID)")
	appSecret := flag.String("app-secret", "", "OpenStack Application Credential Secret (or OPENSTACK_APP_CRED_SECRET)")
	username := flag.String("username", "", "OpenStack Username (or OPENSTACK_USERNAME)")
	password := flag.String("password", "", "OpenStack Password (or OPENSTACK_PASSWORD)")
	projectName := flag.String("project", "", "OpenStack Project Name (or OPENSTACK_PROJECT)")
	region := flag.String("region", "", "OpenStack Region (or OPENSTACK_REGION)")
	flag.Parse()

	prov := strings.ToLower(strings.TrimSpace(*provider))
	if prov == "" {
		if *vultrKey != "" {
			prov = "vultr"
		} else if *hetznerToken != "" {
			prov = "hetzner"
		} else if *doToken != "" {
			prov = "digitalocean"
		} else if *vdsinaToken != "" {
			prov = "vdsina"
		} else if *authURL != "" {
			prov = "openstack"
		} else {
			prov = detectProvider(origEnv)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch prov {
	case "vdsina":
		tok := resolveToken(*vdsinaToken, *key, *token, "VDSINA_TOKEN")
		if tok == "" {
			fmt.Fprintln(os.Stderr, "Error: VDSina token is required (via -token, -key, or VDSINA_TOKEN env)")
			os.Exit(1)
		}
		probeVDSina(ctx, tok, *apiURL)

	case "vultr":
		k := resolveToken(*vultrKey, *key, *token, "VULTR_API_KEY")
		if k == "" {
			fmt.Fprintln(os.Stderr, "Error: Vultr API key is required (via -vultr-key, -key, -token, or VULTR_API_KEY env)")
			os.Exit(1)
		}
		probeVultr(ctx, k)

	case "hetzner", "hcloud":
		tok := resolveToken(*hetznerToken, *key, *token, "HETZNER_TOKEN")
		if tok == "" {
			fmt.Fprintln(os.Stderr, "Error: Hetzner Cloud token is required (via -hetzner-token, -token, -key, or HETZNER_TOKEN env)")
			os.Exit(1)
		}
		probeHetzner(ctx, tok)

	case "digitalocean", "do":
		tok := resolveDOToken(*doToken, *key, *token)
		if tok == "" {
			fmt.Fprintln(os.Stderr, "Error: DigitalOcean token is required (via -do-token, -token, -key, or DIGITALOCEAN_TOKEN env)")
			os.Exit(1)
		}
		probeDigitalOcean(ctx, tok)

	case "openstack", "os":
		url := *authURL
		if url == "" {
			url = os.Getenv("OPENSTACK_AUTH_URL")
		}
		aid := *appID
		if aid == "" {
			aid = os.Getenv("OPENSTACK_APP_CRED_ID")
		}
		asec := *appSecret
		if asec == "" {
			asec = *key
		}
		if asec == "" {
			asec = *token
		}
		if asec == "" {
			asec = os.Getenv("OPENSTACK_APP_CRED_SECRET")
		}
		u := *username
		if u == "" {
			u = os.Getenv("OPENSTACK_USERNAME")
		}
		p := *password
		if p == "" {
			p = os.Getenv("OPENSTACK_PASSWORD")
		}
		proj := *projectName
		if proj == "" {
			proj = os.Getenv("OPENSTACK_PROJECT")
		}
		reg := *region
		if reg == "" {
			reg = os.Getenv("OPENSTACK_REGION")
		}

		if url == "" {
			fmt.Fprintln(os.Stderr, "Error: OpenStack auth-url is required (via -auth-url or OPENSTACK_AUTH_URL env)")
			os.Exit(1)
		}
		cfg := openstack.Config{
			AuthURL:                     url,
			Region:                      reg,
			ApplicationCredentialID:     aid,
			ApplicationCredentialSecret: asec,
			Username:                    u,
			Password:                    p,
			ProjectName:                 proj,
		}
		probeOpenStack(ctx, cfg)

	default:
		fmt.Println("Usage: probe-provider -provider <vdsina|vultr|hetzner|digitalocean|openstack> [flags]")
		fmt.Println("\nExamples:")
		fmt.Println("  # VDSina check (Free / Read-only):")
		fmt.Println("  export VDSINA_TOKEN='your_api_token'")
		fmt.Println("  go run ./cmd/probe-provider -provider vdsina")
		fmt.Println("  # or: go run ./cmd/probe-provider -provider vdsina -token 'your_api_token'")
		fmt.Println("\n  # Vultr check (Free / Read-only):")
		fmt.Println("  export VULTR_API_KEY='your_api_key'")
		fmt.Println("  go run ./cmd/probe-provider -provider vultr")
		fmt.Println("  # or: go run ./cmd/probe-provider -provider vultr -key 'your_api_key'")
		fmt.Println("\n  # Hetzner Cloud check (Free / Read-only):")
		fmt.Println("  export HETZNER_TOKEN='your_api_token'")
		fmt.Println("  go run ./cmd/probe-provider -provider hetzner")
		fmt.Println("  # or: go run ./cmd/probe-provider -provider hetzner -token 'your_api_token'")
		fmt.Println("\n  # DigitalOcean check (Free / Read-only):")
		fmt.Println("  export DIGITALOCEAN_TOKEN='your_api_token'")
		fmt.Println("  go run ./cmd/probe-provider -provider digitalocean")
		fmt.Println("  # or: go run ./cmd/probe-provider -provider digitalocean -token 'your_api_token'")
		fmt.Println("\n  # OpenStack check (Free / Read-only):")
		fmt.Println("  export OPENSTACK_AUTH_URL='https://auth.example.com:5000/v3'")
		fmt.Println("  export OPENSTACK_APP_CRED_ID='app_cred_id'")
		fmt.Println("  export OPENSTACK_APP_CRED_SECRET='app_cred_secret'")
		fmt.Println("  go run ./cmd/probe-provider -provider openstack")
		os.Exit(1)
	}
}

func resolveToken(specificFlag, universalKey, universalToken, envVar string) string {
	if specificFlag != "" {
		return specificFlag
	}
	if universalKey != "" {
		return universalKey
	}
	if universalToken != "" {
		return universalToken
	}
	if envVar != "" {
		return os.Getenv(envVar)
	}
	return ""
}

func resolveDOToken(specificFlag, universalKey, universalToken string) string {
	if tok := resolveToken(specificFlag, universalKey, universalToken, "DIGITALOCEAN_TOKEN"); tok != "" {
		return tok
	}
	return os.Getenv("DO_TOKEN")
}

func detectProvider(origEnv map[string]string) string {
	var shellProv []string
	if origEnv["VULTR_API_KEY"] != "" {
		shellProv = append(shellProv, "vultr")
	}
	if origEnv["HETZNER_TOKEN"] != "" {
		shellProv = append(shellProv, "hetzner")
	}
	if origEnv["DIGITALOCEAN_TOKEN"] != "" || origEnv["DO_TOKEN"] != "" {
		shellProv = append(shellProv, "digitalocean")
	}
	if origEnv["VDSINA_TOKEN"] != "" {
		shellProv = append(shellProv, "vdsina")
	}
	if origEnv["OPENSTACK_AUTH_URL"] != "" {
		shellProv = append(shellProv, "openstack")
	}
	if len(shellProv) == 1 {
		return shellProv[0]
	}
	if len(shellProv) > 1 {
		return ""
	}

	var fileProv []string
	if os.Getenv("VULTR_API_KEY") != "" {
		fileProv = append(fileProv, "vultr")
	}
	if os.Getenv("HETZNER_TOKEN") != "" {
		fileProv = append(fileProv, "hetzner")
	}
	if os.Getenv("DIGITALOCEAN_TOKEN") != "" || os.Getenv("DO_TOKEN") != "" {
		fileProv = append(fileProv, "digitalocean")
	}
	if os.Getenv("VDSINA_TOKEN") != "" {
		fileProv = append(fileProv, "vdsina")
	}
	if os.Getenv("OPENSTACK_AUTH_URL") != "" {
		fileProv = append(fileProv, "openstack")
	}
	if len(fileProv) == 1 {
		return fileProv[0]
	}
	return ""
}

func probeVDSina(ctx context.Context, token, apiURL string) {
	fmt.Println("==> Testing VDSina connection (Read-Only / Free)...")
	var opts []vdsina.Option
	if apiURL != "" {
		opts = append(opts, vdsina.WithBaseURL(apiURL))
	} else if os.Getenv("VDSINA_API_URL") != "" {
		opts = append(opts, vdsina.WithBaseURL(os.Getenv("VDSINA_API_URL")))
	}
	client := vdsina.New(token, opts...)
	hoster := entrywire.VDSinaHoster(client)

	// 1. Account / Balance
	acct, err := hoster.Account(ctx)
	if err != nil {
		fmt.Printf("[-] Account check failed: %v\n", err)
		return
	}
	balStr := "no balance endpoint"
	if acct.BalanceCents != nil {
		balStr = fmt.Sprintf("%.2f ₽ (%d коп.)", float64(*acct.BalanceCents)/100.0, *acct.BalanceCents)
	}
	fmt.Printf("[+] Auth OK! Balance: %s\n", balStr)

	// 2. Regions
	regions, err := hoster.Regions(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list regions: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Datacenters/Regions:\n", len(regions))
		for _, r := range regions {
			if r.Country != "" && r.Continent != "" {
				fmt.Printf("    • [%s] %s (%s, %s)\n", r.ID, r.City, r.Country, r.Continent)
			} else if r.City != "" {
				fmt.Printf("    • [%s] %s\n", r.ID, r.City)
			} else {
				fmt.Printf("    • [%s]\n", r.ID)
			}
		}
	}

	// 3. Plans
	plans, err := hoster.Plans(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list plans: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Server Plans:\n", len(plans))
		limit := len(plans)
		if limit > 5 {
			limit = 5
		}
		for i := 0; i < limit; i++ {
			p := plans[i]
			fmt.Printf("    • Plan ID: %-4s | %d vCPU | %d MB RAM | %d GB Disk | Price: %.2f ₽/mo\n",
				p.ID, p.VCPUs, p.RAMMB, p.DiskGB, float64(p.MonthlyCents)/100.0)
		}
		if len(plans) > limit {
			fmt.Printf("    ... and %d more plans\n", len(plans)-limit)
		}
	}

	// 4. Existing instances
	instances, err := hoster.Instances(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list instances: %v\n", err)
	} else {
		fmt.Printf("[+] Existing servers on account: %d\n", len(instances))
		for _, inst := range instances {
			fmt.Printf("    • [%s] %s | IP: %s | Status: %s\n", inst.ID, inst.Label, inst.Address, inst.Status)
		}
	}
	fmt.Println("\n[SUCCESS] VDSina credentials verified successfully! No money was spent.")
}

func probeVultr(ctx context.Context, apiKey string) {
	fmt.Println("==> Testing Vultr connection (Read-Only / Free)...")
	var opts []vultr.Option
	if *apiURL != "" {
		opts = append(opts, vultr.WithBaseURL(*apiURL))
	} else if u := os.Getenv("VULTR_API_URL"); u != "" {
		opts = append(opts, vultr.WithBaseURL(u))
	}
	client := vultr.New(apiKey, opts...)
	hoster := entrywire.Hoster(client)

	// 1. Account / Balance
	acct, err := hoster.Account(ctx)
	if err != nil {
		fmt.Printf("[-] Account check failed: %v\n", err)
		return
	}
	balStr := "no balance endpoint"
	if acct.BalanceCents != nil {
		balStr = fmt.Sprintf("$%.2f (%d cents)", float64(*acct.BalanceCents)/100.0, *acct.BalanceCents)
	}
	fmt.Printf("[+] Auth OK! Balance: %s\n", balStr)

	// 2. Regions
	regions, err := hoster.Regions(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list regions: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Datacenters/Regions:\n", len(regions))
		for _, r := range regions {
			if r.Country != "" && r.Continent != "" {
				fmt.Printf("    • [%s] %s (%s, %s)\n", r.ID, r.City, r.Country, r.Continent)
			} else if r.City != "" {
				fmt.Printf("    • [%s] %s\n", r.ID, r.City)
			} else {
				fmt.Printf("    • [%s]\n", r.ID)
			}
		}
	}

	// 3. Plans
	plans, err := hoster.Plans(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list plans: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Server Plans:\n", len(plans))
		limit := len(plans)
		if limit > 6 {
			limit = 6
		}
		for i := 0; i < limit; i++ {
			p := plans[i]
			fmt.Printf("    • Plan ID: %-12s | %d vCPU | %d MB RAM | %d GB Disk | Price: $%.2f/mo\n",
				p.ID, p.VCPUs, p.RAMMB, p.DiskGB, float64(p.MonthlyCents)/100.0)
		}
		if len(plans) > limit {
			fmt.Printf("    ... and %d more plans\n", len(plans)-limit)
		}
	}

	// 4. Existing instances
	instances, err := hoster.Instances(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list instances: %v\n", err)
	} else {
		fmt.Printf("[+] Existing servers on account: %d\n", len(instances))
		for _, inst := range instances {
			fmt.Printf("    • [%s] %s | IP: %s | Status: %s\n", inst.ID, inst.Label, inst.Address, inst.Status)
		}
	}
	fmt.Println("\n[SUCCESS] Vultr credentials verified successfully! No money was spent.")
}

func probeHetzner(ctx context.Context, token string) {
	fmt.Println("==> Testing Hetzner Cloud connection (Read-Only / Free)...")
	var opts []hetzner.Option
	if *apiURL != "" {
		opts = append(opts, hetzner.WithBaseURL(*apiURL))
	} else if u := os.Getenv("HETZNER_API_URL"); u != "" {
		opts = append(opts, hetzner.WithBaseURL(u))
	}
	client := hetzner.New(token, opts...)
	hoster := entrywire.HetznerHoster(client)

	// 1. Account / Balance
	acct, err := hoster.Account(ctx)
	if err != nil {
		fmt.Printf("[-] Account check failed: %v\n", err)
		return
	}
	balStr := "no balance endpoint (post-paid / invoice)"
	if acct.BalanceCents != nil {
		balStr = fmt.Sprintf("€%.2f (%d cents)", float64(*acct.BalanceCents)/100.0, *acct.BalanceCents)
	}
	fmt.Printf("[+] Auth OK! Balance: %s\n", balStr)

	// 2. Regions
	regions, err := hoster.Regions(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list regions: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Datacenters/Regions:\n", len(regions))
		for _, r := range regions {
			if r.Country != "" && r.Continent != "" {
				fmt.Printf("    • [%s] %s (%s, %s)\n", r.ID, r.City, r.Country, r.Continent)
			} else if r.City != "" {
				fmt.Printf("    • [%s] %s\n", r.ID, r.City)
			} else {
				fmt.Printf("    • [%s]\n", r.ID)
			}
		}
	}

	// 3. Plans
	plans, err := hoster.Plans(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list plans: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Server Plans:\n", len(plans))
		limit := len(plans)
		if limit > 6 {
			limit = 6
		}
		for i := 0; i < limit; i++ {
			p := plans[i]
			fmt.Printf("    • Plan ID: %-10s | %d vCPU | %d MB RAM | %d GB Disk | Price: €%.2f/mo\n",
				p.ID, p.VCPUs, p.RAMMB, p.DiskGB, float64(p.MonthlyCents)/100.0)
		}
		if len(plans) > limit {
			fmt.Printf("    ... and %d more plans\n", len(plans)-limit)
		}
	}

	// 4. Existing instances
	instances, err := hoster.Instances(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list instances: %v\n", err)
	} else {
		fmt.Printf("[+] Existing servers on account: %d\n", len(instances))
		for _, inst := range instances {
			fmt.Printf("    • [%s] %s | IP: %s | Status: %s\n", inst.ID, inst.Label, inst.Address, inst.Status)
		}
	}
	fmt.Println("\n[SUCCESS] Hetzner Cloud credentials verified successfully! No money was spent.")
}

func probeDigitalOcean(ctx context.Context, token string) {
	fmt.Println("==> Testing DigitalOcean connection (Read-Only / Free)...")
	var opts []digitalocean.Option
	if *apiURL != "" {
		opts = append(opts, digitalocean.WithBaseURL(*apiURL))
	} else if u := os.Getenv("DIGITALOCEAN_API_URL"); u != "" {
		opts = append(opts, digitalocean.WithBaseURL(u))
	}
	client := digitalocean.New(token, opts...)
	hoster := entrywire.DigitalOceanHoster(client)

	// 1. Account / Balance
	acct, err := hoster.Account(ctx)
	if err != nil {
		fmt.Printf("[-] Account check failed: %v\n", err)
		return
	}
	balStr := "no balance endpoint"
	if acct.BalanceCents != nil {
		balStr = fmt.Sprintf("$%.2f (%d cents)", float64(*acct.BalanceCents)/100.0, *acct.BalanceCents)
	}
	fmt.Printf("[+] Auth OK! Balance: %s\n", balStr)

	// 2. Regions
	regions, err := hoster.Regions(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list regions: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Datacenters/Regions:\n", len(regions))
		for _, r := range regions {
			if r.Country != "" && r.Continent != "" {
				fmt.Printf("    • [%s] %s (%s, %s)\n", r.ID, r.City, r.Country, r.Continent)
			} else if r.City != "" {
				fmt.Printf("    • [%s] %s\n", r.ID, r.City)
			} else {
				fmt.Printf("    • [%s]\n", r.ID)
			}
		}
	}

	// 3. Plans
	plans, err := hoster.Plans(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list plans: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Server Plans:\n", len(plans))
		limit := len(plans)
		if limit > 6 {
			limit = 6
		}
		for i := 0; i < limit; i++ {
			p := plans[i]
			fmt.Printf("    • Plan ID: %-14s | %d vCPU | %d MB RAM | %d GB Disk | Price: $%.2f/mo\n",
				p.ID, p.VCPUs, p.RAMMB, p.DiskGB, float64(p.MonthlyCents)/100.0)
		}
		if len(plans) > limit {
			fmt.Printf("    ... and %d more plans\n", len(plans)-limit)
		}
	}

	// 4. Existing instances
	instances, err := hoster.Instances(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list instances: %v\n", err)
	} else {
		fmt.Printf("[+] Existing servers on account: %d\n", len(instances))
		for _, inst := range instances {
			fmt.Printf("    • [%s] %s | IP: %s | Status: %s\n", inst.ID, inst.Label, inst.Address, inst.Status)
		}
	}
	fmt.Println("\n[SUCCESS] DigitalOcean credentials verified successfully! No money was spent.")
}

func probeOpenStack(ctx context.Context, cfg openstack.Config) {
	fmt.Println("==> Testing OpenStack connection (Read-Only / Free)...")
	client := openstack.New(cfg)
	hoster := entrywire.OpenStackHoster(client)

	// 1. Auth check
	_, err := hoster.Account(ctx)
	if err != nil {
		fmt.Printf("[-] Auth / Keystone token failed: %v\n", err)
		return
	}
	fmt.Println("[+] Auth OK! Keystone v3 token granted and catalog discovered.")

	// 2. Regions
	regions, err := hoster.Regions(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list regions: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Regions in catalog:\n", len(regions))
		for _, r := range regions {
			fmt.Printf("    • [%s]\n", r.ID)
		}
	}

	// 3. Flavors / Plans
	plans, err := hoster.Plans(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list flavors: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Flavors (Nova):\n", len(plans))
		limit := len(plans)
		if limit > 6 {
			limit = 6
		}
		for i := 0; i < limit; i++ {
			p := plans[i]
			fmt.Printf("    • Flavor ID: %-12s | %d vCPU | %d MB RAM | %d GB Disk\n",
				p.ID, p.VCPUs, p.RAMMB, p.DiskGB)
		}
		if len(plans) > limit {
			fmt.Printf("    ... and %d more flavors\n", len(plans)-limit)
		}
	}

	// 4. Networks (Neutron)
	nets, err := client.Networks(ctx)
	if err != nil {
		fmt.Printf("[-] Neutron networks: %v\n", err)
	} else {
		fmt.Printf("[+] Discovered %d Networks (Neutron):\n", len(nets))
		for _, n := range nets {
			ext := ""
			if n.External {
				ext = " [external/floating]"
			}
			fmt.Printf("    • [%s] %s (status: %s)%s\n", n.ID, n.Name, n.Status, ext)
		}
	}

	// 5. Existing instances
	instances, err := hoster.Instances(ctx)
	if err != nil {
		fmt.Printf("[-] Failed to list instances: %v\n", err)
	} else {
		fmt.Printf("[+] Existing servers on account: %d\n", len(instances))
		for _, inst := range instances {
			fmt.Printf("    • [%s] %s | IP: %s | Status: %s\n", inst.ID, inst.Label, inst.Address, inst.Status)
		}
	}
	fmt.Println("\n[SUCCESS] OpenStack credentials verified successfully! No money was spent.")
}

func loadEnv() {
	raw, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
			if os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
}
