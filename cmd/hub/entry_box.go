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
//	                                    # its hoster; a ban reason (ip_banned,
//	                                    # fingerprint, domain_blocked) needs the
//	                                    # prober from Russia to confirm it first
//	cmd/server entry-box instances      # every machine at every hoster and
//	                                    # region a box was rented in, against
//	                                    # the hub's record; reads only
//	cmd/server entry-box provider <kind> off
//	cmd/server entry-box provider <kind> <region> <plan> [api-url]
//	                                    # switch a hoster off, or save its
//	                                    # account with the token from stdin
//	cmd/server entry-box hub-key        # the hub's ssh public key, for a
//	                                    # provider's panel (no password then)
//	cmd/server entry-box provoke <box> [port]
//	                                    # stand up or update the опыт 0
//	                                    # provoke receiver on a recorded box
//	                                    # (internal/entry/provoke.go), 2053 by
//	                                    # default; checks it from the hub
//	cmd/server entry-box provoke-off <box>
//	                                    # take the receiver off the box
//	cmd/server entry-box trunk-exit <box> <port,...>
//	                                    # the box's Hikkari relays become the
//	                                    # exit of the cascade's trunk, reaching
//	                                    # those loopback ports; reconfigures
//	cmd/server entry-box trunk-exit-off <box>
//	cmd/server entry-box trunk-entry <box> <exit-box> <listen=port,...> [members] [hikkari-udp-copies=1|2]
//	                                    # the trunk's entry on a box in Russia,
//	                                    # members by Chromium (internal/entry/trunk.go);
//	                                    # the Hikkari relay's datagrams cross once
//	                                    # (v1.02 copies them itself) unless =2
//	cmd/server entry-box trunk-entry-off <box>
//	cmd/server entry-box trunk-pipe <box> <listen=port,...> [members] [hikkari-udp-copies=1|2] [h3dgram=<port>[:proxy]] [voice=<wire>]
//	                                    # the stand over hk-pipe (the Rust pipe)
//	                                    # beside the box's trunk entry, to its exit,
//	                                    # on ports of its own (internal/entry/trunkpipe.go);
//	                                    # h3dgram= adds Chromium's QUIC wire to its
//	                                    # end on the exit (trunk-h3-exit); voice=
//	                                    # holds voice on that wire, no selector
//	cmd/server entry-box trunk-pipe-off <box>
//	cmd/server entry-box trunk-pipe-wires <box> <udp:2,h3dgram:4|off> [h3dgram=<port>[:proxy]] [voice=<wire>]
//	                                    # the stand's wires and their knobs, a file
//	                                    # its brain follows (no new process)
//	cmd/server entry-box trunk-h3-exit <exit-box> <port>
//	                                    # the exit's end of that wire: hk-exit beside
//	                                    # the relay, on its own UDP port
//	                                    # (internal/entry/trunkh3exit.go)
//	cmd/server entry-box trunk-h3-exit-off <exit-box>
//	cmd/server entry-box trunk-wires <exit-box> udp=20447,quic=20448,salamander=20449|off
//	                                    # the trunk's other wires at the exit
//	                                    # (internal/entry/trunkwires.go): env,
//	                                    # reconfigure (reload, no cut), ufw
//	cmd/server entry-box trunk-entry-wires <entry-box> <exit-box> udp:2,quic:4|off [steer]
//	                                    # the entry's wires and members, a file
//	                                    # the running entry reads (no restart)
//	cmd/server entry-box wire-stand <entry-box> <exit-box> [sni] [bench=<minutes>] [load=<wires>] [steps=<steps>] [cc=<h3 congestion>]
//	                                    # the stand of the trunk's second wire
//	                                    # (4.1): a process per wire on both
//	                                    # boxes, synthetic only (internal/entry/wirestand.go);
//	                                    # bench= adds 12 MiB and a 90 s load per wire
//	cmd/server entry-box wire-stand-off <entry-box> <exit-box>
//	cmd/server entry-box lab-reality <box> [port] [cover]
//	                                    # stand up or update the опыт 0 lab
//	                                    # Reality relay on a recorded box
//	                                    # (internal/entry/lab_reality.go), 443
//	                                    # and www.samsung.com by default;
//	                                    # prints its share link on stdout
//	cmd/server entry-box lab-reality-off <box>
//	                                    # take the lab relay off the box
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
	"strconv"
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
	region, domain, xhttp, naive, hikkari := "", "", false, false, false
	if n := len(args); n > 4 {
		last := args[n-1]
		switch {
		case last == "hikkari":
			// Bare hikkari: Hikkari v1 on a domain from the hub's pool.
			hikkari, args = true, args[:n-1]
		case last == "naive":
			// Bare naive: as an ordered Naive box, the hub takes a domain from
			// its pool during the job.
			naive, args = true, args[:n-1]
		case strings.HasPrefix(last, "voice="):
			region, args = strings.TrimPrefix(last, "voice="), args[:n-1]
		case strings.HasPrefix(last, "xhttp="):
			domain, xhttp, args = strings.TrimPrefix(last, "xhttp="), true, args[:n-1]
		case last == "xhttp":
			// Bare xhttp: the hub picks and buys the domain itself (Ф6).
			xhttp, args = true, args[:n-1]
		}
	}
	if (len(args) == 3 && args[0] == "hello-tap" && (args[2] == "on" || args[2] == "off")) ||
		(len(args) == 2 && args[0] == "reconfigure") {
		// entry-box hello-tap <box> on|off — the box's Reality relays count the
		// ClientHellos that reach them (edge/hellotap.go).
		// entry-box reconfigure <box> — the box's relays rewritten from what the
		// hub holds now (env, an XHTTP relay's nginx front).
		// Both apply it now by one reconfigure job on that box, on the build it
		// runs: drained, reinstalled, admission probe, back in the pool.
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
		box := args[1]
		if args[0] == "hello-tap" {
			if err := svc.SetHelloTap(ctx, box, args[2] == "on"); err != nil {
				fail(err)
			}
			fmt.Printf("hello_tap %s = %s\n", box, args[2])
		}
		// The job drains for up to five minutes and probes after: the
		// shell's 30 s are for the setting, not for this.
		jctx, jcancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer jcancel()
		if err := entry.NewOrchestrator(svc, newProvisioner(svc)).Reconfigure(jctx, box); err != nil {
			fail(fmt.Errorf("reconfigure %s: %w", box, err))
		}
		fmt.Printf("%s reconfigured\n", box)
		os.Exit(0)
	}
	if len(args) == 2 && args[0] == "update" {
		// entry-box update <box> — the box moved to this image's edge build
		// now, by the rollout's own job (drain, swap, probe, watch), without
		// waiting out the evening peak: the owner's command.
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		_, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		jctx, jcancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer jcancel()
		if err := entry.NewOrchestrator(svc, newProvisioner(svc)).UpdateNow(jctx, args[1]); err != nil {
			fail(fmt.Errorf("update %s: %w", args[1], err))
		}
		fmt.Printf("%s updated\n", args[1])
		os.Exit(0)
	}
	if len(args) >= 2 && len(args) <= 4 && (args[0] == "lab-reality" || args[0] == "lab-reality-off") {
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
		if args[0] == "lab-reality-off" {
			if len(args) != 2 {
				fail(fmt.Errorf("usage: cmd/server entry-box lab-reality-off <box>"))
			}
			if err := prov.StopLabReality(ctx, args[1]); err != nil {
				fail(err)
			}
			os.Exit(0)
		}
		port, cover := entry.DefaultLabRealityPort, entry.DefaultLabRealityCover
		if len(args) >= 3 {
			if port, err = strconv.Atoi(args[2]); err != nil {
				fail(fmt.Errorf("port %q", args[2]))
			}
		}
		if len(args) == 4 {
			cover = args[3]
		}
		link, err := prov.StandLabReality(ctx, args[1], port, cover)
		if err != nil {
			fail(err)
		}
		// The link holds the relay's id: stdout only, never the log.
		fmt.Println(link)
		os.Exit(0)
	}
	if (len(args) == 3 && args[0] == "trunk-exit") || (len(args) == 2 && args[0] == "trunk-exit-off") {
		// entry-box trunk-exit <box> <port,...> — the box's Hikkari relays
		// become the exit of the cascade's trunk, reaching those loopback
		// ports (internal/entry/trunk.go); -off takes it away. Applied now by
		// a reconfigure job, on the build the box runs.
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		var ports []int
		if args[0] == "trunk-exit" {
			for _, p := range strings.Split(args[2], ",") {
				n, err := strconv.Atoi(strings.TrimSpace(p))
				if err != nil {
					fail(fmt.Errorf("port %q", p))
				}
				ports = append(ports, n)
			}
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		relays, err := svc.SetTrunkExit(ctx, args[1], ports)
		if err != nil {
			fail(err)
		}
		fmt.Printf("trunk %s: %s\n", map[bool]string{true: "on " + args[2], false: "off"}[len(ports) > 0], strings.Join(relays, ", "))
		jctx, jcancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer jcancel()
		if err := entry.NewOrchestrator(svc, newProvisioner(svc)).Reconfigure(jctx, args[1]); err != nil {
			fail(fmt.Errorf("reconfigure %s: %w", args[1], err))
		}
		fmt.Printf("%s reconfigured\n", args[1])
		os.Exit(0)
	}
	if len(args) == 3 && args[0] == "trunk-wires" {
		// entry-box trunk-wires <exit-box> kind=port,...|off — the exit's
		// relay with a trunk serves those wires (internal/entry/trunkwires.go).
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		wires, err := entry.ParseTrunkWires(args[2])
		if err != nil {
			fail(err)
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		before, _ := svc.TrunkWiresOf(ctx, args[1])
		relays, err := svc.SetTrunkWires(ctx, args[1], wires)
		if err != nil {
			fail(err)
		}
		fmt.Printf("trunk wires %s: %s\n", args[2], strings.Join(relays, ", "))
		prov := newProvisioner(svc)
		jctx, jcancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer jcancel()
		if err := prov.OpenTrunkWirePorts(jctx, args[1], wires, before); err != nil {
			fail(err)
		}
		if err := entry.NewOrchestrator(svc, prov).Reconfigure(jctx, args[1]); err != nil {
			fail(fmt.Errorf("reconfigure %s: %w", args[1], err))
		}
		fmt.Printf("%s reconfigured\n", args[1])
		os.Exit(0)
	}
	if (len(args) == 4 || len(args) == 5) && args[0] == "trunk-entry-wires" {
		// entry-box trunk-entry-wires <entry-box> <exit-box> kind:members,...|off [steer]
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		members, err := entry.ParseTrunkEntryWires(args[3])
		if err != nil {
			fail(err)
		}
		steer := false
		if len(args) == 5 {
			if args[4] != "steer" {
				fail(fmt.Errorf("%q: only steer", args[4]))
			}
			steer = true
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		prov := entry.NewProvisioner(svc, entry.SSHDialer, entry.DefaultArtifactDir)
		if err := prov.SetTrunkEntryWires(ctx, args[1], args[2], members, steer); err != nil {
			fail(err)
		}
		fmt.Printf("%s: wires %s, steer=%v (the entry reads them within 10 s)\n", args[1], args[3], steer)
		os.Exit(0)
	}
	if (len(args) == 3 && args[0] == "trunk-h3-exit") || (len(args) == 2 && args[0] == "trunk-h3-exit-off") {
		// entry-box trunk-h3-exit <exit-box> <port> — the exit's end of
		// Chromium's QUIC wire for the stand (internal/entry/trunkh3exit.go, R6.3).
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
		if args[0] == "trunk-h3-exit-off" {
			if err := prov.StopTrunkH3Exit(ctx, args[1]); err != nil {
				fail(err)
			}
			os.Exit(0)
		}
		port, err := strconv.Atoi(args[2])
		if err != nil {
			fail(fmt.Errorf("port %q", args[2]))
		}
		if err := prov.StandTrunkH3Exit(ctx, args[1], port); err != nil {
			fail(err)
		}
		os.Exit(0)
	}
	if (len(args) >= 3 && len(args) <= 7 && args[0] == "trunk-pipe") || (len(args) == 2 && args[0] == "trunk-pipe-off") {
		// entry-box trunk-pipe <box> <listen=port,...> [members] [hikkari-udp-copies=1|2] [h3dgram=<port>[:proxy]]
		// — the stand over hk-pipe beside the box's trunk entry, to its exit
		// (internal/entry/trunkpipe.go, roadmap 4.6, R5).
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
		if args[0] == "trunk-pipe-off" {
			if err := prov.StopTrunkPipe(ctx, args[1]); err != nil {
				fail(err)
			}
			os.Exit(0)
		}
		o := entry.TrunkPipeOptions{Forward: map[int]int{}}
		for _, pair := range strings.Split(args[2], ",") {
			l, r, ok := strings.Cut(pair, "=")
			a, e1 := strconv.Atoi(l)
			b, e2 := strconv.Atoi(r)
			if !ok || e1 != nil || e2 != nil {
				fail(fmt.Errorf("forward %q is not listen=port", pair))
			}
			o.Forward[a] = b
		}
		for _, a := range args[3:] {
			if v, ok := strings.CutPrefix(a, "hikkari-udp-copies="); ok {
				if o.HikkariUDPCopies, err = strconv.Atoi(v); err != nil || o.HikkariUDPCopies < 1 || o.HikkariUDPCopies > 2 {
					fail(fmt.Errorf("hikkari-udp-copies %q: 1 or 2", v))
				}
				continue
			}
			if v, ok := strings.CutPrefix(a, "voice="); ok {
				o.Voice = v
				continue
			}
			if v, ok := strings.CutPrefix(a, "h3dgram="); ok {
				port, usage, _ := strings.Cut(v, ":")
				if o.H3Port, err = strconv.Atoi(port); err != nil {
					fail(fmt.Errorf("h3dgram %q: <port>[:proxy]", v))
				}
				o.H3Usage = usage
				continue
			}
			if o.Members, err = strconv.Atoi(a); err != nil {
				fail(fmt.Errorf("members %q", a))
			}
		}
		if err := prov.StandTrunkPipe(ctx, args[1], o); err != nil {
			fail(err)
		}
		os.Exit(0)
	}
	if len(args) >= 3 && len(args) <= 5 && args[0] == "trunk-pipe-wires" {
		// entry-box trunk-pipe-wires <box> <kind:members,...|off> [h3dgram=<port>[:proxy]] [voice=<wire>]
		// — the stand's wires by its file alone: the brain follows it within
		// 10 s with no new process (internal/entry/trunkpipe.go, roadmap 4.4).
		if err := cryptobox.Configure(os.Getenv); err != nil {
			fail(fmt.Errorf("content key: %w", err))
		}
		members, err := entry.ParseTrunkPipeWires(args[2])
		if err != nil {
			fail(err)
		}
		w := entry.TrunkPipeWires{Members: members}
		for _, a := range args[3:] {
			if v, ok := strings.CutPrefix(a, "voice="); ok {
				w.Voice = v
				continue
			}
			v, ok := strings.CutPrefix(a, "h3dgram=")
			if !ok {
				fail(fmt.Errorf("%q: h3dgram=<port>[:proxy] or voice=<wire>", a))
			}
			port, usage, _ := strings.Cut(v, ":")
			if w.H3Port, err = strconv.Atoi(port); err != nil {
				fail(fmt.Errorf("h3dgram %q: <port>[:proxy]", v))
			}
			w.H3Usage = usage
		}
		ctx, cancel, pool := adminCLIPool()
		defer cancel()
		defer pool.Close()
		svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
		if err != nil {
			fail(err)
		}
		prov := entry.NewProvisioner(svc, entry.SSHDialer, entry.DefaultArtifactDir)
		if err := prov.SetTrunkPipeWires(ctx, args[1], w); err != nil {
			fail(err)
		}
		fmt.Printf("%s: stand wires %s, voice=%s (the brain follows them within 10 s, no new process)\n", args[1], args[2], w.Voice)
		os.Exit(0)
	}
	if (len(args) >= 4 && len(args) <= 6 && args[0] == "trunk-entry") || (len(args) == 2 && args[0] == "trunk-entry-off") {
		// entry-box trunk-entry <box> <exit-box> <listen=port,...> [members] [hikkari-udp-copies=1|2]
		// — the cascade's trunk entry on a box in Russia (internal/entry/trunk.go).
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
		if args[0] == "trunk-entry-off" {
			if err := prov.StopTrunkEntry(ctx, args[1]); err != nil {
				fail(err)
			}
			os.Exit(0)
		}
		o := entry.TrunkEntryOptions{Exit: args[2], Forward: map[int]int{}}
		for _, pair := range strings.Split(args[3], ",") {
			l, r, ok := strings.Cut(pair, "=")
			a, e1 := strconv.Atoi(l)
			b, e2 := strconv.Atoi(r)
			if !ok || e1 != nil || e2 != nil {
				fail(fmt.Errorf("forward %q is not listen=port", pair))
			}
			o.Forward[a] = b
		}
		for _, a := range args[4:] {
			if v, ok := strings.CutPrefix(a, "hikkari-udp-copies="); ok {
				if o.HikkariUDPCopies, err = strconv.Atoi(v); err != nil || o.HikkariUDPCopies < 1 || o.HikkariUDPCopies > 2 {
					fail(fmt.Errorf("hikkari-udp-copies %q: 1 or 2", v))
				}
				continue
			}
			if o.Members, err = strconv.Atoi(a); err != nil {
				fail(fmt.Errorf("members %q", a))
			}
		}
		if err := prov.StandTrunkEntry(ctx, args[1], o); err != nil {
			fail(err)
		}
		os.Exit(0)
	}
	if len(args) >= 2 && len(args) <= 3 && (args[0] == "provoke" || args[0] == "provoke-off") {
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
		if args[0] == "provoke-off" {
			if len(args) != 2 {
				fail(fmt.Errorf("usage: cmd/server entry-box provoke-off <box>"))
			}
			err = prov.StopProvoke(ctx, args[1])
		} else {
			port := entry.DefaultProvokePort
			if len(args) == 3 {
				if port, err = strconv.Atoi(args[2]); err != nil {
					fail(fmt.Errorf("port %q", args[2]))
				}
			}
			err = prov.StandProvoke(ctx, args[1], port)
		}
		if err != nil {
			fail(err)
		}
		os.Exit(0)
	}
	if (len(args) >= 3 && len(args) <= 8 && args[0] == "wire-stand") || (len(args) == 3 && args[0] == "wire-stand-off") {
		// entry-box wire-stand <entry-box> <exit-box> [sni] — the stand of the
		// trunk's second wire on both boxes (internal/entry/wirestand.go);
		// -off takes it away. Neither the entry nor the exit relay restarts.
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
		if args[0] == "wire-stand-off" {
			err = prov.StopWireStand(ctx, args[1], args[2])
		} else {
			sni, bench := entry.DefaultWireStandSNI, time.Duration(0)
			var load *entry.WireLoad
			steps, cc := entry.DefaultWireLoadSteps, ""
			for _, a := range args[3:] {
				if m, ok := strings.CutPrefix(a, "load="); ok {
					load = &entry.WireLoad{Wires: strings.Split(m, ",")}
					continue
				}
				if m, ok := strings.CutPrefix(a, "steps="); ok {
					steps = strings.Split(m, ",")
					continue
				}
				if m, ok := strings.CutPrefix(a, "cc="); ok {
					cc = m
					continue
				}
				if m, ok := strings.CutPrefix(a, "bench="); ok {
					n, err := strconv.Atoi(m)
					if err != nil || n < 10 || n > 24*60 {
						fail(fmt.Errorf("bench=%q: minutes, 10 to 1440", m))
					}
					bench = time.Duration(n) * time.Minute
					continue
				}
				sni = a
			}
			if load != nil {
				load.Steps, load.CC = steps, cc
			}
			err = prov.StandWireStand(ctx, args[1], args[2], sni, bench, load)
		}
		if err != nil {
			fail(err)
		}
		os.Exit(0)
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
	adoptProto := ""
	if len(args) == 3 && (args[0] == "adopt-naive" || args[0] == "adopt-hikkari") {
		adoptProto = strings.TrimPrefix(args[0], "adopt-")
	}
	naiveAdopt := adoptProto != ""
	provider := len(args) >= 3 && len(args) <= 5 && args[0] == "provider"
	if !naiveAdopt && !order && !retire && !provider && len(args) != 1 && len(args) != 4 && len(args) != 5 {
		fail(fmt.Errorf("usage: cmd/server entry-box <box> [<address> <provider> <location> [rented-until] [voice=<region> | xhttp | xhttp=<domain> | naive | hikkari]] < password | entry-box order [region|-] [plan|-] [reality|xhttp|naive|hikkari|awg|voice=<region>] [hoster=<kind>] | entry-box adopt-naive|adopt-hikkari <box> <domain> | entry-box catalog <hoster>"))
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
	case naiveAdopt:
		id, err = prov.Adopt(ctx, args[1], adoptProto, args[2], "")
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
			case p == "hikkari":
				spec.Proto = entry.ProtoHikkari
			case p == "naive":
				spec.Proto = entry.ProtoNaive
			case p == "xhttp":
				spec.Proto = entry.ProtoVlessXhttp
			case p == "awg":
				spec.Proto = entry.ProtoAmneziaWG
			case strings.HasPrefix(p, "voice="):
				spec.Proto, spec.VoiceRegion = entry.ProtoHysteria2, strings.TrimPrefix(p, "voice=")
			default:
				fail(fmt.Errorf("protocol %q: reality, xhttp, naive, hikkari, awg or voice=<region>", p))
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
		case hikkari:
			spec.Proto = entry.ProtoHikkari
		case naive:
			spec.Proto = entry.ProtoNaive
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
	// A ban is the prober's call, not only the testers' (eun16/eun17, 02.11).
	proof, err := svc.ProveCensorDeath(ctx, box, reason)
	if err != nil {
		return err
	}
	if proof != nil {
		fmt.Printf("vantage: %v\n", proof)
	}
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
