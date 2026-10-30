package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func round2(f float64) float64 {
	return float64(int(f*100)) / 100
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// rangeAndBucket maps a humane range string to (since, bucket size) such
// that we render ~60 buckets. Returns ok=false on unknown values.
func rangeAndBucket(rangeStr string) (time.Time, time.Duration, bool) {
	now := time.Now().UTC()
	switch strings.ToLower(rangeStr) {
	case "15m":
		return now.Add(-15 * time.Minute), 15 * time.Second, true
	case "1h":
		return now.Add(-time.Hour), time.Minute, true
	case "6h":
		return now.Add(-6 * time.Hour), 6 * time.Minute, true
	case "24h":
		return now.Add(-24 * time.Hour), 24 * time.Minute, true
	}
	return time.Time{}, 0, false
}
