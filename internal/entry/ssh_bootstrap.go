package entry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/sshexec"
)

// OwnerBootstrapDialer lets an explicit CLI adoption use the owner's temporary
// agent to install the hub's public key. Private keys stay in the agent. The
// returned connection is always reopened with the ORIGINAL hub authentication;
// automatic jobs never receive this dialer and never depend on the owner.
func OwnerBootstrapDialer(base Dialer, owner ssh.AuthMethod, hubPublicKey string) Dialer {
	return func(ctx context.Context, target sshexec.Target, auth ...ssh.AuthMethod) (Remote, error) {
		if !validBootstrapPublicKey(hubPublicKey) {
			return nil, fmt.Errorf("owner agent: invalid hub public key")
		}
		r, err := base(ctx, target, auth...)
		if err == nil || !errors.Is(err, sshexec.ErrAuth) {
			return r, err
		}
		bootstrap, err := base(ctx, target, owner)
		if err != nil {
			return nil, err
		}
		// Pin the exact host met through owner authentication for the proof login.
		target.HostKey = bootstrap.HostKey()
		_, err = bootstrap.Run(ctx, `set -eu
umask 077
mkdir -p ~/.ssh
touch ~/.ssh/authorized_keys
grep -qxF `+proberShellQuote(hubPublicKey)+` ~/.ssh/authorized_keys || printf '\n%s\n' `+proberShellQuote(hubPublicKey)+` >> ~/.ssh/authorized_keys`, nil)
		bootstrap.Close()
		if err != nil {
			return nil, fmt.Errorf("owner agent: install hub key: %w", err)
		}
		r, err = base(ctx, target, auth...)
		if err != nil {
			return nil, fmt.Errorf("owner agent: prove hub key: %w", err)
		}
		return r, nil
	}
}

// Keep public keys as one authorized_keys line, even for external callers.
func validBootstrapPublicKey(key string) bool {
	_, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	return err == nil && strings.TrimSpace(string(rest)) == ""
}
