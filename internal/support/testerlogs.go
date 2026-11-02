package support

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Tester log stream. An install with the tester switch on ships its
// sanitized engine log here in chunks (hikkari-client engine/core/testerlog),
// so a field problem can be read without asking the tester for a report.

const (
	maxTesterChunkBody = 1 << 20 // 1MB: one minute of the noisiest session fits
	// TesterLogRetention is how long chunks are kept.
	TesterLogRetention = 7 * 24 * time.Hour
	// maxTesterLogRead caps how many chunks one admin read concatenates.
	maxTesterLogRead = 500
)

// TesterChunkRequest is one batch of log lines from a tester install.
type TesterChunkRequest struct {
	InstallID string `json:"install_id"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	// Lines is the batch, oldest first, already sanitized by the client.
	Lines []string `json:"lines"`
	// Dropped counts lines the client discarded because its buffer was full
	// (hub unreachable for a long time); shown so a gap is not misread.
	Dropped int `json:"dropped"`
	// Heartbeat marks a call with nothing to ship: the client is on the line and
	// asks for commands. It updates the last signal and stores no chunk.
	Heartbeat bool `json:"heartbeat,omitempty"`
	// Acks names the commands whose result ends in this batch.
	Acks []string `json:"acks,omitempty"`
}

// TesterInstall summarizes one install's stream for the admin list.
type TesterInstall struct {
	// Alias comes first everywhere: tester-001, or the operator's own name.
	Alias     string    `json:"alias"`
	InstallID string    `json:"install_id"`
	Note      string    `json:"note"`
	Platform  string    `json:"platform"`
	Version   string    `json:"version"`
	Chunks    int       `json:"chunks"`
	Lines     int       `json:"lines"`
	Dropped   int       `json:"dropped"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Online: a signal within TesterOnlineWindow, so a command would reach it.
	Online bool `json:"online"`
}

// TesterChunk is one stored batch.
type TesterChunk struct {
	ID         int64     `json:"id"`
	Platform   string    `json:"platform"`
	Version    string    `json:"version"`
	Lines      int       `json:"lines"`
	Dropped    int       `json:"dropped"`
	Body       string    `json:"body"`
	ReceivedAt time.Time `json:"received_at"`
}

// TesterStore keeps tester log chunks.
type TesterStore interface {
	// AppendChunk records the install's signal, naming it on first sight, and
	// stores the batch if it carries one.
	AppendChunk(ctx context.Context, req TesterChunkRequest) error
	// ListInstalls returns installs heard from within retention, latest first.
	ListInstalls(ctx context.Context) ([]TesterInstall, error)
	// ResolveInstall finds the install a reference names (aliases.go).
	ResolveInstall(ctx context.Context, ref string) (installID, alias string, err error)
	RenameAlias(ctx context.Context, installID, alias, note string) (string, error)
	// EnqueueCommand queues a command, or returns the same kind still open.
	EnqueueCommand(ctx context.Context, installID, kind string, params json.RawMessage) (TesterCommandState, error)
	// TakeCommands hands over what waits for the install and marks it delivered.
	TakeCommands(ctx context.Context, installID string) ([]TesterCommand, error)
	AckCommands(ctx context.Context, installID string, nonces []string) error
	ReadCommand(ctx context.Context, installID, nonce string) (TesterCommandState, error)
	// ReadChunks returns the install's chunks received in (since, until],
	// oldest first, at most limit of the newest ones. A zero until means now.
	ReadChunks(ctx context.Context, installID string, since, until time.Time, limit int) ([]TesterChunk, error)
	// ReadReachLines returns the install's chunks received in (since, until],
	// oldest first, each holding only its [diag:reach] lines. Chunks without one
	// are left out, so reading days of them does not move days of log.
	ReadReachLines(ctx context.Context, installID string, since, until time.Time) ([]TesterChunk, error)
	PurgeChunks(ctx context.Context, olderThan time.Duration) (int64, error)
}

// NewTesterStore returns a PostgreSQL-backed store if pool is provided, or an in-memory store otherwise.
func NewTesterStore(pool db.Pool) TesterStore {
	if pool != nil {
		return &DBTesterStore{pool: pool}
	}
	return NewMemTesterStore()
}

// RegisterTesterRoutes mounts the client-facing tester log intake.
func RegisterTesterRoutes(mux *http.ServeMux, store TesterStore, limiter *Limiter) {
	mux.HandleFunc("POST /api/v1/support/tester-logs", TesterIntakeHandler(store, limiter))
}

