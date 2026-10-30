// Package devicecard is how a device describes itself to its owner — the
// model and the operating system on the sessions list and on the «подключить
// это устройство?» screen — and how the server keeps that at rest.
//
// The client reports the card (DeviceBinding.card); the server stores it
// encrypted under its own key (cryptobox), as it stores the address. That is
// the line the project draws: content is end-to-end and the server is blind to
// it; metadata is the server's to read — fraud scoring, regional figures — and
// is encrypted only so that a leaked dump or backup does not hand it out.
package devicecard

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// MaxFieldLen bounds each field, in bytes. Matches the proto comment.
const MaxFieldLen = 64

// ErrInvalid is a card the server refuses to store.
var ErrInvalid = errors.New("device card: invalid")

// Card is what a person recognises a device by.
type Card struct {
	// «iPhone 12 mini», «MacBook Pro». Never a host name.
	Model string `json:"model,omitempty"`
	// «iOS 18.1», «macOS 15.2».
	System string `json:"system,omitempty"`
}

// IsZero reports a card that says nothing — a session registered before
// devices reported one.
func (c Card) IsZero() bool { return c.Model == "" && c.System == "" }

// Normalize trims the fields and checks them. A field that is not UTF-8, holds
// a control character or runs past MaxFieldLen is a client bug or a probe,
// and is refused rather than truncated: a shortened model is a different one.
func Normalize(c Card) (Card, error) {
	out := Card{Model: strings.TrimSpace(c.Model), System: strings.TrimSpace(c.System)}
	for _, f := range []string{out.Model, out.System} {
		if len(f) > MaxFieldLen || !utf8.ValidString(f) {
			return Card{}, ErrInvalid
		}
		for _, r := range f {
			if r < 0x20 || r == 0x7f {
				return Card{}, ErrInvalid
			}
		}
	}
	return out, nil
}

// Seal encrypts the card for a BYTEA column. A zero card is SQL NULL.
func Seal(c Card) ([]byte, error) {
	if c.IsZero() {
		return nil, nil
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("device card: encode: %w", err)
	}
	ct, err := cryptobox.Encrypt(plain)
	if err != nil {
		return nil, fmt.Errorf("device card: encrypt: %w", err)
	}
	return ct, nil
}

// Column is Seal as a SQL bind value. A failure to encrypt is logged and
// stores NULL — the card is a nicety, and a misconfigured key must not lock a
// person out of signing in. The same rule the session address follows.
func Column(c Card) any {
	ct, err := Seal(c)
	if err != nil {
		log.Printf("devicecard: %v", err)
		return nil
	}
	if len(ct) == 0 {
		return nil
	}
	return ct
}

// Read is Open for a row being listed: a card that does not open is logged and
// read as none, so one bad row does not hide the account's other devices.
func Read(ct []byte) Card {
	c, err := Open(ct)
	if err != nil {
		log.Printf("devicecard: %v", err)
		return Card{}
	}
	return c
}

// Open reverses Seal. NULL is the zero card.
func Open(ct []byte) (Card, error) {
	if len(ct) == 0 {
		return Card{}, nil
	}
	plain, err := cryptobox.Decrypt(ct)
	if err != nil {
		return Card{}, fmt.Errorf("device card: decrypt: %w", err)
	}
	var c Card
	if err := json.Unmarshal(plain, &c); err != nil {
		return Card{}, fmt.Errorf("device card: decode: %w", err)
	}
	return c, nil
}
