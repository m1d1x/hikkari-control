package middleware

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestASlowRequestIsLoggedWithItsPathAndWindowOnly(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	h := Trace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fast" {
			time.Sleep(slowRequest + 20*time.Millisecond)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/admin/entry/machines/42?window=24h&who=tamila", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/fast", nil))

	out := buf.String()
	if !strings.Contains(out, "slow request") || !strings.Contains(out, "/api/v1/admin/entry/machines/{n}") || !strings.Contains(out, "24h") {
		t.Fatalf("the slow request is not in the log: %q", out)
	}
	if strings.Contains(out, "tamila") || strings.Contains(out, "/42") {
		t.Fatalf("the log carries more than the path and the window: %q", out)
	}
	if strings.Contains(out, "/fast") {
		t.Fatalf("a fast request was logged: %q", out)
	}
}
