package support

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReportSubmissionAndRateLimit(t *testing.T) {
	store := NewMemStore()
	limiter := NewLimiter(2, time.Minute)

	mux := http.NewServeMux()
	RegisterPublicRoutes(mux, store, limiter)

	payload := ReportRequest{
		InstallID: "r-test-123",
		Platform:  "macos",
		Version:   "1.0.10",
		Message:   "Не удается подключиться к серверу",
		Logs:      "=== Hikkari Diagnostics Report ===\n[proxy] dial failed: timeout",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	// 1st request should succeed (201)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/support/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("first request code = %d, want %d; body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var resp struct {
		OK     bool   `json:"ok"`
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.ID <= 0 || resp.Status != "open" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// 2nd request should succeed (201)
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/support/report", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("second request code = %d, want %d", rec2.Code, http.StatusCreated)
	}

	// 3rd request should be blocked by rate limiter (429)
	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/support/report", bytes.NewReader(body))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("third request code = %d, want %d (rate limited)", rec3.Code, http.StatusTooManyRequests)
	}

	// Another install_id should still be allowed
	payload2 := payload
	payload2.InstallID = "r-different-client"
	body2, _ := json.Marshal(payload2)
	reqOther := httptest.NewRequest(http.MethodPost, "/api/v1/support/report", bytes.NewReader(body2))
	reqOther.Header.Set("Content-Type", "application/json")
	recOther := httptest.NewRecorder()
	mux.ServeHTTP(recOther, reqOther)

	if recOther.Code != http.StatusCreated {
		t.Fatalf("different install_id request code = %d, want %d", recOther.Code, http.StatusCreated)
	}
}

func TestReportValidation(t *testing.T) {
	store := NewMemStore()
	mux := http.NewServeMux()
	RegisterPublicRoutes(mux, store, nil)

	tests := []struct {
		name    string
		payload string
		want    int
	}{
		{
			name:    "empty install_id",
			payload: `{"install_id":"","message":"help"}`,
			want:    http.StatusBadRequest,
		},
		{
			name:    "empty message",
			payload: `{"install_id":"r-123","message":""}`,
			want:    http.StatusBadRequest,
		},
		{
			name:    "malformed json",
			payload: `{"install_id":`,
			want:    http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/support/report", strings.NewReader(tc.payload))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s: got code %d, want %d", tc.name, rec.Code, tc.want)
			}
		})
	}
}

func TestAdminRoutes(t *testing.T) {
	store := NewMemStore()
	mux := http.NewServeMux()

	passthrough := func(next http.HandlerFunc) http.HandlerFunc {
		return next
	}
	RegisterAdminRoutes(mux, store, passthrough)

	// Create 2 test reports
	rep1, _ := store.CreateReport(nil, ReportRequest{
		InstallID: "r-user-1",
		Platform:  "macos",
		Version:   "1.0.10",
		Message:   "Первая проблема",
		Logs:      "log line 1\nlog line 2",
	})
	rep2, _ := store.CreateReport(nil, ReportRequest{
		InstallID: "r-user-2",
		Platform:  "windows",
		Version:   "1.0.9",
		Message:   "Вторая проблема",
		Logs:      "log line A\nlog line B",
	})

	// 1. List reports
	reqList := httptest.NewRequest(http.MethodGet, "/api/v1/admin/support/reports", nil)
	recList := httptest.NewRecorder()
	mux.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("list code = %d, want %d", recList.Code, http.StatusOK)
	}

	var listResp struct {
		Reports []*Report `json:"reports"`
	}
	if err := json.Unmarshal(recList.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Reports) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(listResp.Reports))
	}

	// 2. Get single report
	reqGet := httptest.NewRequest(http.MethodGet, "/api/v1/admin/support/reports/"+strconv.FormatInt(rep1.ID, 10), nil)
	recGet := httptest.NewRecorder()
	mux.ServeHTTP(recGet, reqGet)

	if recGet.Code != http.StatusOK {
		t.Fatalf("get code = %d, want %d", recGet.Code, http.StatusOK)
	}

	var getResp struct {
		Report *Report `json:"report"`
	}
	if err := json.Unmarshal(recGet.Body.Bytes(), &getResp); err != nil {
		t.Fatal(err)
	}
	if getResp.Report.ID != rep1.ID || getResp.Report.Message != "Первая проблема" {
		t.Fatalf("unexpected report detail: %+v", getResp.Report)
	}
	if len(getResp.Report.Messages) != 1 || getResp.Report.Messages[0].Staff {
		t.Fatalf("expected 1 user message, got %+v", getResp.Report.Messages)
	}

	// 3. Reply to report
	replyPayload := `{"text":"Попробуйте перезапустить приложение"}`
	reqReply := httptest.NewRequest(http.MethodPost, "/api/v1/admin/support/reports/"+strconv.FormatInt(rep1.ID, 10)+"/reply", strings.NewReader(replyPayload))
	recReply := httptest.NewRecorder()
	mux.ServeHTTP(recReply, reqReply)

	if recReply.Code != http.StatusOK {
		t.Fatalf("reply code = %d, want %d; body: %s", recReply.Code, http.StatusOK, recReply.Body.String())
	}

	var replyResp struct {
		OK     bool    `json:"ok"`
		Report *Report `json:"report"`
	}
	if err := json.Unmarshal(recReply.Body.Bytes(), &replyResp); err != nil {
		t.Fatal(err)
	}
	if replyResp.Report.Status != "answered" {
		t.Fatalf("status after reply = %s, want answered", replyResp.Report.Status)
	}
	if len(replyResp.Report.Messages) != 2 || !replyResp.Report.Messages[1].Staff {
		t.Fatalf("expected 2 messages with 2nd being staff, got: %+v", replyResp.Report.Messages)
	}

	// 4. Update status to closed
	statusPayload := `{"status":"closed"}`
	reqStatus := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/support/reports/"+strconv.FormatInt(rep2.ID, 10), strings.NewReader(statusPayload))
	recStatus := httptest.NewRecorder()
	mux.ServeHTTP(recStatus, reqStatus)

	if recStatus.Code != http.StatusOK {
		t.Fatalf("status update code = %d, want %d", recStatus.Code, http.StatusOK)
	}

	var statusResp struct {
		OK     bool    `json:"ok"`
		Report *Report `json:"report"`
	}
	if err := json.Unmarshal(recStatus.Body.Bytes(), &statusResp); err != nil {
		t.Fatal(err)
	}
	if statusResp.Report.Status != "closed" {
		t.Fatalf("expected closed status, got %s", statusResp.Report.Status)
	}

	// 5. Filter list by open status
	reqOpenList := httptest.NewRequest(http.MethodGet, "/api/v1/admin/support/reports?status=open", nil)
	recOpenList := httptest.NewRecorder()
	mux.ServeHTTP(recOpenList, reqOpenList)

	var openResp struct {
		Reports []*Report `json:"reports"`
	}
	_ = json.Unmarshal(recOpenList.Body.Bytes(), &openResp)
	// rep1 is answered, rep2 is closed -> 0 open reports
	if len(openResp.Reports) != 0 {
		t.Fatalf("expected 0 open reports, got %d", len(openResp.Reports))
	}
}

