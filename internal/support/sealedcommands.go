package support

// proto_targets carries profiles of subscriptions the owner paid for, secrets
// included, since the core has to dial them. The row holds them only under
// cryptobox, the plain text exists on the way to the tester's client, and the
// kind stays out of testerCommandKinds: only the census asks for it, from what
// it holds itself, never from a request body.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// TesterCommandProto asks a client to try profiles through its own core.
const TesterCommandProto = "proto_targets"

// ErrTesterOffline means the install is not on the line.
var ErrTesterOffline = errors.New("tester is not on the line")

// QueueProto seals plain and queues it for the tester named by ref.
func QueueProto(ctx context.Context, store TesterStore, ref string, plain []byte) (TesterCommandState, string, error) {
	installID, _, err := store.ResolveInstall(ctx, ref)
	if err != nil {
		return TesterCommandState{}, "", err
	}
	in, err := findInstall(ctx, store, installID)
	if err != nil {
		return TesterCommandState{}, "", err
	}
	if !in.Online {
		return TesterCommandState{}, "", fmt.Errorf("%w: last signal %s ago", ErrTesterOffline, time.Since(in.LastSeen).Round(time.Second))
	}
	ct, err := cryptobox.Encrypt(plain)
	if err != nil {
		return TesterCommandState{}, "", err
	}
	params, err := json.Marshal(map[string][]byte{"sealed": ct})
	if err != nil {
		return TesterCommandState{}, "", err
	}
	cmd, err := store.EnqueueCommand(ctx, installID, TesterCommandProto, params)
	return cmd, in.Version, err
}

// unsealCommands opens sealed params on their way out. One that cannot be
// opened is dropped: the operator's wait ends in a timeout, not in garbage.
func unsealCommands(cmds []TesterCommand) []TesterCommand {
	out := cmds[:0]
	for _, c := range cmds {
		if c.Kind == TesterCommandProto {
			var p struct {
				Sealed []byte `json:"sealed"`
			}
			if json.Unmarshal(c.Params, &p) != nil {
				continue
			}
			plain, err := cryptobox.Decrypt(p.Sealed)
			if err != nil {
				continue
			}
			c.Params = plain
		}
		out = append(out, c)
	}
	return out
}
