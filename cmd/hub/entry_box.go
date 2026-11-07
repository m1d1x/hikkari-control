package main

// entry-box: provision a relay box from the hub's own shell (roadmap 42
// П6.15) — the same job the admin console starts, run in the foreground with
// its log on stdout. The panel is the everyday way; this is the way when the
// panel is not an option, with the box's SSH key as the root of trust (as
// admin-create).
//
//	cmd/server entry-box <box> <address> <provider> <location> [rented-until] [voice=<region> | xhttp=<domain>]
//	cmd/server entry-box <box>          # run again on a recorded box
//	cmd/server entry-box ssh <box> <command...>
//	                                    # run a command on a box with the
//	                                    # hub's key (stdin passed through)
//	cmd/server entry-box order [region] [plan]
//	                                    # order a Reality machine from the saved
//	                                    # hoster and stand it up (П9.2)
//	cmd/server entry-box ipv6 <box> forget-order
//	                                    # drop the record of an IPv6 order the
//	                                    # hoster does not have, so the next run
//	                                    # of entry-box ipv6 may buy
//	cmd/server entry-box retire <box> [reason]
//	                                    # hand a machine back and delete it at
//	                                    # the hoster that is saved now
//	cmd/server entry-box instances      # every machine at every hoster and
//	                                    # region a box was rented in, against
//	                                    # the hub's record; reads only
//	cmd/server entry-box provider <kind> off
//	cmd/server entry-box provider <kind> <region> <plan> [api-url]
//	                                    # switch a hoster off, or save its
//	                                    # account with the token from stdin
//	cmd/server entry-box hub-key        # the hub's ssh public key, for a
//	                                    # provider's panel (no password then)
//	cmd/server entry-box record <relay> # an alive relay's signed record, as a
//	                                    # client gets it (the live voice harness)
//
// The provider's root password, if the hub key is not on the box yet, is read
// from stdin (one line; empty = none) — never an argument, which would land in
// the shell history and the process table.
//
// voice=<region> makes a voice entry (Hysteria2) in front of that voice
// region's SFUs instead of a Reality stream entry (voice plan Ф3).
//
// `order` is the console's "order a machine" from the same shell: the hoster
// account saved in the console, its default region and plan unless given, and
// the same money caps (Service.order → affordable). The console asks a passkey
// for it because a browser session is not the box; this shell already is, and
// it can provision and retire machines that cost the same money.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/entrywire"
)

