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
	if !order && len(args) != 1 && len(args) != 4 && len(args) != 5 {
		fail(fmt.Errorf("usage: cmd/server entry-box <box> [<address> <provider> <location> [rented-until] [voice=<region> | xhttp | xhttp=<domain>]] < password | entry-box order [region|-] [plan|-] [reality|xhttp|voice=<region>]"))
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
			case strings.HasPrefix(p, "voice="):
				spec.Proto, spec.VoiceRegion = entry.ProtoHysteria2, strings.TrimPrefix(p, "voice=")
			default:
				fail(fmt.Errorf("protocol %q: reality, xhttp or voice=<region>", p))
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
