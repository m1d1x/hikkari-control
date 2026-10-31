package support

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/m1d1x/hikkari-control/internal/db"
)

var (
	ErrNotFound      = errors.New("report not found")
	ErrInvalidStatus = errors.New("invalid status, must be open, answered, or closed")
)

// Store defines operations for support reports.
type Store interface {
	CreateReport(ctx context.Context, req ReportRequest) (*Report, error)
	ListReports(ctx context.Context, status string) ([]*Report, error)
	GetReport(ctx context.Context, id int64) (*Report, error)
	AddReply(ctx context.Context, id int64, text string) (*Report, error)
	UpdateStatus(ctx context.Context, id int64, status string) (*Report, error)
}

// NewStore returns a PostgreSQL-backed store if pool is provided, or an in-memory store otherwise.
func NewStore(pool db.Pool) Store {
	if pool != nil {
		return NewDBStore(pool)
	}
	return NewMemStore()
}

// ============================================================================
// In-Memory Store
// ============================================================================

type MemStore struct {
	mu      sync.RWMutex
	reports map[int64]*Report
	nextID  int64
}

func NewMemStore() *MemStore {
	return &MemStore{
		reports: make(map[int64]*Report),
		nextID:  1,
	}
}

func (s *MemStore) CreateReport(_ context.Context, req ReportRequest) (*Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	rep := &Report{
		ID:        s.nextID,
		InstallID: req.InstallID,
		From:      req.InstallID,
		Platform:  req.Platform,
		Version:   req.Version,
		Message:   req.Message,
		Logs:      req.Logs,
		Status:    "open",
		Messages: []Message{
			{
				Staff: false,
				Text:  req.Message,
				TS:    now.UnixMilli(),
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.reports[rep.ID] = rep
	s.nextID++

	return cloneReport(rep), nil
}

func (s *MemStore) ListReports(_ context.Context, status string) ([]*Report, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Report
	for _, rep := range s.reports {
		if status == "" || status == "all" || rep.Status == status {
			result = append(result, cloneReport(rep))
		}
	}

	// Sort descending by ID
	for i := 0; i < len(result)-1; i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i].ID < result[j].ID {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	return result, nil
}

func (s *MemStore) GetReport(_ context.Context, id int64) (*Report, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rep, ok := s.reports[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneReport(rep), nil
}

func (s *MemStore) AddReply(_ context.Context, id int64, text string) (*Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rep, ok := s.reports[id]
	if !ok {
		return nil, ErrNotFound
	}

	now := time.Now()
	rep.Messages = append(rep.Messages, Message{
		Staff: true,
		Text:  text,
		TS:    now.UnixMilli(),
	})
	rep.Status = "answered"
	rep.UpdatedAt = now

	return cloneReport(rep), nil
}

func (s *MemStore) UpdateStatus(_ context.Context, id int64, status string) (*Report, error) {
	if status != "open" && status != "answered" && status != "closed" {
		return nil, ErrInvalidStatus
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rep, ok := s.reports[id]
	if !ok {
		return nil, ErrNotFound
	}

	rep.Status = status
	rep.UpdatedAt = time.Now()

	return cloneReport(rep), nil
}

func cloneReport(r *Report) *Report {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Messages = make([]Message, len(r.Messages))
	copy(cp.Messages, r.Messages)
	return &cp
}

// ============================================================================
// PostgreSQL-backed Store
// ============================================================================

type DBStore struct {
	pool db.Pool
}

func NewDBStore(pool db.Pool) *DBStore {
	return &DBStore{pool: pool}
}

func (s *DBStore) CreateReport(ctx context.Context, req ReportRequest) (*Report, error) {
	now := time.Now()
	initMsg := []Message{
		{
			Staff: false,
			Text:  req.Message,
			TS:    now.UnixMilli(),
		},
	}
	msgsJSON, err := json.Marshal(initMsg)
	if err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO support_reports (install_id, platform, version, message, logs, status, messages, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'open', $6, $7, $7)
		RETURNING id, created_at, updated_at
	`, req.InstallID, req.Platform, req.Version, req.Message, req.Logs, msgsJSON, now)

	rep := &Report{
		InstallID: req.InstallID,
		From:      req.InstallID,
		Platform:  req.Platform,
		Version:   req.Version,
		Message:   req.Message,
		Logs:      req.Logs,
		Status:    "open",
		Messages:  initMsg,
	}

	if err := row.Scan(&rep.ID, &rep.CreatedAt, &rep.UpdatedAt); err != nil {
		return nil, fmt.Errorf("create report: %w", err)
	}

	return rep, nil
}

func (s *DBStore) ListReports(ctx context.Context, status string) ([]*Report, error) {
	var rows pgx.Rows
	var err error

	if status != "" && status != "all" {
		rows, err = s.pool.Query(ctx, `
			SELECT id, install_id, platform, version, message, logs, status, messages, created_at, updated_at
			FROM support_reports
			WHERE status = $1
			ORDER BY id DESC
			LIMIT 200
		`, status)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id, install_id, platform, version, message, logs, status, messages, created_at, updated_at
			FROM support_reports
			ORDER BY id DESC
			LIMIT 200
		`)
	}
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	defer rows.Close()

	var reports []*Report
	for rows.Next() {
		rep := &Report{}
		var msgsData []byte
		if err := rows.Scan(
			&rep.ID, &rep.InstallID, &rep.Platform, &rep.Version,
			&rep.Message, &rep.Logs, &rep.Status, &msgsData,
			&rep.CreatedAt, &rep.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan report: %w", err)
		}
		rep.From = rep.InstallID
		if len(msgsData) > 0 {
			_ = json.Unmarshal(msgsData, &rep.Messages)
		}
		reports = append(reports, rep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reports: %w", err)
	}

	return reports, nil
}

func (s *DBStore) GetReport(ctx context.Context, id int64) (*Report, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, install_id, platform, version, message, logs, status, messages, created_at, updated_at
		FROM support_reports
		WHERE id = $1
	`, id)

	rep := &Report{}
	var msgsData []byte
	if err := row.Scan(
		&rep.ID, &rep.InstallID, &rep.Platform, &rep.Version,
		&rep.Message, &rep.Logs, &rep.Status, &msgsData,
		&rep.CreatedAt, &rep.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get report: %w", err)
	}

	rep.From = rep.InstallID
	if len(msgsData) > 0 {
		_ = json.Unmarshal(msgsData, &rep.Messages)
	}

	return rep, nil
}

func (s *DBStore) AddReply(ctx context.Context, id int64, text string) (*Report, error) {
	rep, err := s.GetReport(ctx, id)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	rep.Messages = append(rep.Messages, Message{
		Staff: true,
		Text:  text,
		TS:    now.UnixMilli(),
	})
	rep.Status = "answered"
	rep.UpdatedAt = now

	msgsJSON, err := json.Marshal(rep.Messages)
	if err != nil {
		return nil, err
	}

	_, err = s.pool.Exec(ctx, `
		UPDATE support_reports
		SET status = 'answered', messages = $1, updated_at = $2
		WHERE id = $3
	`, msgsJSON, now, id)
	if err != nil {
		return nil, fmt.Errorf("update report reply: %w", err)
	}

	return rep, nil
}

func (s *DBStore) UpdateStatus(ctx context.Context, id int64, status string) (*Report, error) {
	if status != "open" && status != "answered" && status != "closed" {
		return nil, ErrInvalidStatus
	}

	now := time.Now()
	tag, err := s.pool.Exec(ctx, `
		UPDATE support_reports
		SET status = $1, updated_at = $2
		WHERE id = $3
	`, status, now, id)
	if err != nil {
		return nil, fmt.Errorf("update report status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}

	return s.GetReport(ctx, id)
}