// RegisterTesterAdminRoutes mounts operator-facing tester log reads.
func RegisterTesterAdminRoutes(mux *http.ServeMux, store TesterStore, gate func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/support/tester-relays", gate(TesterRelaysHandler(store)))
	mux.HandleFunc("GET /api/v1/admin/support/tester-logs", gate(TesterListHandler(store)))
	mux.HandleFunc("GET /api/v1/admin/support/tester-logs/{install}", gate(TesterReadHandler(store)))
	mux.HandleFunc("GET /api/v1/admin/support/tester-logs/{install}/export", gate(TesterExportHandler(store)))
	mux.HandleFunc("PUT /api/v1/admin/support/tester-logs/{install}/alias", gate(AliasRenameHandler(store)))
	mux.HandleFunc("POST /api/v1/admin/support/tester-logs/{install}/commands", gate(TesterCommandRequestHandler(store)))
	mux.HandleFunc("GET /api/v1/admin/support/tester-logs/{install}/commands/{nonce}", gate(TesterCommandStatusHandler(store)))
}

// RegisterTesterOpsRoutes mounts what the agent's token may do: read logs, ask
// for a diagnostic, rename. A separate path, so the token has no way into any
// other admin route — those read only the panel's cookie. gate takes the audit
// action; an empty one is a read not worth a row (a list, a poll).
func RegisterTesterOpsRoutes(mux *http.ServeMux, store TesterStore, gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/testers", gate("", TesterListHandler(store)))
	mux.HandleFunc("GET /api/v1/ops/testers/{install}/export", gate("ops.tester_logs.read", TesterExportHandler(store)))
	mux.HandleFunc("PUT /api/v1/ops/testers/{install}/alias", gate("ops.tester.rename", AliasRenameHandler(store)))
	mux.HandleFunc("POST /api/v1/ops/testers/{install}/commands", gate("ops.tester.request_diag", TesterCommandRequestHandler(store)))
	mux.HandleFunc("GET /api/v1/ops/testers/{install}/commands/{nonce}", gate("", TesterCommandStatusHandler(store)))
}

// TesterIntakeHandler accepts one chunk from a tester install.
func TesterIntakeHandler(store TesterStore, limiter *Limiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req TesterChunkRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTesterChunkBody)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		req.InstallID = strings.TrimSpace(req.InstallID)
		req.Platform = strings.TrimSpace(req.Platform)
		req.Version = strings.TrimSpace(req.Version)
		if req.InstallID == "" {
			writeErr(w, http.StatusBadRequest, "install_id is required")
			return
		}
		if len(req.Lines) == 0 && req.Dropped == 0 && !req.Heartbeat && len(req.Acks) == 0 {
			writeErr(w, http.StatusBadRequest, "lines are required")
			return
		}
		if req.Dropped < 0 {
			req.Dropped = 0
		}
		if req.Platform == "" {
			req.Platform = "unknown"
		}
		if req.Version == "" {
			req.Version = "unknown"
		}
		if limiter != nil && !limiter.Allow(req.InstallID) {
			writeErr(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		if err := store.AppendChunk(r.Context(), req); err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to save chunk")
			return
		}
		// The batch is saved: a failure below costs a command, which its caller
		// sees as a timeout, not the lines, which the client would send again.
		if len(req.Acks) > 0 {
			if err := store.AckCommands(r.Context(), req.InstallID, req.Acks); err != nil {
				log.Printf("support: ack tester commands: %v", err)
			}
		}
		commands, err := store.TakeCommands(r.Context(), req.InstallID)
		commands = unsealCommands(commands)
		if err != nil {
			log.Printf("support: take tester commands: %v", err)
		}
		resp := map[string]any{"ok": true}
		if len(commands) > 0 {
			resp["commands"] = commands
		}
		writeJSON(w, http.StatusCreated, resp)
	}
}

// TesterListHandler lists installs that streamed logs within retention.
func TesterListHandler(store TesterStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		installs, err := store.ListInstalls(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to list tester installs")
			return
		}
		if installs == nil {
			installs = []TesterInstall{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"installs": installs})
	}
}

