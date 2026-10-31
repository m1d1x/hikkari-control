package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// SMTPConfig is one relay. Port 465 speaks TLS from the first byte; any other
// port must offer STARTTLS — credentials never cross in clear.
type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
}

// SMTP sends through a relay over a fresh connection per message: the relay
// keeps no session worth reusing, and one-shot connections never go stale.
type SMTP struct {
	cfg     SMTPConfig
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	timeout time.Duration
}

func NewSMTP(cfg SMTPConfig) *SMTP {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &SMTP{cfg: cfg, dial: d.DialContext, timeout: 30 * time.Second}
}

func (*SMTP) Name() string { return "smtp" }

// Send performs (TLS or STARTTLS) + AUTH + MAIL/RCPT/DATA within one
// deadline. The error names the step that failed (dial, STARTTLS, AUTH,
// RCPT…), which is what triage needs.
func (s *SMTP) Send(ctx context.Context, msg Message) error {
	body, err := msg.BuildMIME()
	if err != nil {
		return fmt.Errorf("mailer: build mime: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	conn, err := s.dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mailer: dial %s: %w", addr, err)
	}
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	if s.cfg.Port == 465 {
		conn = tls.Client(conn, tlsCfg)
	}
	// A slow relay yields when the deadline fires: smtp.Client calls do not
	// watch ctx themselves.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer: smtp handshake: %w", err)
	}
	defer func() { _ = client.Quit() }()
	if err := client.Hello(heloName(msg.From)); err != nil {
		return fmt.Errorf("mailer: EHLO: %w", err)
	}
	if s.cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("mailer: relay does not offer STARTTLS — refusing to send credentials in clear")
		}
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("mailer: STARTTLS: %w", err)
		}
	}
	if err := client.Auth(smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)); err != nil {
		return fmt.Errorf("mailer: AUTH: %w", err)
	}
	if err := client.Mail(msg.From); err != nil {
		return fmt.Errorf("mailer: MAIL FROM %s: %w", msg.From, err)
	}
	if err := client.Rcpt(msg.To); err != nil {
		return fmt.Errorf("mailer: RCPT TO: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mailer: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return fmt.Errorf("mailer: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: close DATA: %w", err)
	}
	return nil
}

// heloName is the domain of from, for EHLO.
func heloName(from string) string {
	for i := len(from) - 1; i >= 0; i-- {
		if from[i] == '@' {
			return from[i+1:]
		}
	}
	return "localhost"
}
