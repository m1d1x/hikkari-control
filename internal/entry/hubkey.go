package entry

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// The hub's ssh key (migration 043): one key for every box it provisions,
// minted on first use, the private half sealed under the server key. After the
// first login with the provider's password the hub logs in with this key only,
// and password login is switched off on the box.

const (
	selectHubSSH = `SELECT private_ct, public_key FROM entry_hub_ssh WHERE id = true`
	insertHubSSH = `
		INSERT INTO entry_hub_ssh (private_ct, public_key) VALUES ($1, $2)
		ON CONFLICT (id) DO NOTHING`
)

// HubSSHKey returns the hub's ssh signer and its public key in authorized_keys
// form, minting the key the first time. Two hubs racing to mint it both end up
// with the one that landed first.
func (s *Service) HubSSHKey(ctx context.Context) (ssh.Signer, string, error) {
	signer, pub, err := s.loadHubSSHKey(ctx)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return signer, pub, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "nekko-hub")
	if err != nil {
		return nil, "", err
	}
	fresh, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, "", err
	}
	ct, err := cryptobox.Encrypt(pem.EncodeToMemory(block))
	if err != nil {
		return nil, "", err
	}
	if _, err := s.pool.Exec(ctx, insertHubSSH, ct, authorizedKey(fresh.PublicKey())); err != nil {
		return nil, "", err
	}
	return s.loadHubSSHKey(ctx)
}

func (s *Service) loadHubSSHKey(ctx context.Context) (ssh.Signer, string, error) {
	var ct []byte
	var pub string
	if err := s.pool.QueryRow(ctx, selectHubSSH).Scan(&ct, &pub); err != nil {
		return nil, "", err
	}
	raw, err := cryptobox.Decrypt(ct)
	if err != nil {
		return nil, "", err
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, "", err
	}
	return signer, pub, nil
}

func authorizedKey(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) + " nekko-hub"
}
