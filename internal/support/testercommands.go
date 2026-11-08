package support

// Commands to a tester's client. A command rides the answer to POST
// /tester-logs: the client already calls once a minute (a heartbeat when the
// log is quiet), so there is no new connection and the delay stays under a
// minute. The result comes back as ordinary log lines, source
// «doctor nonce=…», with the nonce acknowledged in the chunk that carries the
// last of them. A command runs no code: kind is a word from a list both sides
// hold. Only probe_targets and provoke_targets carry data, addresses to open a
// connection to, and both sides check them (probetargets.go).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// TesterOnlineWindow is how long after its last signal an install counts as
	// on the line: three missed one-minute heartbeats.
	TesterOnlineWindow = 3 * time.Minute
	// TesterCommandTTL is how long a command waits for its client and for its
	// answer. Past it the command stops being delivered and reads as expired.
	TesterCommandTTL = 5 * time.Minute

	TesterCommandRunDoctor = "run_doctor"
	TesterCommandSnapshot  = "snapshot"
	TesterCommandProbe     = "probe_targets"
	// TesterCommandLoad runs every profile of the install under load now. Its
	// answer only says the round started: the lines follow as [diag:load].
	TesterCommandLoad = "load_profiles"
	// TesterCommandVoice sends a call's datagrams through the tunnel for half a
	// minute, and on Windows from the card beside it; the answer is the
	// [diag:voice] lines (internal/voiceruns keeps them).
	TesterCommandVoice = "probe_voice"
	// TesterCommandProvoke sends stimuli ТСПУ is known to cut (blocked SNIs, a
	// long flow, a burst of source ports, datagram streams) from the card to
	// provoke boxes (edge EDGE_PROTO=provoke) and asks each what reached it:
	// опыт 0 of the РФ-каскад. Up to MaxProvokeTargets boxes, run at once.
	TesterCommandProvoke = "provoke_targets"

	TesterCommandPending   = "pending"
	TesterCommandDelivered = "delivered"
	TesterCommandDone      = "done"
	TesterCommandExpired   = "expired"
)

// ErrCommandNotFound means no such command for this install.
var ErrCommandNotFound = errors.New("command not found")

// testerCommandKinds is the whitelist of what may be asked of a client.
var testerCommandKinds = map[string]bool{
	TesterCommandRunDoctor: true,
	TesterCommandSnapshot:  true,
	TesterCommandProbe:     true,
	TesterCommandLoad:      true,
	TesterCommandVoice:     true,
	TesterCommandProvoke:   true,
}

// TesterCommand is what a client receives in the answer to its chunk.
type TesterCommand struct {
	Kind   string          `json:"kind"`
	Nonce  string          `json:"nonce"`
	Params json.RawMessage `json:"params,omitempty"`
}

// TesterCommandState is a command as the operator sees it.
type TesterCommandState struct {
	Nonce       string     `json:"nonce"`
	InstallID   string     `json:"install_id"`
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	AckedAt     *time.Time `json:"acked_at,omitempty"`
	params      json.RawMessage
}

// settle derives the state from the row's timestamps.
func (c *TesterCommandState) settle(now time.Time) {
	switch {
	case c.AckedAt != nil:
		c.State = TesterCommandDone
	case now.Sub(c.CreatedAt) > TesterCommandTTL:
		c.State = TesterCommandExpired
	case c.DeliveredAt != nil:
		c.State = TesterCommandDelivered
	default:
		c.State = TesterCommandPending
	}
}

