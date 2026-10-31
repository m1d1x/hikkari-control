package main

import (
	"crypto/rand"
	"fmt"
	"log"

	"github.com/m1d1x/hikkari-control/internal/billing"
	"github.com/m1d1x/hikkari-control/internal/config"
)

// billingProvider is the payment provider BILLING_PROVIDER names:
//
//	""        none: plans are shown, orders refused
//	"sandbox" the hub's own test payment page, no money moves
//	          (BILLING_SANDBOX_SECRET signs its notifications; random if unset)
//
// A merchant is one more case here and one Provider in internal/billing.
func billingProvider(getenv func(string) string) (billing.Provider, error) {
	switch name := getenv("BILLING_PROVIDER"); name {
	case "":
		log.Println("billing: BILLING_PROVIDER not set — subscriptions are not sold")
		return nil, nil
	case "sandbox":
		secret := []byte(getenv("BILLING_SANDBOX_SECRET"))
		if len(secret) == 0 {
			secret = make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return nil, err
			}
		}
		log.Println("billing: SANDBOX provider — test payments, no money moves")
		return billing.NewSandbox(config.Default().APIURL(), secret), nil
	default:
		return nil, fmt.Errorf("BILLING_PROVIDER %q: unknown provider", name)
	}
}
