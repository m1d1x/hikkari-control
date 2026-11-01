package support

import (
	"context"
	"encoding/json"
	"fmt"
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
}

// TesterInstall summarizes one install's stream for the admin list.
type TesterInstall struct {
	InstallID string    `json:"install_id"`
	Platform  string    `json:"platform"`
	Version   string    `json:"version"`
	Chunks    int       `json:"chunks"`
	Lines     int       `json:"lines"`
	Dropped   int       `json:"dropped"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
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
	AppendChunk(ctx context.Context, req TesterChunkRequest) error
	ListInstalls(ctx context.Context) ([]TesterInstall, error)
	// ReadChunks returns the install's chunks received in (since, until],
	// oldest first, at most limit of the newest ones. A zero until means now.
	ReadChunks(ctx context.Context, installID string, since, until time.Time, limit int) ([]TesterChunk, error)
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
	mux.HandleFunc("GET /api/v1/admin/support/tester-logs", gate(TesterListHandler(store)))
	mux.HandleFunc("GET /api/v1/admin/support/tester-logs/{install}", gate(TesterReadHandler(store)))
	mux.HandleFunc("GET /api/v1/admin/support/tester-logs/{install}/export", gate(TesterExportHandler(store)))
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
		if len(req.Lines) == 0 && req.Dropped == 0 {
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
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
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
		install := strings.TrimSpace(r.PathValue("install"))
		if install == "" {
			writeErr(w, http.StatusBadRequest, "install is required")
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

type MemTesterStore struct {
	mu     sync.RWMutex
	chunks []memTesterChunk
	nextID int64
}

func NewMemTesterStore() *MemTesterStore { return &MemTesterStore{nextID: 1} }

func (s *MemTesterStore) AppendChunk(_ context.Context, req TesterChunkRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	byID := map[string]*TesterInstall{}
	for _, c := range s.chunks {
		in := byID[c.install]
		if in == nil {
			in = &TesterInstall{InstallID: c.install, FirstSeen: c.chunk.ReceivedAt}
			byID[c.install] = in
		}
		in.Platform, in.Version, in.LastSeen = c.chunk.Platform, c.chunk.Version, c.chunk.ReceivedAt
		in.Chunks++
		in.Lines += c.chunk.Lines
		in.Dropped += c.chunk.Dropped
	}
	out := make([]TesterInstall, 0, len(byID))
	for _, in := range byID {
		out = append(out, *in)
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
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tester_log_chunks (install_id, platform, version, lines, dropped, body)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, req.InstallID, req.Platform, req.Version, len(req.Lines), req.Dropped, strings.Join(req.Lines, "\n"))
	if err != nil {
		return fmt.Errorf("append tester chunk: %w", err)
	}
	return nil
}

func (s *DBTesterStore) ListInstalls(ctx context.Context) ([]TesterInstall, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT install_id,
		       (array_agg(platform ORDER BY received_at DESC))[1],
		       (array_agg(version ORDER BY received_at DESC))[1],
		       count(*), sum(lines), sum(dropped), min(received_at), max(received_at)
		FROM tester_log_chunks
		GROUP BY install_id
		ORDER BY max(received_at) DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list tester installs: %w", err)
	}
	defer rows.Close()
	var out []TesterInstall
	for rows.Next() {
		var in TesterInstall
		if err := rows.Scan(&in.InstallID, &in.Platform, &in.Version, &in.Chunks, &in.Lines, &in.Dropped, &in.FirstSeen, &in.LastSeen); err != nil {
			return nil, fmt.Errorf("scan tester install: %w", err)
		}
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
			SELECT * FROM tester_log_chunks
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

func (s *DBTesterStore) PurgeChunks(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tester_log_chunks WHERE received_at < $1`, time.Now().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("purge tester chunks: %w", err)
	}
	return tag.RowsAffected(), nil
}
