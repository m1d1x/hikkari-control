// Package hikkariobs is the hub's side of Hikkari v1.04 observability
// (hikkari-protocol spec/v1.md): the periods each end of a Hikkari session
// counted, as time series in ClickHouse hikkari_obs, one row per number.
//
//	relay pulse  "hikkari": {"<path>/<key>": period}  ─┐
//	client log   source hikkari: one period per line  ─┴─► Flatten ─► hikkari_obs
//
// A period is the protocol's observe.Period as JSON. The hub does not decode
// it into a type of its own: every number in it becomes a row named by its
// path in the JSON ("tcp.rtt.p95", "quic.voice.loss_pm.max", "dup.only_stream",
// "tunnels.eof"), so a new field on the wire is a new series here without a
// change on the hub.
package hikkariobs

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Sides of a session.
const (
	SideRelay  = "relay"
	SideClient = "client"
)

// Row is one number of one period.
type Row struct {
	TS      time.Time
	Side    string // SideRelay or SideClient
	Relay   string // the relay that counted it; a client's profile tag
	Path    string // direct, entry, trunk; "" for a client
	Subject string // the account (relay side) or the install (client side)
	PeriodS uint32 // seconds the period covers
	Metric  string
	Value   float64
}

// maxMetrics bounds the rows of one period: a period is a few dozen numbers,
// so anything near this is not one.
const maxMetrics = 512

// maxDepth bounds how deep a period nests.
const maxDepth = 5

// Flatten turns one period into its numbers by path, sorted. Strings, nulls
// and booleans carry no number and are left out.
func Flatten(period json.RawMessage) (map[string]float64, error) {
	var v any
	if err := json.Unmarshal(period, &v); err != nil {
		return nil, fmt.Errorf("hikkariobs: period: %w", err)
	}
	out := map[string]float64{}
	if err := flatten("", v, 0, out); err != nil {
		return nil, err
	}
	return out, nil
}

func flatten(prefix string, v any, depth int, out map[string]float64) error {
	switch x := v.(type) {
	case float64:
		if prefix == "" {
			return fmt.Errorf("hikkariobs: a period is an object")
		}
		if len(out) >= maxMetrics {
			return fmt.Errorf("hikkariobs: more than %d numbers in a period", maxMetrics)
		}
		out[prefix] = x
	case map[string]any:
		if depth >= maxDepth {
			return fmt.Errorf("hikkariobs: period nests deeper than %d", maxDepth)
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !validName(k) {
				return fmt.Errorf("hikkariobs: field %q", k)
			}
			name := k
			if prefix != "" {
				name = prefix + "." + k
			}
			if err := flatten(name, x[k], depth+1, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// validName keeps metric names to what the protocol writes: lower case,
// digits and underscores.
func validName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// Rows turns one period into rows.
func Rows(base Row, period json.RawMessage) ([]Row, error) {
	numbers, err := Flatten(period)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(numbers))
	for name := range numbers {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]Row, 0, len(names))
	for _, name := range names {
		r := base
		r.Metric, r.Value = name, numbers[name]
		rows = append(rows, r)
	}
	return rows, nil
}

// SplitGroup reads a relay's group "<path>/<key>"; a group without a path is
// direct (as observe.SplitGroup).
func SplitGroup(group string) (path, key string) {
	if path, key, ok := strings.Cut(group, "/"); ok {
		return path, key
	}
	return "direct", group
}
