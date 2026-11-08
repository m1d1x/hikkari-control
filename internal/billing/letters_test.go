package billing

import (
	"strings"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/mailer"
	"github.com/m1d1x/hikkari-control/internal/subscription"
)

func TestPurchaseLetterKeepsOnlyTheExpiryAndLink(t *testing.T) {
	s := &Service{}
	const link = "https://sub.hikkari.net/sub/test-token"
	l := s.linkLetter(subscription.Plan{Title: "1 месяц"}, link, time.Date(2026, 12, 8, 12, 0, 0, 0, time.UTC))
	m, err := l.Render(mailer.Sender{From: "no-reply@hikkari.net", FromName: "hikkari"}, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{m.TextBody, m.HTMLBody} {
		for _, want := range []string{link, "08.12.2026", "открыть подписку"} {
			if !strings.Contains(body, want) {
				t.Errorf("letter missing %q", want)
			}
		}
		if strings.Contains(body, "<script") || strings.Contains(body, "hikkari://") {
			t.Error("email relies on unsupported interactive content")
		}
	}
	if l.Action != link {
		t.Fatalf("button does not open canonical subscription page: %s", l.Action)
	}
}
