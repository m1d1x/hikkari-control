package mailer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/db"
)

const (
	// lease is how long a claimed letter is held by one delivery attempt; a
	// worker that dies mid-send leaves it to be tried again after.
	lease = 5 * time.Minute
	batch = 20
)

// retryAfter is the wait after each failed attempt; past the last the letter
// is given up on, about a day after it was queued.
var retryAfter = []time.Duration{
	time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour,
}

// Queue puts a letter to to in the outbox, inside q's transaction: it goes
// out only if that transaction commits.
func Queue(ctx context.Context, q db.DBTX, to, kind string, l Letter) error {
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	sealed, err := cryptobox.Encrypt(raw)
	if err != nil {
		return fmt.Errorf("mailer: seal letter: %w", err)
	}
	_, err = q.Exec(ctx,
		`INSERT INTO hub_mail_outbox (to_address, kind, letter_sealed) VALUES ($1, $2, $3)`, to, kind, sealed)
	return err
}

// Sent is how many letters of kind were queued to to since since: what a
// sender that anyone can trigger (a link sent again) is limited by.
func Sent(ctx context.Context, q db.DBTX, to, kind string, since time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx,
		`SELECT count(*) FROM hub_mail_outbox WHERE to_address = $1 AND kind = $2 AND created_at > $3`,
		to, kind, since).Scan(&n)
	return n, err
}

// Outbox delivers queued letters through the provider.
type Outbox struct {
	pool     db.Pool
	provider Provider
	sender   Sender
	nudge    chan struct{}
}

// NewOutbox delivers through provider; nil keeps letters queued until a
// provider is configured.
func NewOutbox(pool db.Pool, provider Provider, sender Sender) *Outbox {
	return &Outbox{pool: pool, provider: provider, sender: sender, nudge: make(chan struct{}, 1)}
}

// Nudge asks for a delivery round now rather than at the next tick: called
// after a commit that queued a letter, so it leaves within a second.
func (o *Outbox) Nudge() {
	select {
	case o.nudge <- struct{}{}:
	default:
	}
}

// Run delivers until ctx ends, every interval and on every nudge.
func (o *Outbox) Run(ctx context.Context, every time.Duration) {
	if o.provider == nil {
		applog.Warn(ctx, "mailer", "MAILER_PROVIDER not set — letters stay queued")
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := o.Flush(ctx); err != nil {
			applog.Error(ctx, "mailer", "flush", applog.F("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-o.nudge:
		}
	}
}

type due struct {
	id       int64
	to       string
	sealed   []byte
	attempts int
}

// Flush tries every letter that is due, once.
func (o *Outbox) Flush(ctx context.Context) error {
	if o.provider == nil {
		return nil
	}
	rows, err := o.pool.Query(ctx, `
		UPDATE hub_mail_outbox SET next_at = now() + make_interval(secs => $1), attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM hub_mail_outbox
			WHERE sent_at IS NULL AND failed_at IS NULL AND next_at <= now()
			ORDER BY next_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING id, to_address, letter_sealed, attempts`, lease.Seconds(), batch)
	if err != nil {
		return err
	}
	dues, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id, &d.to, &d.sealed, &d.attempts)
	})
	if err != nil {
		return err
	}
	for _, d := range dues {
		o.deliver(ctx, d)
	}
	return nil
}

func (o *Outbox) deliver(ctx context.Context, d due) {
	err := o.send(ctx, d)
	if err == nil {
		if _, err := o.pool.Exec(ctx, `
			UPDATE hub_mail_outbox SET sent_at = now(), provider = $2, last_error = NULL WHERE id = $1`,
			d.id, o.provider.Name()); err != nil {
			applog.Error(ctx, "mailer", "mark sent", applog.F("err", err.Error()))
		}
		return
	}
	applog.Warn(ctx, "mailer", "delivery failed", applog.F("letter", d.id), applog.F("attempt", d.attempts),
		applog.F("err", err.Error()))
	if d.attempts > len(retryAfter) {
		_, err = o.pool.Exec(ctx,
			`UPDATE hub_mail_outbox SET failed_at = now(), last_error = $2 WHERE id = $1`, d.id, err.Error())
	} else {
		_, err = o.pool.Exec(ctx, `
			UPDATE hub_mail_outbox SET next_at = now() + make_interval(secs => $2), last_error = $3 WHERE id = $1`,
			d.id, retryAfter[d.attempts-1].Seconds(), err.Error())
	}
	if err != nil {
		applog.Error(ctx, "mailer", "mark failed", applog.F("err", err.Error()))
	}
}

func (o *Outbox) send(ctx context.Context, d due) error {
	raw, err := cryptobox.Decrypt(d.sealed)
	if err != nil {
		return fmt.Errorf("open letter: %w", err)
	}
	var l Letter
	if err := json.Unmarshal(raw, &l); err != nil {
		return fmt.Errorf("read letter: %w", err)
	}
	msg, err := l.Render(o.sender, d.to)
	if err != nil {
		return fmt.Errorf("render letter: %w", err)
	}
	return o.provider.Send(ctx, msg)
}
