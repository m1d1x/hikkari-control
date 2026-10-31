package billing

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
)

// Provider is one payment service. Connecting a merchant is one
// implementation of it: the rest of billing never learns whose money moves.
type Provider interface {
	// Name is the provider's id in hub_orders.provider and in its webhook
	// path, /v1/billing/webhook/{name}.
	Name() string
	// Create opens a payment for an order and answers with the provider's id
	// for it and the page the buyer pays on.
	Create(ctx context.Context, p Payment) (Checkout, error)
	// Webhook reads a notification the provider sent, after checking it came
	// from the provider. ErrNotForUs for a well-formed notification billing
	// has nothing to do with (another event type): it is acknowledged.
	Webhook(r *http.Request) (Event, error)
	// Check asks the provider where a payment stands: what settles an order
	// whose notification was lost.
	Check(ctx context.Context, ref string) (Event, error)
}

// Payment is what the provider is asked to collect.
type Payment struct {
	Order       uuid.UUID
	AmountCents int
	// Description is shown to the buyer and printed on the receipt.
	Description string
	// Email is the buyer's, for the receipt.
	Email string
	// ReturnURL is where the provider sends the buyer after paying.
	ReturnURL string
}

// Checkout is an opened payment.
type Checkout struct {
	Ref string
	URL string
}

// State is where a payment stands at the provider.
type State int

const (
	// Pending: not paid yet, may still be.
	Pending State = iota
	// Paid: the money is taken.
	Paid
	// Canceled: the payment will not happen (declined, abandoned, expired at
	// the provider).
	Canceled
)

// Event is a payment's state as the provider reports it.
type Event struct {
	Ref         string
	State       State
	AmountCents int
}

// ErrNotForUs: a genuine notification that settles nothing.
var ErrNotForUs = errors.New("billing: notification not for an order")

// ErrSignature: a notification that did not come from the provider.
var ErrSignature = errors.New("billing: notification signature")