func TestReportDeduplication(t *testing.T) {
	store := NewMemStore()
	mux := http.NewServeMux()
	RegisterPublicRoutes(mux, store, nil)

	payload1 := `{"install_id":"r-dup-user","platform":"macos","version":"1.0.10","message":"Error 1","logs":"logs v1"}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/support/report", strings.NewReader(payload1))
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)

	var resp1 struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)

	// Send duplicate report from same install_id
	payload2 := `{"install_id":"r-dup-user","platform":"macos","version":"1.0.10","message":"Error 2","logs":"logs v2"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/support/report", strings.NewReader(payload2))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	var resp2 struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)

	// IDs must be identical: same ticket reused!
	if resp1.ID != resp2.ID {
		t.Fatalf("expected duplicate to reuse ID %d, got %d", resp1.ID, resp2.ID)
	}

	reports, _ := store.ListReports(nil, "all")
	if len(reports) != 1 {
		t.Fatalf("expected exactly 1 ticket in store, got %d", len(reports))
	}
	if reports[0].Logs != "logs v2" {
		t.Fatalf("expected logs to be updated to 'logs v2', got %q", reports[0].Logs)
	}
	if len(reports[0].Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(reports[0].Messages))
	}
	if reports[0].Messages[0].Logs != "logs v1" {
		t.Fatalf("expected msg 0 to retain 'logs v1', got %q", reports[0].Messages[0].Logs)
	}
	if reports[0].Messages[1].Logs != "logs v2" {
		t.Fatalf("expected msg 1 to have 'logs v2', got %q", reports[0].Messages[1].Logs)
	}
}

func TestPurgeOldReports(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()

	// Create an old report
	rep, err := store.CreateReport(ctx, ReportRequest{
		InstallID: "old-user",
		Platform:  "macos",
		Version:   "1.0.0",
		Message:   "Old error",
		Logs:      "old logs",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Backdate the report to 40 days ago
	store.mu.Lock()
	store.reports[rep.ID].CreatedAt = time.Now().Add(-40 * 24 * time.Hour)
	store.mu.Unlock()

	// Purge with 30-day cutoff
	purged, err := store.PurgeOldReports(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("purge error: %v", err)
	}
	if purged != 1 {
		t.Fatalf("expected 1 purged report, got %d", purged)
	}

	remaining, _ := store.ListReports(ctx, "all")
	if len(remaining) != 1 {
		t.Fatalf("expected 1 report remaining (history preserved), got %d", len(remaining))
	}
	if remaining[0].Logs != "[лог старше 30 дней очищен]" {
		t.Fatalf("expected logs to be wiped, got %q", remaining[0].Logs)
	}
	if remaining[0].Message != "Old error" {
		t.Fatalf("expected message text to be preserved, got %q", remaining[0].Message)
	}
}
