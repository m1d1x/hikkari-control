package admin

// product_errors.go answers the product errors screen: what breaks in the
// client, how often, for how many people, and since which version.
//
// # What a group is
//
// The client scrubs every error message on the device (addresses, paths,
// hosts and keys replaced by placeholders — hikkari-client
// engine/core/telemetry/scrub.go) and fingerprints the scrubbed text. A group
// here is (code, component, fingerprint): the same failure from the same part
// of the client, however many installs hit it. The message shown is the most
// recent one in the group; they differ only in what the scrubber left alone.
//
// There is no stack trace: the client never sends one. The code (from a fixed
// dictionary) and the component say where; the message says what.
//
// count is occurrences — the client folds repeats of one error into a row
// with a count, so a crash loop is one row with a big count, not a flood.

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type productErrors struct {
	Window string `json:"window"`
	// Occurrences and Installs are over every group in the window, not just
	// the ones returned: Installs is distinct installs that hit any error.
	Occurrences uint64       `json:"occurrences"`
	Installs    uint64       `json:"installs"`
	Groups      []errorGroup `json:"groups"`
	// Truncated says the window held more groups than the limit.
	Truncated bool `json:"truncated"`
}

type errorGroup struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Component   string   `json:"component"`
	Message     string   `json:"message"`
	Occurrences uint64   `json:"occurrences"`
	Installs    uint64   `json:"installs"`
	OSes        []string `json:"oses"`
	Versions    []string `json:"versions"`
	FirstSeen   int64    `json:"firstSeen"`
	LastSeen    int64    `json:"lastSeen"`
}

const errorGroupLimit = 200

// ProductErrors is GET /api/v1/admin/product/errors?window=24h|7d|30d.
func (h *Handlers) ProductErrors(w http.ResponseWriter, r *http.Request) {
	if h.analytics == nil {
		http.Error(w, "analytics store not configured", http.StatusServiceUnavailable)
		return
	}
	win, ok := parseProductWindow(r.URL.Query().Get("window"), "24h")
	if !ok {
		http.Error(w, "window is 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	out, err := h.productErrors(r.Context(), win, time.Now())
	if err != nil {
		log.Printf("admin product errors: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, out)
}

func (h *Handlers) productErrors(ctx context.Context, win productWindow, now time.Time) (*productErrors, error) {
	since, end := win.span(now)
	out := &productErrors{Window: win.Name, Groups: []errorGroup{}}

	if err := h.analytics.Row(ctx, `
		SELECT sum(count), uniq(install)
		FROM telemetry_errors
		WHERE ts >= ? AND ts < ?`, since, end,
	).Scan(&out.Occurrences, &out.Installs); err != nil {
		return nil, err
	}

	// One past the limit, to know whether the list was cut.
	rows, err := h.analytics.Query(ctx, `
		SELECT code, component, toString(fp), argMax(msg, ts),
			sum(count) AS n, uniq(install),
			groupUniqArray(os), groupUniqArray(version),
			min(ts), max(ts)
		FROM telemetry_errors
		WHERE ts >= ? AND ts < ?
		GROUP BY code, component, fp
		ORDER BY n DESC, max(ts) DESC
		LIMIT ?`, since, end, uint32(errorGroupLimit+1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var g errorGroup
		var fp string
		var first, last time.Time
		if err := rows.Scan(&g.Code, &g.Component, &fp, &g.Message, &g.Occurrences, &g.Installs,
			&g.OSes, &g.Versions, &first, &last); err != nil {
			return nil, err
		}
		g.ID = g.Code + "/" + g.Component + "/" + fp
		g.FirstSeen, g.LastSeen = first.UnixMilli(), last.UnixMilli()
		sort.Strings(g.OSes)
		sortVersionsDesc(g.Versions)
		out.Groups = append(out.Groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Groups) > errorGroupLimit {
		out.Groups, out.Truncated = out.Groups[:errorGroupLimit], true
	}
	return out, nil
}

// sortVersionsDesc orders "major.minor.patch" newest first. The client's
// contract only lets that shape through; anything else sorts last.
func sortVersionsDesc(vs []string) {
	sort.SliceStable(vs, func(i, j int) bool { return versionLess(vs[j], vs[i]) })
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		if ea != nil || eb != nil {
			return a < b
		}
		if na != nb {
			return na < nb
		}
	}
	return len(pa) < len(pb)
}
