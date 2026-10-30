// Package audit persists every mutating admin action to admin_audit_log
// (migration 076). Calls are best-effort: failures are logged, never
// returned, so the action being recorded is never blocked by audit I/O.
package audit

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// Entry is the data the caller fills in. AdminID, request-scoped fields
// (IP, UA) come from the *http.Request via Log; the helper extracts them.
type Entry struct {
	AdminID    string
	Action     string
	TargetType string
	TargetID   string
	Payload    map[string]any
}

// Log inserts an audit row best-effort. r may be nil for non-HTTP call
// sites (e.g. background jobs); when nil, ip/UA are left empty.
//
// Errors are logged but never returned — audit MUST NOT block the action
// it's recording.
func Log(ctx context.Context, q db.DBTX, r *http.Request, e Entry) {
	if q == nil {
		return
	}
	if e.Action == "" || e.TargetType == "" {
		log.Printf("audit: skip — action=%q targetType=%q", e.Action, e.TargetType)
		return
	}

	// Marshal payload defensively. If serialization fails (cycle, weird
	// type) we still want the row written so the action is recorded.
	var payloadJSON []byte
	if e.Payload == nil {
		payloadJSON = []byte("{}")
	} else if b, err := json.Marshal(e.Payload); err != nil {
		log.Printf("audit: marshal payload: %v", err)
		payloadJSON = []byte(`{"_payloadError":"marshal failed"}`)
	} else {
		payloadJSON = b
	}

	ip, ua := extractRequestMeta(r)

	var adminID any
	if e.AdminID != "" && e.AdminID != "00000000-0000-0000-0000-000000000000" {
		adminID = e.AdminID
	}

	var ipArg any
	if ip != "" {
		ipArg = ip
	}

	if _, err := q.Exec(ctx, `
		INSERT INTO admin_audit_log (admin_id, action, target_type, target_id, payload, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
	`, adminID, e.Action, e.TargetType, e.TargetID, string(payloadJSON), ipArg, ua); err != nil {
		log.Printf("audit: insert: %v action=%s target=%s/%s", err, e.Action, e.TargetType, e.TargetID)
	}
}

// extractRequestMeta pulls the source IP (X-Forwarded-For first hop or
// RemoteAddr) and a length-bounded user agent off the request. Both are
// safe-to-store strings; an unparseable IP returns empty.
func extractRequestMeta(r *http.Request) (ip, ua string) {
	if r == nil {
		return "", ""
	}
	ua = r.Header.Get("User-Agent")
	if len(ua) > 512 {
		ua = ua[:512]
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First hop is the real client; the rest is proxy chain.
		first := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
		if parsed := net.ParseIP(first); parsed != nil {
			return parsed.String(), ua
		}
	}
	if r.RemoteAddr != "" {
		// RemoteAddr is "ip:port" — strip the port.
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if parsed := net.ParseIP(host); parsed != nil {
			return parsed.String(), ua
		}
	}
	return "", ua
}
