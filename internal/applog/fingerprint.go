package applog

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
)

// Fingerprint logic must stay 1:1 with admin/src/lib/logs/group.ts so server
// aggregation and the live admin grouper agree on which lines collapse together.
// Update both sides if you change the regex or the join shape.
var (
	reUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f-]+`)
	reNum  = regexp.MustCompile(`\d+`)
)

func normalizeMsg(msg string) string {
	s := reUUID.ReplaceAllString(strings.ToLower(msg), "<id>")
	s = reNum.ReplaceAllString(s, "<n>")
	return s
}

// Fingerprint returns a 16-hex-char SHA1 prefix of cat||normalizedMsg.
func Fingerprint(cat, msg string) string {
	if cat == "" {
		cat = "—"
	}
	h := sha1.New()
	h.Write([]byte(cat))
	h.Write([]byte("::"))
	h.Write([]byte(normalizeMsg(msg)))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
