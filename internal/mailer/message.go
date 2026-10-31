package mailer

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// Message is one outbound email. From/To are RFC 5322 addr-specs (optionally
// with display-name when assembled by the Service); body parts are stored as
// raw UTF-8 strings — encoding to quoted-printable happens in BuildMIME.
type Message struct {
	From     string // bare email address ("no-reply@hikkari.net")
	FromName string // optional display name ("hikkari")
	To       string // bare recipient address
	ToName   string // optional recipient display name
	Subject  string // raw UTF-8; will be Q-encoded into headers
	TextBody string // required (anti-spam: drops Outlook/Gmail score)
	HTMLBody string // optional
}

// Validate sanity-checks the message before we open an SMTP connection.
// Returns the first failure — caller can surface it directly.
func (m *Message) Validate() error {
	if m == nil {
		return errors.New("nil message")
	}
	if _, err := mail.ParseAddress(m.From); err != nil {
		return fmt.Errorf("invalid From %q: %w", m.From, err)
	}
	if _, err := mail.ParseAddress(m.To); err != nil {
		return fmt.Errorf("invalid To %q: %w", m.To, err)
	}
	if strings.TrimSpace(m.Subject) == "" {
		return errors.New("empty Subject")
	}
	if strings.TrimSpace(m.TextBody) == "" {
		return errors.New("empty TextBody (multipart/alternative requires the text part)")
	}
	return nil
}

// BuildMIME produces the wire-format bytes of the message: headers + body
// (multipart/alternative when HTMLBody is non-empty, text/plain otherwise).
// Subject is RFC 2047 Q-encoded so non-ASCII (Cyrillic) survives transport.
// Body parts are quoted-printable encoded.
//
// Lines use CRLF as required by SMTP. The final terminating dot is added by
// net/smtp when we Write, not here.
func (m *Message) BuildMIME() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	// ── Headers ──────────────────────────────────────────────────────
	buf.WriteString("From: ")
	buf.WriteString(formatAddress(m.From, m.FromName))
	buf.WriteString("\r\n")

	buf.WriteString("To: ")
	buf.WriteString(formatAddress(m.To, m.ToName))
	buf.WriteString("\r\n")

	buf.WriteString("Subject: ")
	buf.WriteString(mime.QEncoding.Encode("utf-8", m.Subject))
	buf.WriteString("\r\n")

	buf.WriteString("Date: ")
	buf.WriteString(time.Now().UTC().Format(time.RFC1123Z))
	buf.WriteString("\r\n")

	// Local Message-ID — the relay may append its own, but ours gives
	// threading and our own logs a stable reference for the original send.
	mid, err := generateMessageID(m.From)
	if err != nil {
		return nil, fmt.Errorf("message-id: %w", err)
	}
	buf.WriteString("Message-ID: ")
	buf.WriteString(mid)
	buf.WriteString("\r\n")

	// Mark as transactional so the relay does not inject bulk markers
	// (Precedence: bulk, List-Unsubscribe): our own headers take precedence
	// over relay defaults.
	buf.WriteString("Precedence: transaction\r\n")
	buf.WriteString("X-Message-Type: transaction\r\n")

	buf.WriteString("MIME-Version: 1.0\r\n")

	// ── Body ─────────────────────────────────────────────────────────
	if m.HTMLBody == "" {
		// text/plain only
		buf.WriteString(`Content-Type: text/plain; charset="utf-8"`)
		buf.WriteString("\r\n")
		buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
		buf.WriteString("\r\n")
		if err := writeQuotedPrintable(&buf, m.TextBody); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	// multipart/alternative
	boundary, err := randomBoundary()
	if err != nil {
		return nil, fmt.Errorf("boundary: %w", err)
	}

	buf.WriteString(`Content-Type: multipart/alternative; boundary="`)
	buf.WriteString(boundary)
	buf.WriteString("\"\r\n\r\n")

	// text/plain part
	buf.WriteString("--")
	buf.WriteString(boundary)
	buf.WriteString("\r\n")
	buf.WriteString(`Content-Type: text/plain; charset="utf-8"`)
	buf.WriteString("\r\n")
	buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	buf.WriteString("\r\n")
	if err := writeQuotedPrintable(&buf, m.TextBody); err != nil {
		return nil, err
	}
	buf.WriteString("\r\n")

	// text/html part
	buf.WriteString("--")
	buf.WriteString(boundary)
	buf.WriteString("\r\n")
	buf.WriteString(`Content-Type: text/html; charset="utf-8"`)
	buf.WriteString("\r\n")
	buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	buf.WriteString("\r\n")
	if err := writeQuotedPrintable(&buf, m.HTMLBody); err != nil {
		return nil, err
	}
	buf.WriteString("\r\n")

	// closing boundary
	buf.WriteString("--")
	buf.WriteString(boundary)
	buf.WriteString("--\r\n")

	return buf.Bytes(), nil
}

// formatAddress returns "Name" <addr> if name is non-empty, otherwise <addr>.
// Name is Q-encoded when it contains non-ASCII so RFC 5322 stays compliant.
func formatAddress(addr, name string) string {
	addr = strings.TrimSpace(addr)
	name = strings.TrimSpace(name)
	if name == "" {
		return "<" + addr + ">"
	}
	a := &mail.Address{Name: name, Address: addr}
	return a.String()
}

// writeQuotedPrintable encodes s and writes the result to buf with CRLF line
// endings. mime/quotedprintable.Writer already takes care of soft line breaks.
func writeQuotedPrintable(buf *bytes.Buffer, s string) error {
	w := quotedprintable.NewWriter(buf)
	if _, err := w.Write([]byte(s)); err != nil {
		return err
	}
	return w.Close()
}

// randomBoundary returns a 24-char URL-safe random string suitable for a
// MIME multipart boundary. Collisions in any one message are vanishingly
// rare; we don't bother with a global registry.
func randomBoundary() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// generateMessageID builds an RFC 5322 Message-ID using the From-domain and
// 18 bytes of randomness. The angle brackets are part of the value.
func generateMessageID(from string) (string, error) {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return "", err
	}
	at := strings.LastIndex(addr.Address, "@")
	if at < 0 {
		return "", fmt.Errorf("from %q missing @", from)
	}
	domain := addr.Address[at+1:]
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "<" + base64.RawURLEncoding.EncodeToString(b[:]) + "@" + domain + ">", nil
}
