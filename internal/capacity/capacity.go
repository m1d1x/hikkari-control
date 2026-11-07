// Package capacity records bounded measurements made by our observation points.
// Nothing in this package requests a measurement from a subscriber device.
package capacity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

const Header = "Hikkari-Relay-Capacity"
const TaskKind = "capacity-v1"
const MaxBytes = 32 << 20
const FreshFor = 90 * time.Minute

// Key identifies the transport configuration without the subscriber credential
// or presentation label. Query ordering is canonical on both ends of the contract.
func Key(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	u.User, u.Fragment, u.RawFragment = nil, "", ""
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	q := u.Query()
	q.Del("hikkari-node")
	u.RawQuery = q.Encode()
	sum := sha256.Sum256([]byte(u.String()))
	return hex.EncodeToString(sum[:])
}

type Sample struct {
	Key        string  `json:"key"`
	MeasuredAt int64   `json:"measuredAt"`
	Bytes      int64   `json:"bytes"`
	DurationMS int64   `json:"durationMs"`
	Mbps       float64 `json:"mbps"`
	Limited    bool    `json:"limited"`
	Error      string  `json:"error,omitempty"`
}

func (s Sample) Validate(now time.Time) error {
	key, err := hex.DecodeString(s.Key)
	if err != nil || len(key) != 32 || s.Key != strings.ToLower(s.Key) {
		return fmt.Errorf("invalid capacity key")
	}
	if s.MeasuredAt < now.Add(-2*time.Hour).Unix() || s.MeasuredAt > now.Add(2*time.Minute).Unix() {
		return fmt.Errorf("invalid capacity time")
	}
	if s.Bytes < 0 || s.Bytes > MaxBytes || s.DurationMS < 0 || s.DurationMS > 30000 || math.IsNaN(s.Mbps) || math.IsInf(s.Mbps, 0) || s.Mbps < 0 {
		return fmt.Errorf("invalid capacity sample")
	}
	if s.Error != "" {
		if s.Mbps != 0 || (s.Error != "unreachable" && s.Error != "control_unavailable" && s.Error != "core_unavailable") {
			return fmt.Errorf("invalid capacity failure")
		}
		return nil
	}
	if s.Bytes < 256<<10 || s.DurationMS < 1 || s.Mbps == 0 {
		return fmt.Errorf("empty capacity measurement")
	}
	calculated := float64(s.Bytes) * 8 / float64(s.DurationMS) / 1000
	if math.Abs(s.Mbps-calculated) > math.Max(0.1, calculated*0.02) {
		return fmt.Errorf("capacity rate does not match byte counters")
	}
	return nil
}

type Metric struct {
	Key        string  `json:"key"`
	MeasuredAt int64   `json:"measuredAt"`
	ExpiresAt  int64   `json:"expiresAt"`
	Mbps       float64 `json:"mbps"`
	Limited    bool    `json:"limited"`
	Points     int     `json:"points"`
}

// Encode bounds the subscription header below the ordinary HTTP header limit.
func Encode(metrics []Metric) string {
	if len(metrics) > 24 {
		metrics = metrics[:24]
	}
	data, _ := json.Marshal(metrics)
	return string(data)
}
