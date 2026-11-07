package main

import (
	"context"
	"github.com/m1d1x/hikkari-control/internal/stand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestStandResultSurvivesHubFailureAndWorkerRestart(t *testing.T) {
	status := 503
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(status)
		if status == 200 {
			w.Write([]byte(`{"added":1}`))
		}
	}))
	defer server.Close()
	cfg := config{stand: standConfig{dir: t.TempDir()}, hub: hubClient{base: server.URL, http: server.Client(), token: "test"}}
	if err := cfg.saveStandResult(pendingStandResult{ID: "task", Result: stand.Result{Core: "test"}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := cfg.flushStandResult(context.Background()); ok || err == nil {
		t.Fatal("failed delivery was acknowledged")
	}
	if _, err := os.Stat(cfg.standResultPath()); err != nil {
		t.Fatal("outbox lost")
	}
	status = 200
	restarted := cfg
	if ok, err := restarted.flushStandResult(context.Background()); !ok || err != nil {
		t.Fatal("retry", ok, err)
	}
	if _, err := os.Stat(cfg.standResultPath()); !os.IsNotExist(err) {
		t.Fatal("delivered report retained")
	}
	if ok, err := cfg.flushStandResult(context.Background()); !ok || err != nil || calls != 2 {
		t.Fatal("duplicate delivery", calls, err)
	}
}
