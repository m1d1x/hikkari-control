package mailer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
)

func TestMain(m *testing.M) {
	cryptobox.ConfigureEphemeralForTests()
	os.Exit(m.Run())
}

// A letter renders into a message with both parts, its words escaped in the
// HTML one, its codes and its button in both.
func TestALetterRenders(t *testing.T) {
	l := Letter{
		Title:       "ваша подписка hikkari",
		Lines:       []string{"строка <b>не тег</b>"},
		Codes:       []string{"https://sub.hikkari.net/sub/abc"},
		Action:      "https://hikkari.net/open#url=x",
		ActionLabel: "открыть в hikkari",
	}
	m, err := l.Render(Sender{From: "no-reply@hikkari.net", FromName: "hikkari"}, "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != l.Title || !strings.Contains(m.TextBody, l.Codes[0]) || !strings.Contains(m.TextBody, l.Action) {
		t.Fatalf("text %q", m.TextBody)
	}
	if !strings.Contains(m.HTMLBody, "&lt;b&gt;не тег&lt;/b&gt;") || !strings.Contains(m.HTMLBody, l.Codes[0]) {
		t.Fatalf("html %q", m.HTMLBody)
	}
	raw, err := m.BuildMIME()
	if err != nil || !strings.Contains(string(raw), "multipart/alternative") {
		t.Fatalf("mime: %v", err)
	}
}

type flaky struct{ fails int }

func (*flaky) Name() string { return "flaky" }

func (f *flaky) Send(context.Context, Message) error {
	if f.fails > 0 {
		f.fails--
		return errors.New("relay down")
	}
	return nil
}

// A letter the relay refuses waits and is tried again; one refused every
// time is given up on after the last retry.
func TestAFailedLetterIsRetriedThenGivenUp(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run mailer integration tests")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	to := "retry-" + uuid.NewString()[:8] + "@example.com"
	state := func() (attempts int, sent, failed bool) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT attempts, sent_at IS NOT NULL, failed_at IS NOT NULL FROM hub_mail_outbox WHERE to_address = $1`, to).
			Scan(&attempts, &sent, &failed); err != nil {
			t.Fatal(err)
		}
		return
	}
	due := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE hub_mail_outbox SET next_at = now() WHERE to_address = $1`, to); err != nil {
			t.Fatal(err)
		}
	}
	if err := Queue(ctx, pool, to, "test", Letter{Title: "t", Lines: []string{"l"}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := Sent(ctx, pool, to, "test", time.Now().Add(-time.Hour)); n != 1 {
		t.Fatalf("sent count %d", n)
	}

	relay := &flaky{fails: 1}
	o := NewOutbox(pool, relay, Sender{From: "no-reply@hikkari.test"})
	if err := o.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if a, sent, _ := state(); a != 1 || sent {
		t.Fatalf("after a refusal: attempts %d sent %v", a, sent)
	}
	if err := o.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := state(); a != 1 {
		t.Fatal("a refused letter was tried again before its wait")
	}
	due()
	if err := o.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if a, sent, _ := state(); a != 2 || !sent {
		t.Fatalf("after the retry: attempts %d sent %v", a, sent)
	}

	to = "giveup-" + uuid.NewString()[:8] + "@example.com"
	if err := Queue(ctx, pool, to, "test", Letter{Title: "t", Lines: []string{"l"}}); err != nil {
		t.Fatal(err)
	}
	o = NewOutbox(pool, &flaky{fails: 1000}, Sender{From: "no-reply@hikkari.test"})
	for range len(retryAfter) + 1 {
		due()
		if err := o.Flush(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if a, sent, failed := state(); a != len(retryAfter)+1 || sent || !failed {
		t.Fatalf("after every retry: attempts %d sent %v failed %v", a, sent, failed)
	}
}