// TesterReadHandler returns an install's chunks. Query: since (unix seconds,
// optional), limit (chunks, optional).
func TesterReadHandler(store TesterStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		install, _, err := store.ResolveInstall(r.Context(), r.PathValue("install"))
		if err != nil {
			writeResolveErr(w, err)
			return
		}
		var since time.Time
		if v := r.URL.Query().Get("since"); v != "" {
			sec, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid since")
				return
			}
			since = time.Unix(sec, 0)
		}
		limit := maxTesterLogRead
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				writeErr(w, http.StatusBadRequest, "invalid limit")
				return
			}
			limit = min(n, maxTesterLogRead)
		}
		chunks, err := store.ReadChunks(r.Context(), install, since, time.Time{}, limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to read tester logs")
			return
		}
		if chunks == nil {
			chunks = []TesterChunk{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"chunks": chunks})
	}
}

// ============================================================================
// In-Memory Store
// ============================================================================

type memTesterChunk struct {
	install string
	chunk   TesterChunk
}

type memInstall struct {
	alias, note, platform, version string
	firstSeen, lastSeen            time.Time
}

type MemTesterStore struct {
	mu       sync.RWMutex
	chunks   []memTesterChunk
	nextID   int64
	installs map[string]*memInstall
	aliasSeq int
	commands []*TesterCommandState
	// now is the clock; tests move it.
	now func() time.Time
}

func NewMemTesterStore() *MemTesterStore {
	return &MemTesterStore{nextID: 1, installs: map[string]*memInstall{}, now: time.Now}
}

func (s *MemTesterStore) AppendChunk(_ context.Context, req TesterChunkRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touchInstall(req)
	if len(req.Lines) == 0 && req.Dropped == 0 {
		return nil
	}
	s.chunks = append(s.chunks, memTesterChunk{install: req.InstallID, chunk: TesterChunk{
		ID:         s.nextID,
		Platform:   req.Platform,
		Version:    req.Version,
		Lines:      len(req.Lines),
		Dropped:    req.Dropped,
		Body:       strings.Join(req.Lines, "\n"),
		ReceivedAt: time.Now(),
	}})
	s.nextID++
	return nil
}

