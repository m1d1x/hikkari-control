package billing

// letters.go is what billing writes to the buyer: the link after a purchase,
// the new end after a renewal, and every link of an address on request — the
// one way back to a lost link, there being no accounts to log into.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/mailer"
	"github.com/m1d1x/hikkari-control/internal/subscription"
)

const (
	kindLink     = "subscription_link"
	kindRenewal  = "renewal"
	kindRecovery = "recovery"
	// recoveriesPerHour bounds the letters anyone can make the hub send to
	// one address by typing it in.
	recoveriesPerHour = 3
)

// moscow is the time buyers read dates in.
var moscow = time.FixedZone("MSK", 3*60*60)

func day(t time.Time) string { return t.In(moscow).Format("02.01.2006") }

// openURL is the landing's page that opens a link in the app: mail clients
// drop hikkari:// hrefs, a web page can offer them.
func (s *Service) openURL(link string) string {
	return s.siteURL + "/open#url=" + url.QueryEscape(link)
}

func planName(p subscription.Plan) string {
	if p.Title != "" {
		return p.Title
	}
	return fmt.Sprintf("%d дн.", p.Days)
}

// expiresIn is the end of account's unrevoked subscription, as tx sees it.
func expiresIn(ctx context.Context, tx pgx.Tx, account uuid.UUID) (time.Time, error) {
	var t *time.Time
	err := tx.QueryRow(ctx,
		`SELECT expires_at FROM hub_subscriptions WHERE account_id = $1 AND revoked_at IS NULL`, account).Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, err
	}
	return *t, nil
}

func (s *Service) linkLetter(p subscription.Plan, link string, ends time.Time) mailer.Letter {
	return mailer.Letter{
		Title: "ваша подписка hikkari",
		Lines: []string{
			fmt.Sprintf("оплата прошла. подписка «%s» действует до %s.", planName(p), day(ends)),
			"откройте её в hikkari кнопкой ниже или вставьте ссылку вручную: профили → добавить.",
			"ссылка и есть доступ: сохраните это письмо и не публикуйте её.",
		},
		Codes:       []string{link},
		Action:      s.openURL(link),
		ActionLabel: "открыть в hikkari",
	}
}

func renewalLetter(p subscription.Plan, ends time.Time) mailer.Letter {
	return mailer.Letter{
		Title: "подписка hikkari продлена",
		Lines: []string{
			fmt.Sprintf("оплата прошла: «%s» добавлены к подписке. теперь она действует до %s.", planName(p), day(ends)),
			"в hikkari ничего менять не нужно: срок обновится сам в течение пяти минут.",
		},
	}
}

// queuePaid writes the letter of a paid order inside its transaction.
func (s *Service) queuePaid(ctx context.Context, tx pgx.Tx, email, plan string, account uuid.UUID, renewal bool, link string) error {
	p, err := s.planAny(ctx, tx, plan)
	if err != nil {
		return err
	}
	ends, err := expiresIn(ctx, tx, account)
	if err != nil {
		return err
	}
	if renewal {
		return mailer.Queue(ctx, tx, email, kindRenewal, renewalLetter(p, ends))
	}
	return mailer.Queue(ctx, tx, email, kindLink, s.linkLetter(p, link, ends))
}

// planAny reads a plan whether on sale or not: an order is delivered on the
// plan it was placed for.
func (s *Service) planAny(ctx context.Context, tx pgx.Tx, id string) (subscription.Plan, error) {
	p := subscription.Plan{ID: id}
	err := tx.QueryRow(ctx, `SELECT title, days FROM hub_plans WHERE id = $1`, id).Scan(&p.Title, &p.Days)
	return p, err
}

// Recover sends email every live-or-ended subscription link bought with it,
// or word that there is none. It answers the same either way, so the form
// tells nobody whether an address has bought anything; the letter goes only
// to the address itself.
func (s *Service) Recover(ctx context.Context, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	// One recovery at a time per address, so the count below holds.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('billing.recover:' || $1))`, email); err != nil {
		return err
	}
	n, err := mailer.Sent(ctx, tx, email, kindRecovery, s.now().Add(-time.Hour))
	if err != nil {
		return err
	}
	if n >= recoveriesPerHour {
		return nil
	}

	// The address the subscription was bought with, or any it was paid
	// for since.
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT s.account_id, s.expires_at FROM hub_subscriptions s
		JOIN hub_accounts a ON a.id = s.account_id
		WHERE s.revoked_at IS NULL AND s.token_sealed IS NOT NULL
		  AND (a.email = $1 OR s.account_id IN (SELECT account_id FROM hub_orders WHERE email = $1 AND status = 'paid'))
		ORDER BY s.expires_at DESC NULLS LAST`, email)
	if err != nil {
		return err
	}
	type found struct {
		account uuid.UUID
		ends    *time.Time
	}
	subs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (found, error) {
		var f found
		return f, r.Scan(&f.account, &f.ends)
	})
	if err != nil {
		return err
	}

	letter := mailer.Letter{
		Title: "ваши ссылки hikkari",
		Lines: []string{"по этому адресу не найдено ни одной подписки hikkari. возможно, её оформляли на другой email."},
	}
	if len(subs) > 0 {
		letter.Lines = []string{"вы попросили прислать ссылки подписок, оформленных на этот адрес. вот они."}
		for _, f := range subs {
			link, err := s.subs.URL(ctx, f.account)
			if errors.Is(err, subscription.ErrUnknown) {
				continue
			}
			if err != nil {
				return err
			}
			letter.Codes = append(letter.Codes, link)
			if f.ends != nil {
				state := "действует до "
				if f.ends.Before(s.now()) {
					state = "закончилась "
				}
				letter.Lines = append(letter.Lines, fmt.Sprintf("подписка %d: %s%s.", len(letter.Codes), state, day(*f.ends)))
			}
		}
		letter.Lines = append(letter.Lines, "добавьте ссылку в hikkari: профили → добавить. если письмо пришло неожиданно, просто удалите его.")
		if len(letter.Codes) == 1 {
			letter.Action, letter.ActionLabel = s.openURL(letter.Codes[0]), "открыть в hikkari"
		}
	}
	if err := mailer.Queue(ctx, tx, email, kindRecovery, letter); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.mail.Nudge()
	return nil
}
