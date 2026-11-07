package main

import (
	"bufio"
	"context"
	"fmt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"net"
	"os"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

func runEntryVantageAdopt(args []string) {
	fail := func(err error) { fmt.Fprintf(os.Stderr, "entry-vantage-adopt: %v\n", err); os.Exit(1) }
	if len(args) < 2 || len(args) > 3 {
		fail(fmt.Errorf("usage: hub entry-vantage-adopt <slot> <address> [ssh-user] < password"))
	}
	if err := cryptobox.Configure(os.Getenv); err != nil {
		fail(err)
	}
	user := "root"
	if len(args) == 3 {
		user = args[2]
	}
	password := ""
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 65536)
		if scanner.Scan() {
			password = strings.TrimSuffix(scanner.Text(), "\r")
		}
		if scanner.Err() != nil {
			fail(fmt.Errorf("read password stdin failed"))
		}
	}
	_, cancel, pool := adminCLIPool()
	cancel()
	defer pool.Close()
	svc, err := entry.NewService(pool, nil, entry.DefaultBatchSize)
	if err != nil {
		fail(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 35*time.Minute)
	defer stop()
	dial := entry.SSHDialer
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		conn, err := net.DialTimeout("unix", socket, 5*time.Second)
		if err != nil {
			fail(fmt.Errorf("owner agent: connect: %w", err))
		}
		defer conn.Close()
		_, pub, err := svc.HubSSHKey(ctx)
		if err != nil {
			fail(fmt.Errorf("owner agent: hub key: %w", err))
		}
		dial = entry.OwnerBootstrapDialer(entry.SSHDialer, ssh.PublicKeysCallback(agent.NewClient(conn).Signers), pub)
	}
	id, err := newProvisionerWithDialer(svc, dial).AdoptVantage(ctx, args[0], args[1], user, password)
	password = ""
	if err != nil {
		fail(err)
	}
	fmt.Printf("prober job %s\n", id)
	if err = svc.WaitProberJob(ctx, id); err != nil {
		fail(err)
	}
	fmt.Printf("slot %s adopted; current prober confirmed by hub poll\n", args[0])
	os.Exit(0)
}