func (s *MemTesterStore) ListInstalls(_ context.Context) ([]TesterInstall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.now()
	out := make([]TesterInstall, 0, len(s.installs))
	for id, m := range s.installs {
		if now.Sub(m.lastSeen) > TesterLogRetention {
			continue
		}
		in := TesterInstall{
			Alias: m.alias, InstallID: id, Note: m.note, Platform: m.platform, Version: m.version,
			FirstSeen: m.firstSeen, LastSeen: m.lastSeen, Online: now.Sub(m.lastSeen) <= TesterOnlineWindow,
		}
		for _, c := range s.chunks {
			if c.install != id {
				continue
			}
			in.Chunks++
			in.Lines += c.chunk.Lines
			in.Dropped += c.chunk.Dropped
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out, nil
}

func (s *MemTesterStore) ReadChunks(_ context.Context, installID string, since, until time.Time, limit int) ([]TesterChunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if until.IsZero() {
		until = time.Now()
	}
	var out []TesterChunk
	for _, c := range s.chunks {
		if c.install == installID && c.chunk.ReceivedAt.After(since) && !c.chunk.ReceivedAt.After(until) {
			out = append(out, c.chunk)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (s *MemTesterStore) ReadReachLines(_ context.Context, installID string, since, until time.Time) ([]TesterChunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []TesterChunk
	for _, c := range s.chunks {
		if c.install != installID || !c.chunk.ReceivedAt.After(since) || c.chunk.ReceivedAt.After(until) {
			continue
		}
		var kept []string
		for _, line := range strings.Split(c.chunk.Body, "\n") {
			if strings.Contains(line, "[diag:reach] ") {
				kept = append(kept, line)
			}
		}
		if len(kept) > 0 {
			chunk := c.chunk
			chunk.Body = strings.Join(kept, "\n")
			out = append(out, chunk)
		}
	}
	return out, nil
}

func (s *MemTesterStore) PurgeChunks(_ context.Context, olderThan time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-olderThan)
	kept := s.chunks[:0]
	var n int64
	for _, c := range s.chunks {
		if c.chunk.ReceivedAt.Before(cutoff) {
			n++
			continue
		}
		kept = append(kept, c)
	}
	s.chunks = kept
	return n, nil
}

// ============================================================================
// PostgreSQL-backed Store
// ============================================================================

type DBTesterStore struct {
	pool db.Pool
}

func (s *DBTesterStore) AppendChunk(ctx context.Context, req TesterChunkRequest) error {
	if err := touchInstallDB(ctx, s.pool, req); err != nil {
		return err
	}
	if len(req.Lines) == 0 && req.Dropped == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO support_tester_log_chunks (install_id, platform, version, lines, dropped, body)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, req.InstallID, req.Platform, req.Version, len(req.Lines), req.Dropped, strings.Join(req.Lines, "\n"))
	if err != nil {
		return fmt.Errorf("append tester chunk: %w", err)
	}
	return nil
}

func (s *DBTesterStore) ListInstalls(ctx context.Context) ([]TesterInstall, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.alias, a.install_id, a.note, a.platform, a.version,
		       COALESCE(c.chunks, 0), COALESCE(c.lines, 0), COALESCE(c.dropped, 0),
		       COALESCE(c.first_seen, a.created_at), a.last_seen_at
		FROM support_install_aliases a
		LEFT JOIN (
			SELECT install_id, count(*) AS chunks, sum(lines) AS lines, sum(dropped) AS dropped,
			       min(received_at) AS first_seen
			FROM support_tester_log_chunks
			GROUP BY install_id
		) c USING (install_id)
		WHERE a.last_seen_at > $1
		ORDER BY a.last_seen_at DESC
	`, time.Now().Add(-TesterLogRetention))
	if err != nil {
		return nil, fmt.Errorf("list tester installs: %w", err)
	}
	defer rows.Close()
	now := time.Now()
	var out []TesterInstall
	for rows.Next() {
		var in TesterInstall
		if err := rows.Scan(&in.Alias, &in.InstallID, &in.Note, &in.Platform, &in.Version, &in.Chunks, &in.Lines, &in.Dropped, &in.FirstSeen, &in.LastSeen); err != nil {
			return nil, fmt.Errorf("scan tester install: %w", err)
		}
		in.Online = now.Sub(in.LastSeen) <= TesterOnlineWindow
		out = append(out, in)
	}
	return out, rows.Err()
}

func (s *DBTesterStore) ReadChunks(ctx context.Context, installID string, since, until time.Time, limit int) ([]TesterChunk, error) {
	if until.IsZero() {
		until = time.Now()
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, platform, version, lines, dropped, body, received_at FROM (
			SELECT * FROM support_tester_log_chunks
			WHERE install_id = $1 AND received_at > $2 AND received_at <= $3
			ORDER BY received_at DESC
			LIMIT $4
		) newest ORDER BY received_at ASC
	`, installID, since, until, limit)
	if err != nil {
		return nil, fmt.Errorf("read tester chunks: %w", err)
	}
	defer rows.Close()
	var out []TesterChunk
	for rows.Next() {
		var c TesterChunk
		if err := rows.Scan(&c.ID, &c.Platform, &c.Version, &c.Lines, &c.Dropped, &c.Body, &c.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan tester chunk: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *DBTesterStore) ReadReachLines(ctx context.Context, installID string, since, until time.Time) ([]TesterChunk, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, platform, version, lines, dropped, received_at,
		       array_to_string(ARRAY(
		           SELECT line FROM unnest(string_to_array(body, E'\n')) AS line
		           WHERE line LIKE '%[diag:reach] %'
		       ), E'\n')
		FROM support_tester_log_chunks
		WHERE install_id = $1 AND received_at > $2 AND received_at <= $3
		  AND body LIKE '%[diag:reach] %'
		ORDER BY received_at ASC
	`, installID, since, until)
	if err != nil {
		return nil, fmt.Errorf("read tester reach lines: %w", err)
	}
	defer rows.Close()
	var out []TesterChunk
	for rows.Next() {
		var c TesterChunk
		if err := rows.Scan(&c.ID, &c.Platform, &c.Version, &c.Lines, &c.Dropped, &c.ReceivedAt, &c.Body); err != nil {
			return nil, fmt.Errorf("scan tester reach lines: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *DBTesterStore) PurgeChunks(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM support_tester_log_chunks WHERE received_at < $1`, time.Now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("purge tester chunks: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM support_tester_commands WHERE created_at < $1`, time.Now().Add(-olderThan)); err != nil {
		return 0, fmt.Errorf("purge tester commands: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AgeInstallForTest moves an install's last signal into the past.
func (s *MemTesterStore) AgeInstallForTest(installID string, by time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in := s.installs[installID]; in != nil {
		in.lastSeen = in.lastSeen.Add(-by)
	}
}
