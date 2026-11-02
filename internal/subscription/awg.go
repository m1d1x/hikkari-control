package subscription

// awg.go is a subscription's AmneziaWG side. An account dials every AmneziaWG
// relay with one key pair and one tunnel address, made the first time its link
// is asked for. The relays learn the public half and the address, and hold
// nothing else about the account, the same as with the other protocols' keys.
//
// A subscriber's first link can come up to one pulse before the relays know the
// key: the tunnel starts working within half a minute.

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// awgAddress is the nth host of 10.66.0.0/16, skipping the .0 and .255 that
// some stacks refuse to address. Address 1 is the vantage's
// (entry.AwgProbeAddress), and 64770 is the last there is.
func awgAddress(n int64) (string, error) {
	if n < 1 || n > 254*255 {
		return "", fmt.Errorf("subscription: awg address %d is out of the network", n)
	}
	i := n - 1
	return fmt.Sprintf("10.66.%d.%d", i/254, i%254+1), nil
}

// ensureAwgKey gives the account its key pair and address if it has none. Two
// concurrent first requests settle on one: the row lock makes the second see the
// first's.
func (s *Service) ensureAwgKey(ctx context.Context, account uuid.UUID, k *accessKey) error {
	if k.awgPrivate != "" {
		return nil
	}
	private, public, err := entry.NewAwgKeyPair()
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	var have *string
	if err := tx.QueryRow(ctx, `SELECT awg_private FROM hub_access_keys WHERE account_id = $1 FOR UPDATE`, account).Scan(&have); err != nil {
		return fmt.Errorf("subscription: awg key: %w", err)
	}
	if have == nil {
		var n int64
		if err := tx.QueryRow(ctx, `SELECT nextval('hub_awg_address_seq')`).Scan(&n); err != nil {
			return fmt.Errorf("subscription: awg address: %w", err)
		}
		address, err := awgAddress(n)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE hub_access_keys SET awg_private = $2, awg_public = $3, awg_address = $4 WHERE account_id = $1`,
			account, private, public, address); err != nil {
			return fmt.Errorf("subscription: awg key: %w", err)
		}
	}
	if err := tx.QueryRow(ctx,
		`SELECT awg_private, host(awg_address) FROM hub_access_keys WHERE account_id = $1`, account,
	).Scan(&k.awgPrivate, &k.awgAddress); err != nil {
		return fmt.Errorf("subscription: awg key: %w", err)
	}
	return tx.Commit(ctx)
}

// AwgLink renders an AmneziaWG relay as Amnezia's own vpn:// link: one line,
// which a line-per-link subscription needs and a .conf file is not, and already
// understood by the clients in people's hands. The payload is Qt's qCompress of
// a server description holding the .conf text.
func AwgLink(r entry.RelayRecord) (string, error) {
	p := r.AwgParams
	if r.Proto != entry.ProtoAmneziaWG || p == nil {
		return "", errNoLink
	}
	conf := strings.Join([]string{
		"[Interface]",
		"PrivateKey = " + p.ClientPrivateKey,
		"Address = " + p.ClientAddress + "/32",
		"MTU = 1280",
		"Jc = " + strconv.Itoa(p.Jc),
		"Jmin = " + strconv.Itoa(p.Jmin),
		"Jmax = " + strconv.Itoa(p.Jmax),
		"S1 = " + strconv.Itoa(p.S1),
		"S2 = " + strconv.Itoa(p.S2),
		"H1 = " + p.H1,
		"H2 = " + p.H2,
		"H3 = " + p.H3,
		"H4 = " + p.H4,
		"",
		"[Peer]",
		"PublicKey = " + p.ServerPublicKey,
		"AllowedIPs = 0.0.0.0/0",
		"Endpoint = " + net.JoinHostPort(r.Address, strconv.Itoa(r.Port)),
		"PersistentKeepalive = 25",
		"",
	}, "\n")
	doc, err := json.Marshal(map[string]any{
		"description":      "Hikkari " + r.Name,
		"hostName":         r.Address,
		"defaultContainer": "amnezia-awg",
		"containers": []any{map[string]any{
			"container": "amnezia-awg",
			"awg":       map[string]any{"last_config": conf},
		}},
	})
	if err != nil {
		return "", err
	}
	var compressed bytes.Buffer
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, uint32(len(doc)))
	compressed.Write(size)
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(doc); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return "vpn://" + base64.RawURLEncoding.EncodeToString(compressed.Bytes()), nil
}
