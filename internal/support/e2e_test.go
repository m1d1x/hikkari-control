package support

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestEndToEndSupportDiagnosticsFlow(t *testing.T) {
	// 1. Setup Hub with support store, rate limiter and routes
	store := NewMemStore()
	limiter := NewLimiter(10, time.Minute)

	publicMux := http.NewServeMux()
	RegisterPublicRoutes(publicMux, store, limiter)

	adminMux := http.NewServeMux()
	gate := func(next http.HandlerFunc) http.HandlerFunc { return next }
	RegisterAdminRoutes(adminMux, store, gate)

	// 2. Client prepares report with sanitized diagnostics
	clientReport := ReportRequest{
		InstallID: "r-e2e-user-42",
		Platform:  "macos",
		Version:   "1.0.10",
		Message:   "Не работает соединение после пробуждения из сна",
		Logs: `=== Hikkari Diagnostics Report ===
OS: macos
Arch: arm64
App Version: 1.0.10
Timestamp: 2026-11-01T03:00:00Z
==================================
[proxy] dial tcp [REDACTED_IP]:443: connect: connection refused
[engine] active configuration [REDACTED_URI]
[wireguard] interface private key [REDACTED_KEY]
[auth] session token [REDACTED_TOKEN]
[core] listening on 127.0.0.1:9090`,
	}

	reportBytes, err := json.Marshal(clientReport)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Client sends report to hub: POST /api/v1/support/report
	reqPost := httptest.NewRequest(http.MethodPost, "/api/v1/support/report", bytes.NewReader(reportBytes))
	reqPost.Header.Set("Content-Type", "application/json")
	recPost := httptest.NewRecorder()
	publicMux.ServeHTTP(recPost, reqPost)

	if recPost.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/support/report returned %d, want 201; body: %s", recPost.Code, recPost.Body.String())
	}

	var postResult struct {
		OK     bool   `json:"ok"`
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recPost.Body.Bytes(), &postResult); err != nil {
		t.Fatalf("unmarshal post response: %v", err)
	}
	if !postResult.OK || postResult.ID <= 0 || postResult.Status != "open" {
		t.Fatalf("unexpected post result: %+v", postResult)
	}

	// 4. Admin panel loads reports: GET /api/v1/admin/support/reports
	reqList := httptest.NewRequest(http.MethodGet, "/api/v1/admin/support/reports", nil)
	recList := httptest.NewRecorder()
	adminMux.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/admin/support/reports returned %d, want 200", recList.Code)
	}

	var listResult struct {
		Reports []*Report `json:"reports"`
	}
	if err := json.Unmarshal(recList.Body.Bytes(), &listResult); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listResult.Reports) == 0 {
		t.Fatal("expected at least one report in list")
	}

	found := listResult.Reports[0]
	if found.ID != postResult.ID {
		t.Fatalf("report ID mismatch: got %d, want %d", found.ID, postResult.ID)
	}
	if found.InstallID != "r-e2e-user-42" {
		t.Fatalf("install_id mismatch: got %s", found.InstallID)
	}
	if found.Status != "open" {
		t.Fatalf("status mismatch: got %s, want open", found.Status)
	}
	if found.Message != clientReport.Message {
		t.Fatalf("message mismatch: got %s", found.Message)
	}
	if found.Logs != clientReport.Logs {
		t.Fatalf("logs mismatch: got %s", found.Logs)
	}

	// 5. Admin panel fetches single report detail: GET /api/v1/admin/support/reports/{id}
	reqGet := httptest.NewRequest(http.MethodGet, "/api/v1/admin/support/reports/"+strconv.FormatInt(postResult.ID, 10), nil)
	recGet := httptest.NewRecorder()
	adminMux.ServeHTTP(recGet, reqGet)

	if recGet.Code != http.StatusOK {
		t.Fatalf("GET report detail returned %d, want 200", recGet.Code)
	}

	// 6. Admin panel replies to the report: POST /api/v1/admin/support/reports/{id}/reply
	replyBody := `{"text": "Переподключитесь через настройки сети"}`
	reqReply := httptest.NewRequest(http.MethodPost, "/api/v1/admin/support/reports/"+strconv.FormatInt(postResult.ID, 10)+"/reply", bytes.NewBufferString(replyBody))
	reqReply.Header.Set("Content-Type", "application/json")
	recReply := httptest.NewRecorder()
	adminMux.ServeHTTP(recReply, reqReply)

	if recReply.Code != http.StatusOK {
		t.Fatalf("reply returned %d, want 200", recReply.Code)
	}

	var replyResult struct {
		OK     bool    `json:"ok"`
		Report *Report `json:"report"`
	}
	if err := json.Unmarshal(recReply.Body.Bytes(), &replyResult); err != nil {
		t.Fatalf("unmarshal reply response: %v", err)
	}
	if replyResult.Report.Status != "answered" {
		t.Fatalf("status after reply = %s, want answered", replyResult.Report.Status)
	}
	if len(replyResult.Report.Messages) != 2 {
		t.Fatalf("expected 2 messages (user + staff), got %d", len(replyResult.Report.Messages))
	}
	if !replyResult.Report.Messages[1].Staff || replyResult.Report.Messages[1].Text != "Переподключитесь через настройки сети" {
		t.Fatalf("unexpected reply message: %+v", replyResult.Report.Messages[1])
	}
}
