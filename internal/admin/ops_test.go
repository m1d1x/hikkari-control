package admin

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/auth"
)

func TestOpsGate(t *testing.T) {
	pool := livePostgres(t)
	ctx := context.Background()
	label := fmt.Sprintf("agent-test-%d", time.Now().UnixNano())
	token, err := auth.IssueOpsToken(ctx, pool, label)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	reached := 0
	gate := RequireOps(pool)
	mux.HandleFunc("POST /api/v1/ops/testers/{install}/commands", gate("ops.tester.request_diag", func(http.ResponseWriter, *http.Request) { reached++ }))
	// The admin plane's own gate behind the same mux: a token means nothing there.
	mux.HandleFunc("GET /api/v1/admin/entry/machines", RequireAdmin(&key.PublicKey, pool, func(http.ResponseWriter, *http.Request) { reached += 100 }))

	do := func(method, path, bearer string) int {
		req := httptest.NewRequest(method, path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	const path = "/api/v1/ops/testers/tester-001/commands"
	if code := do(http.MethodPost, path, ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code := do(http.MethodPost, path, "hko_wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code := do(http.MethodPost, path, token); code != http.StatusOK || reached != 1 {
		t.Fatalf("good token: %d reached=%d", code, reached)
	}
	if code := do(http.MethodGet, "/api/v1/admin/entry/machines", token); code != http.StatusUnauthorized || reached != 1 {
		t.Fatalf("an ops token must not open the admin plane: %d reached=%d", code, reached)
	}

	var rows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM admin_audit_log
		WHERE admin_id IS NULL AND action = 'ops.tester.request_diag' AND target_id = 'tester-001'
		  AND payload->>'token' = $1`, label).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("audit rows: %d", rows)
	}

	if n, err := auth.RevokeOpsTokens(ctx, pool, label); err != nil || n != 1 {
		t.Fatalf("revoke: %d %v", n, err)
	}
	if code := do(http.MethodPost, path, token); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}
}