func newCommandNonce() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("command nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// findInstall returns one install's summary from the list.
func findInstall(ctx context.Context, store TesterStore, installID string) (*TesterInstall, error) {
	installs, err := store.ListInstalls(ctx)
	if err != nil {
		return nil, err
	}
	for i := range installs {
		if installs[i].InstallID == installID {
			return &installs[i], nil
		}
	}
	return nil, ErrAliasNotFound
}

// TesterCommandRequestHandler asks an install for a diagnostic. An install off
// the line is refused at once: the caller should not wait out a timeout to
// learn what the hub already knows.
func TesterCommandRequestHandler(store TesterStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		installID, alias, err := store.ResolveInstall(r.Context(), r.PathValue("install"))
		if err != nil {
			writeResolveErr(w, err)
			return
		}
		var req struct {
			Kind    string        `json:"kind"`
			Targets []ProbeTarget `json:"targets"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBody)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !testerCommandKinds[req.Kind] {
			writeErr(w, http.StatusBadRequest, "unknown command kind")
			return
		}
		params, err := commandParams(req.Kind, req.Targets)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		in, err := findInstall(r.Context(), store, installID)
		if err != nil {
			writeResolveErr(w, err)
			return
		}
		if !in.Online {
			writeErr(w, http.StatusConflict, fmt.Sprintf("%s не на связи: последний сигнал %s назад",
				alias, time.Since(in.LastSeen).Round(time.Second)))
			return
		}
		cmd, err := store.EnqueueCommand(r.Context(), installID, req.Kind, params)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to queue command")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"nonce": cmd.Nonce, "kind": cmd.Kind, "state": cmd.State, "created_at": cmd.CreatedAt,
			"install_id": installID, "alias": alias, "version": in.Version,
		})
	}
}

// TesterCommandStatusHandler says how far one command got.
func TesterCommandStatusHandler(store TesterStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		installID, _, err := store.ResolveInstall(r.Context(), r.PathValue("install"))
		if err != nil {
			writeResolveErr(w, err)
			return
		}
		cmd, err := store.ReadCommand(r.Context(), installID, r.PathValue("nonce"))
		if err != nil {
			if errors.Is(err, ErrCommandNotFound) {
				writeErr(w, http.StatusNotFound, "command not found")
				return
			}
			writeErr(w, http.StatusInternalServerError, "failed to read command")
			return
		}
		writeJSON(w, http.StatusOK, cmd)
	}
}

// ============================================================================
// In-Memory Store
// ============================================================================

func (s *MemTesterStore) EnqueueCommand(_ context.Context, installID, kind string, params json.RawMessage) (TesterCommandState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, c := range s.commands {
		c.settle(now)
		if c.InstallID == installID && c.Kind == kind && bytes.Equal(c.params, params) && (c.State == TesterCommandPending || c.State == TesterCommandDelivered) {
			return *c, nil
		}
	}
	nonce, err := newCommandNonce()
	if err != nil {
		return TesterCommandState{}, err
	}
	c := &TesterCommandState{Nonce: nonce, InstallID: installID, Kind: kind, CreatedAt: now, params: params}
	c.settle(now)
	s.commands = append(s.commands, c)
	return *c, nil
}

func (s *MemTesterStore) TakeCommands(_ context.Context, installID string) ([]TesterCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []TesterCommand
	for _, c := range s.commands {
		c.settle(now)
		if c.InstallID == installID && c.State == TesterCommandPending {
			delivered := now
			c.DeliveredAt = &delivered
			out = append(out, TesterCommand{Kind: c.Kind, Nonce: c.Nonce, Params: c.params})
		}
	}
	return out, nil
}

func (s *MemTesterStore) AckCommands(_ context.Context, installID string, nonces []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, c := range s.commands {
		for _, n := range nonces {
			if c.InstallID == installID && c.Nonce == n && c.AckedAt == nil {
				acked := now
				c.AckedAt = &acked
			}
		}
	}
	return nil
}

func (s *MemTesterStore) ReadCommand(_ context.Context, installID, nonce string) (TesterCommandState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.commands {
		if c.InstallID == installID && c.Nonce == nonce {
			out := *c
			out.settle(s.now())
			return out, nil
		}
	}
	return TesterCommandState{}, ErrCommandNotFound
}

// ============================================================================
// PostgreSQL-backed Store
// ============================================================================

func (s *DBTesterStore) EnqueueCommand(ctx context.Context, installID, kind string, params json.RawMessage) (TesterCommandState, error) {
	// A second caller asking the same thing waits on the same run.
	c, err := scanCommand(s.pool.QueryRow(ctx, `
		SELECT nonce, install_id, kind, created_at, delivered_at, acked_at
		FROM support_tester_commands
		WHERE install_id = $1 AND kind = $2 AND acked_at IS NULL AND created_at > $3
		  AND params IS NOT DISTINCT FROM $4::jsonb
		ORDER BY created_at DESC LIMIT 1
	`, installID, kind, time.Now().Add(-TesterCommandTTL), nullableJSON(params)))
	if err == nil {
		c.settle(time.Now())
		return c, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TesterCommandState{}, fmt.Errorf("find open command: %w", err)
	}
	nonce, err := newCommandNonce()
	if err != nil {
		return TesterCommandState{}, err
	}
	c, err = scanCommand(s.pool.QueryRow(ctx, `
		INSERT INTO support_tester_commands (nonce, install_id, kind, params) VALUES ($1, $2, $3, $4::jsonb)
		RETURNING nonce, install_id, kind, created_at, delivered_at, acked_at
	`, nonce, installID, kind, nullableJSON(params)))
	if err != nil {
		return TesterCommandState{}, fmt.Errorf("queue command: %w", err)
	}
	c.settle(time.Now())
	return c, nil
}

func (s *DBTesterStore) TakeCommands(ctx context.Context, installID string) ([]TesterCommand, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE support_tester_commands SET delivered_at = now()
		WHERE install_id = $1 AND delivered_at IS NULL AND created_at > $2
		RETURNING kind, nonce, params
	`, installID, time.Now().Add(-TesterCommandTTL))
	if err != nil {
		return nil, fmt.Errorf("take commands: %w", err)
	}
	defer rows.Close()
	var out []TesterCommand
	for rows.Next() {
		var c TesterCommand
		if err := rows.Scan(&c.Kind, &c.Nonce, &c.Params); err != nil {
			return nil, fmt.Errorf("scan command: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *DBTesterStore) AckCommands(ctx context.Context, installID string, nonces []string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE support_tester_commands SET acked_at = now()
		WHERE install_id = $1 AND nonce = ANY($2) AND acked_at IS NULL
	`, installID, nonces); err != nil {
		return fmt.Errorf("ack commands: %w", err)
	}
	return nil
}

func (s *DBTesterStore) ReadCommand(ctx context.Context, installID, nonce string) (TesterCommandState, error) {
	c, err := scanCommand(s.pool.QueryRow(ctx, `
		SELECT nonce, install_id, kind, created_at, delivered_at, acked_at
		FROM support_tester_commands WHERE install_id = $1 AND nonce = $2
	`, installID, nonce))
	if errors.Is(err, pgx.ErrNoRows) {
		return TesterCommandState{}, ErrCommandNotFound
	}
	if err != nil {
		return TesterCommandState{}, fmt.Errorf("read command: %w", err)
	}
	c.settle(time.Now())
	return c, nil
}
