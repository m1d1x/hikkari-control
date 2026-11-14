// Package fx turns a price in the currency a hoster bills in into US cents,
// the one unit the hub keeps money in (entry_boxes.monthly_cents, the budget
// caps, the console).
//
// Hosters bill in their own money: VDSina's Russian API and the Russian
// hosters rented by hand in roubles, HostVDS and Hetzner in euros. A price
// copied into cents as it stands reads 469 roubles as $4.69 and €1.49 as
// $1.49, which is how the console came to show a fleet at a third of its cost
// (14.11). The rate is the Bank of Russia's daily one, read from the public
// mirror of its feed: the hub's own money is paid from Russia, so that is the
// rate the bills are actually settled at.
package fx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Currencies the hub prices machines in.
const (
	USD = "USD"
	EUR = "EUR"
	RUB = "RUB"
)

// DefaultURL is the Bank of Russia's daily rates as JSON.
const DefaultURL = "https://www.cbr-xml-daily.ru/daily_json.js"

// refresh is how old a reading may be before the next conversion reads
// again. The bank publishes once a day; a few hours keeps a long-running hub
// within a day of it without asking on every price.
const refresh = 6 * time.Hour

// ErrCurrency: a currency the hub does not price machines in.
var ErrCurrency = errors.New("fx: unknown currency")

// Normalize upper-cases a currency code and accepts the rouble's common
// spellings. ErrCurrency for anything else.
func Normalize(cur string) (string, error) {
	c := strings.ToUpper(strings.TrimSpace(cur))
	switch c {
	case "", "$", "USD":
		return USD, nil
	case "€", "EUR":
		return EUR, nil
	case "₽", "RUB", "RUR", "РУБ":
		return RUB, nil
	}
	return "", fmt.Errorf("%w: %q", ErrCurrency, cur)
}

// Rates converts with the bank's rates, read lazily and kept for [refresh].
// A failed read keeps the last good one: a price a day stale is closer to the
// truth than no price. With no reading at all, conversion is an error, never
// a guess.
type Rates struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu   sync.Mutex
	at   time.Time
	rubs map[string]float64 // roubles per one unit of the currency
}

// New reads from url (DefaultURL when empty).
func New(url string) *Rates {
	if url == "" {
		url = DefaultURL
	}
	return &Rates{url: url, client: &http.Client{Timeout: 15 * time.Second}, now: time.Now}
}

// Fixed is a Rates that never reads: roubles per one unit of each currency.
// For tests and for a caller that was handed the rates.
func Fixed(rubsPer map[string]float64) *Rates {
	r := &Rates{now: time.Now, rubs: map[string]float64{RUB: 1}}
	for k, v := range rubsPer {
		r.rubs[k] = v
	}
	r.at = time.Unix(1<<62, 0) // never stale
	return r
}

var (
	defaultOnce  sync.Once
	defaultRates *Rates
)

// Default is the process-wide reader of the bank's feed, so every hoster and
// every pass share one reading.
func Default() *Rates {
	defaultOnce.Do(func() { defaultRates = New("") })
	return defaultRates
}

// USDCents converts amount (whole units of cur, per month) into US cents,
// rounded to the cent.
func (r *Rates) USDCents(ctx context.Context, amount float64, cur string) (int, error) {
	c, err := Normalize(cur)
	if err != nil {
		return 0, err
	}
	if c == USD {
		return int(math.Round(amount * 100)), nil
	}
	rubs, err := r.read(ctx)
	if err != nil {
		return 0, err
	}
	perUnit, ok := rubs[c]
	usd, okUSD := rubs[USD]
	if !ok || !okUSD || usd <= 0 {
		return 0, fmt.Errorf("fx: no rate for %s", c)
	}
	return int(math.Round(amount * perUnit / usd * 100)), nil
}

func (r *Rates) read(ctx context.Context) (map[string]float64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rubs != nil && r.now().Sub(r.at) < refresh {
		return r.rubs, nil
	}
	fresh, err := r.fetch(ctx)
	if err != nil {
		if r.rubs != nil {
			return r.rubs, nil
		}
		return nil, err
	}
	r.rubs, r.at = fresh, r.now()
	return r.rubs, nil
}

func (r *Rates) fetch(ctx context.Context) (map[string]float64, error) {
	if r.url == "" {
		return nil, errors.New("fx: no rates source")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fx: rates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fx: rates: http %d", resp.StatusCode)
	}
	var body struct {
		Valute map[string]struct {
			Nominal float64 `json:"Nominal"`
			Value   float64 `json:"Value"`
		} `json:"Valute"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("fx: rates: %w", err)
	}
	out := map[string]float64{RUB: 1}
	for _, c := range []string{USD, EUR} {
		v, ok := body.Valute[c]
		if !ok || v.Value <= 0 || v.Nominal <= 0 {
			return nil, fmt.Errorf("fx: rates: no %s in the feed", c)
		}
		out[c] = v.Value / v.Nominal
	}
	return out, nil
}