func runEntryBox(args []string) {
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "entry-box: %v\n", err)
		os.Exit(1)
	}
	if len(args) == 2 && args[0] == "record" {
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		env, err := svc.RelayEnvelope(ctx, args[1])
		if err != nil {
			fail(err)
		}
		fmt.Println(string(env))
		os.Exit(0)
	}
	if len(args) == 1 && args[0] == "hub-key" {
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		_, pub, err := svc.HubSSHKey(ctx)
		if err != nil {
			fail(err)
		}
		fmt.Println(pub)
		os.Exit(0)
	}
	if len(args) >= 2 && args[0] == "domain" {
		// entry-box domain add <name>  — seed a hand-bought name into the pool
		// entry-box domain list        — the registrar ledger (Ф6)
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		switch {
		case args[1] == "add" && len(args) == 3:
			if err := svc.SeedDomain(ctx, args[2]); err != nil {
				fail(err)
			}
			fmt.Printf("seeded %s into the pool\n", strings.ToLower(args[2]))
		case args[1] == "list" && len(args) == 2:
			ds, err := svc.Domains(ctx, 200)
			if err != nil {
				fail(err)
			}
			for _, d := range ds {
				fmt.Printf("%-28s %-9s %-9s box=%-12s $%0.2f %s\n",
					d.Domain, d.Status, d.Source, d.Box, float64(d.CostCents)/100, d.OrderID)
			}
		default:
			fail(fmt.Errorf("usage: cmd/server entry-box domain add <name> | entry-box domain list"))
		}
		os.Exit(0)
	}
	if (len(args) == 2 || len(args) == 3) && args[0] == "catalog" {
		// entry-box catalog <hoster> [region] — what that account offers and what
		// the hub counts a plan at, before an order names one. Reads only. A
		// region reads that region's plans: OpenStack's differ by region.
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		svc.SetProviderConnector(entrywire.Providers())
		region := ""
		if len(args) == 3 {
			region = args[2]
		}
		cat, err := svc.ProviderCatalogIn(ctx, args[1], region)
		if err != nil {
			fail(err)
		}
		for _, r := range cat.Regions {
			fmt.Printf("region %s\n", r.ID)
		}
		for _, p := range cat.Plans {
			fmt.Printf("plan %s  %d vCPU  %d MB  %d GB  counted at $%0.2f  regions %v\n", p.ID, p.VCPUs, p.RAMMB, p.DiskGB, float64(p.MonthlyCents)/100, p.Regions)
		}
		os.Exit(0)
	}
	if len(args) == 1 && args[0] == "instances" {
		// entry-box instances — the console's orphan check from this shell, at
		// every hoster and region a box was ever rented in. Reads only. A line
		// marked LEAK is a machine the hub wrote off as handed back and the
		// hoster still charges for.
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		svc.SetProviderConnector(entrywire.Providers())
		list, err := svc.HostingInstances(ctx)
		if err != nil {
			fail(err)
		}
		for _, i := range list {
			mark := "ok"
			switch {
			case i.Released:
				mark = "LEAK"
			case i.Box == "":
				mark = "ORPHAN"
			case i.Retired:
				mark = "RETIRED"
			}
			box := i.Box
			if box == "" {
				box = "-"
			}
			fmt.Printf("%-7s %-10s %-10s %-6s %s %s %s %s\n", mark, i.Provider, i.Region, box, i.ID, i.Address, i.Status, i.Label)
		}
		os.Exit(0)
	}
	if len(args) == 3 && args[0] == "ipv6" && args[2] == "forget-order" {
		// The service derives its batch key from the content key.
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		if err := svc.ForgetIPv6Order(ctx, args[1]); err != nil {
			fail(err)
		}
		os.Exit(0)
	}
	if len(args) == 2 && args[0] == "ipv6" {
		// entry-box ipv6 <box> — buys IPv6 for a machine in service and configures
		// it. This spends money and reapplies the machine's network.
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		_, cancel, pool := adminCLIPool()
		cancel() // the order outlives the CLI's 30 s connect budget
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		svc.SetProviderConnector(entrywire.Providers())
		ctx, stop := context.WithTimeout(context.Background(), 6*time.Minute)
		defer stop()
		out, err := entry.NewProvisioner(svc, entry.SSHDialer, entry.DefaultArtifactDir).GiveBoxIPv6(ctx, args[1])
		fmt.Print(out)
		if err != nil {
			fail(err)
		}
		os.Exit(0)
	}
	// hoster=<kind> as the last word of an order: from that enabled hoster, for
	// this one order; the running hub keeps its own (entry.Service.UseHoster).
	namedHoster := ""
	if n := len(args); n > 1 && args[0] == "order" && strings.HasPrefix(args[n-1], "hoster=") {
		namedHoster, args = strings.TrimPrefix(args[n-1], "hoster="), args[:n-1]
	}
	region, domain, xhttp := "", "", false
	if n := len(args); n > 4 {
		last := args[n-1]
		switch {
		case strings.HasPrefix(last, "voice="):
			region, args = strings.TrimPrefix(last, "voice="), args[:n-1]
		case strings.HasPrefix(last, "xhttp="):
			domain, xhttp, args = strings.TrimPrefix(last, "xhttp="), true, args[:n-1]
		case last == "xhttp":
			// Bare xhttp: the hub picks and buys the domain itself (Ф6).
			xhttp, args = true, args[:n-1]
		}
	}
	if len(args) >= 3 && args[0] == "ssh" {
		// entry-box ssh <box> <command...> — the operator's way onto a box
		// only the hub holds a key to (the chaos runs of П9.7).
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		prov := entry.NewProvisioner(svc, entry.SSHDialer, entry.DefaultArtifactDir)
		var stdin io.Reader
		if st, _ := os.Stdin.Stat(); st.Mode()&os.ModeCharDevice == 0 {
			stdin = os.Stdin
		}
		out, err := prov.RunOnBox(ctx, args[1], strings.Join(args[2:], " "), stdin)
		fmt.Print(out)
		if err != nil {
			fail(err)
		}
		os.Exit(0)
	}
	order := len(args) >= 1 && len(args) <= 4 && args[0] == "order"
	retire := len(args) >= 2 && len(args) <= 3 && args[0] == "retire"
	provider := len(args) >= 3 && len(args) <= 5 && args[0] == "provider"
	if !order && !retire && !provider && len(args) != 1 && len(args) != 4 && len(args) != 5 {
		fail(fmt.Errorf("usage: cmd/server entry-box <box> [<address> <provider> <location> [rented-until] [voice=<region> | xhttp | xhttp=<domain>]] < password | entry-box order [region|-] [plan|-] [reality|xhttp|awg|voice=<region>] [hoster=<kind>] | entry-box catalog <hoster>"))
	}
	if err := cryptobox.Configure(os.Getenv); err != nil {
		fail(fmt.Errorf("content key: %w", err))
	}
	signer, err := entry.LoadSigner(os.Getenv, entry.PinnedKeys())
	if err != nil {
		fail(err)
	}
	if signer == nil {
		fail(entry.ErrNoSigner)
	}
	password := ""
	if st, _ := os.Stdin.Stat(); st.Mode()&os.ModeCharDevice == 0 {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		password = strings.TrimRight(line, "\r\n")
	}

	_, cancel, pool := adminCLIPool()
	cancel() // the job outlives the CLI's 30 s connect budget
	defer pool.Close()
	svc, err := entry.NewService(pool, signer, entry.DefaultBatchSize)
	if err != nil {
		fail(err)
	}
	prov := newProvisioner(svc)
	ctx := context.Background()
	// So `entry-box <box> <ip> <provider> <city> xhttp` (no domain) buys and
	// points the domain itself, through the account saved in the console, and
	// a blocked domain is replaced (Ф6).
	// The one-shot CLI never runs a repair pass of its own; it wires the
	// domains so a provisioning job can claim or buy a name (Ф6).
	_ = wireEntryDomains(ctx, svc, prov)
	if namedHoster != "" {
		// The order's region reaches the client: an OpenStack client speaks to
		// one region's endpoints and orders nowhere else.
		orderRegion := ""
		if len(args) > 1 && args[1] != "-" {
			orderRegion = args[1]
		}
		if err := svc.UseHoster(ctx, namedHoster, orderRegion); err != nil {
			fail(err)
		}
	}

	if retire {
		reason := entry.DeathRotated
		if len(args) == 3 {
			reason = args[2]
		}
		if err := retireBox(ctx, svc, args[1], reason); err != nil {
			fail(err)
		}
		os.Exit(0)
	}
	if provider {
		if err := saveHoster(ctx, svc, args[1:], password); err != nil {
			fail(err)
		}
		os.Exit(0)
	}

	var id uuid.UUID
	switch {
	case order:
		// "-" keeps the hoster's default, so a protocol can be named alone:
		// `entry-box order - - xhttp` — what the operator runs when the
		// autopilot has spent its strikes on a pool (repair.go, FailStrikes).
		spec := entry.OrderSpec{}
		if len(args) > 1 && args[1] != "-" {
			spec.Region = args[1]
		}
		if len(args) > 2 && args[2] != "-" {
			spec.Plan = args[2]
		}
		if len(args) > 3 {
			switch p := args[3]; {
			case p == "reality":
			case p == "xhttp":
				spec.Proto = entry.ProtoVlessXhttp
			case p == "awg":
				spec.Proto = entry.ProtoAmneziaWG
			case strings.HasPrefix(p, "voice="):
				spec.Proto, spec.VoiceRegion = entry.ProtoHysteria2, strings.TrimPrefix(p, "voice=")
			default:
				fail(fmt.Errorf("protocol %q: reality, xhttp, awg or voice=<region>", p))
			}
		}
		var name string
		name, id, err = prov.OrderBox(ctx, spec, "")
		if name != "" {
			fmt.Printf("ordered %s\n", name)
		}
	case len(args) == 1:
		id, err = prov.Provision(ctx, args[0], password, "")
	default:
		spec := entry.BoxSpec{Name: args[0], Address: args[1], Provider: args[2], Location: args[3]}
		switch {
		case region != "":
			spec.Proto, spec.Region = entry.ProtoHysteria2, region
		case xhttp:
			// domain may be empty: the hub assigns one during the job (Ф6).
			spec.Proto, spec.Domain = entry.ProtoVlessXhttp, domain
		}
		if len(args) == 5 {
			d, perr := time.Parse(time.DateOnly, args[4])
			if perr != nil {
				fail(fmt.Errorf("rented-until must be YYYY-MM-DD"))
			}
			spec.RentedUntil = &d
		}
		id, err = prov.CreateAndProvision(ctx, entry.NewBox{BoxSpec: spec, Password: password}, "")
	}
	if err != nil {
		fail(err)
	}

	// Follow the job's log until it ends — the same lines the console shows.
	seq := 0
	for {
		job, err := svc.Job(ctx, id)
		if err != nil {
			fail(err)
		}
		lines, err := svc.JobLog(ctx, job.ID, seq)
		if err != nil {
			fail(err)
		}
		for _, l := range lines {
			fmt.Printf("%s  %s\n", l.At.Format("15:04:05"), l.Line)
			seq = l.Seq
		}
		if job.Status != "running" {
			if job.Status == "failed" {
				os.Exit(1)
			}
			os.Exit(0)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// retireBox is the console's handback from this shell. The delete goes to the
// hoster saved now, so a machine must leave before its hoster is switched off.
func retireBox(ctx context.Context, svc *entry.Service, box, reason string) error {
	relays, holders, err := svc.Retire(ctx, box, reason)
	if err != nil {
		return err
	}
	fmt.Printf("retired %s: relays %v, holders told %d\n", box, relays, holders)
	if err := svc.DeleteBoxInstance(ctx, box); err != nil {
		return fmt.Errorf("delete at the hoster: %w", err)
	}
	fmt.Printf("deleted %s at the hoster\n", box)
	return nil
}

// saveHoster switches a hoster off, or saves its account and default order.
func saveHoster(ctx context.Context, svc *entry.Service, args []string, token string) error {
	kind := args[0]
	if len(args) == 2 && args[1] == "off" {
		view, err := svc.SaveProvider(ctx, kind, entry.ProviderUpdate{Enabled: false}, "")
		if err != nil {
			return err
		}
		fmt.Printf("%s enabled=%v\n", kind, view.Enabled)
		return nil
	}
	if len(args) < 3 {
		return fmt.Errorf("usage: cmd/server entry-box provider <kind> off | <kind> <region> <plan> [api-url]")
	}
	if token == "" {
		return fmt.Errorf("the token is read from stdin, one line")
	}
	creds := map[string]string{"api_token": token}
	if len(args) == 4 {
		creds["api_url"] = args[3]
	}
	view, err := svc.SaveProvider(ctx, kind, entry.ProviderUpdate{
		Enabled: true, Credentials: creds, DefaultRegion: args[1], DefaultPlan: args[2],
	}, "")
	if err != nil {
		return err
	}
	fmt.Printf("%s enabled=%v region=%s plan=%s\n", kind, view.Enabled, view.DefaultRegion, view.DefaultPlan)
	return nil
}
