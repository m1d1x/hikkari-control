package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/m1d1x/hikkari-control/internal/mailer"
)

// mail-test <to>: one letter straight through the configured provider,
// bypassing the outbox — what proves MAILER_PROVIDER and SMTP_* work before
// a buyer depends on them.
func runMailTest(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "mail-test <to>")
		os.Exit(2)
	}
	provider, sender, err := mailer.FromEnv(os.Getenv)
	if err == nil && provider == nil {
		err = fmt.Errorf("MAILER_PROVIDER is not set")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mail-test:", err)
		os.Exit(1)
	}
	msg, err := mailer.Letter{
		Title: "проверка почты hikkari",
		Lines: []string{"если вы читаете это письмо, хаб умеет отправлять почту."},
		Codes: []string{"https://sub.hikkari.net/sub/example"},
	}.Render(sender, args[0])
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		err = provider.Send(ctx, msg)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mail-test:", err)
		os.Exit(1)
	}
	fmt.Printf("sent to %s via %s from %s\n", args[0], provider.Name(), sender.From)
	os.Exit(0)
}
