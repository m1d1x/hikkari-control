// Package mailer sends the hub's letters: the subscription link after a
// payment, the renewal receipt, the link again on request (internal/billing).
//
// A letter is queued in Postgres inside the transaction of the act that
// sends it (Queue), and a worker (Run) delivers it through the Provider with
// retries — a payment is never rolled back over a mail relay's mood, and a
// letter is never lost to a restart. What the letter carries (a subscription
// link is a credential) is sealed at rest with the content key.
//
// The Provider is the one seam to a mail service: SMTP today (smtp.bz), an
// HTTP API (Unisender Go) tomorrow is one more implementation and one case
// in FromEnv. Carried over from Nekko's internal/mailer.
package mailer

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"
)

// Provider delivers one message. Implementations are safe for concurrent use.
type Provider interface {
	// Name is the provider's id, kept with each sent letter.
	Name() string
	Send(ctx context.Context, msg Message) error
}

// Sender is who the letters are from.
type Sender struct {
	From     string
	FromName string
}

// FromEnv builds the provider MAILER_PROVIDER names:
//
//	""     none: letters stay queued until one is configured
//	"smtp" SMTP_HOST, SMTP_PORT (587 STARTTLS or 465 TLS), SMTP_USER, SMTP_PASSWORD
//
// and the sender, MAIL_FROM and MAIL_FROM_NAME (default "hikkari").
func FromEnv(getenv func(string) string) (Provider, Sender, error) {
	sender := Sender{From: strings.TrimSpace(getenv("MAIL_FROM")), FromName: getenv("MAIL_FROM_NAME")}
	if sender.FromName == "" {
		sender.FromName = "hikkari"
	}
	switch name := getenv("MAILER_PROVIDER"); name {
	case "":
		return nil, sender, nil
	case "smtp":
		if _, err := mail.ParseAddress(sender.From); err != nil {
			return nil, sender, fmt.Errorf("MAIL_FROM %q: %w", sender.From, err)
		}
		port, err := strconv.Atoi(getenv("SMTP_PORT"))
		if err != nil {
			return nil, sender, fmt.Errorf("SMTP_PORT %q: %w", getenv("SMTP_PORT"), err)
		}
		cfg := SMTPConfig{Host: getenv("SMTP_HOST"), Port: port, User: getenv("SMTP_USER"), Password: getenv("SMTP_PASSWORD")}
		if cfg.Host == "" || cfg.User == "" || cfg.Password == "" {
			return nil, sender, errors.New("MAILER_PROVIDER=smtp needs SMTP_HOST, SMTP_USER and SMTP_PASSWORD")
		}
		return NewSMTP(cfg), sender, nil
	default:
		return nil, sender, fmt.Errorf("MAILER_PROVIDER %q: unknown provider (smtp)", name)
	}
}
