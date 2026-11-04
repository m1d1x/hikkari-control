package stand

// The knob controller (hikkari-client plans/2026-11-04-tspu-detection-research.md,
// «Что делать дальше», 2). The stand measures every candidate of a knob on
// every node of its subscription from every point, whatever value the node
// has now; the controller reads those rows and moves a node off a value Russia
// cuts onto a candidate that passes, before a tester complains.
//
// # Who owns what
//
// The node's value stays where the subscription reads it: entry_nodes
// (fingerprint, sni, alpn). The candidates are the stand's own (stand_knobs):
// a value the stand does not measure cannot be chosen. The policy is in
// stand_settings (migration 026); the journal is relay_knob_events, written in
// the same transaction as the switch and drawn on the knob's chart.
//
// # The rule
//
// Over the window, from the points in Russia (every active slot outside
// control_slots), the share of the burst that answered, per value:
//
//   - the address with no protocol is cut → no knob helps: say so, touch nothing;
//   - the node's value is at or above knob_low → nothing to do;
//   - a candidate measured enough rounds, at or above knob_high, and ahead of
//     the node's value by knob_margin, and not refused at the control points
//     (a value the server itself refuses, edge and qq on ams3, is red there
//     too) → switch to the best such candidate, unless the node switched this
//     knob within knob_cooldown;
//   - no such candidate → every candidate is red: say so, touch nothing.
//
// A node returning to a value it left is not special: the stand keeps
// measuring the old value after the switch, so a return needs that value to
// pass on rows measured since.
//
// # When things fail
//
// Too few rounds of the node's own value (the stand silent, a point down) is
// no evidence: nothing happens and nothing is written. A node whose value
// changed under the controller (the owner set it by hand) is left as it is:
// the update is conditional on the value the decision was made on.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/relayruns"
)

// baselineFloor is the share of the burst with no protocol below which the
// address counts as cut. The baseline passes 148–150 of 150 on a live address.
const baselineFloor = 0.9

// repeatQuiet is how long a state the controller cannot act on (all red, the
// address cut) is not written again for the same node and knob.
const repeatQuiet = 6 * time.Hour

// nodeColumn is the entry_nodes column of each knob the controller can set.
var nodeColumn = map[string]string{"fp": "fingerprint", "sni": "sni", "alpn": "alpn"}

// Policy is the controller's part of stand_settings.
type Policy struct {
	Mode      string        `json:"mode"`
	Window    time.Duration `json:"-"`
	MinRounds int           `json:"min_rounds"`
	Low       float64       `json:"low"`
	High      float64       `json:"high"`
	Margin    float64       `json:"margin"`
	Cooldown  time.Duration `json:"-"`
	Control   []string      `json:"control_slots"`
}

// Share is a value's burst over the window at one kind of point.
type Share struct {
	Rounds   int `json:"rounds"`
	Answered int `json:"answered"`
	Asked    int `json:"asked"`
}

// Of is the share answered, 0 with nothing asked.
func (s Share) Of() float64 {
	if s.Asked == 0 {
		return 0
	}
	return float64(s.Answered) / float64(s.Asked)
}

func (s Share) String() string {
	return fmt.Sprintf("%d/%d за %d р.", s.Answered, s.Asked, s.Rounds)
}

// Evidence is what a decision was made on, kept with its journal line.
type Evidence struct {
	Window   string           `json:"window"`
	Russia   map[string]Share `json:"russia"`
	Control  map[string]Share `json:"control,omitempty"`
	Baseline Share            `json:"baseline"`
}

// Decision is what the rule says for one node and knob.
type Decision struct {
	Kind   string // "" (nothing), relayruns.EventSwitch, EventAllRed or EventAddressCut
	To     string
	Reason string
}

// Decide applies the rule. candidates are the stand's values of the knob in
// its order (the tie-break); current is the node's value; lastSwitch is when
// the node last switched this knob (zero for never).
func Decide(p Policy, current string, candidates []string, ev Evidence, lastSwitch, now time.Time) Decision {
	cur := ev.Russia[current]
	if cur.Rounds < p.MinRounds {
		return Decision{}
	}
	if ev.Baseline.Rounds > 0 && ev.Baseline.Of() < baselineFloor {
		return Decision{Kind: relayruns.EventAddressCut,
			Reason: fmt.Sprintf("адрес без протокола из РФ %s: режут адрес, ручка не поможет", ev.Baseline)}
	}
	if cur.Of() >= p.Low {
		return Decision{}
	}
	best, bestShare := "", 0.0
	for _, v := range candidates {
		s, ok := ev.Russia[v]
		if v == current || !ok || s.Rounds < p.MinRounds || s.Of() < p.High || s.Of()-cur.Of() < p.Margin {
			continue
		}
		if c, ok := ev.Control[v]; ok && c.Rounds > 0 && c.Of() < p.High {
			continue // refused outside Russia too: the server, not the censor
		}
		if s.Of() > bestShare {
			best, bestShare = v, s.Of()
		}
	}
	if best == "" {
		return Decision{Kind: relayruns.EventAllRed,
			Reason: fmt.Sprintf("%s из РФ %s, ни один кандидат не проходит порог %.0f%%", current, cur, p.High*100)}
	}
	if !lastSwitch.IsZero() && now.Sub(lastSwitch) < p.Cooldown {
		return Decision{}
	}
	return Decision{Kind: relayruns.EventSwitch, To: best,
		Reason: fmt.Sprintf("%s из РФ %s ниже %.0f%%; %s %s", current, cur, p.Low*100, best, ev.Russia[best])}
}

// Controller runs the rule over every live node.
type Controller struct {
	pool db.Pool
	now  func() time.Time
}

// NewController is the controller on pool.
func NewController(pool db.Pool) *Controller {
	return &Controller{pool: pool, now: time.Now}
}

// Run decides every tick until ctx ends.
func (c *Controller) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := c.Tick(ctx); err != nil {
			applog.Error(ctx, "knobs", "controller failed", applog.F("err", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Controller) policy(ctx context.Context) (Policy, error) {
	var p Policy
	var window, cooldown int
	err := c.pool.QueryRow(ctx, `
		SELECT controller, knob_window_minutes, knob_min_rounds, knob_low, knob_high, knob_margin,
		       knob_cooldown_minutes, control_slots
		FROM stand_settings WHERE id`).Scan(&p.Mode, &window, &p.MinRounds, &p.Low, &p.High, &p.Margin, &cooldown, &p.Control)
	if err != nil {
		return Policy{}, fmt.Errorf("controller policy: %w", err)
	}
	p.Window = time.Duration(window) * time.Minute
	p.Cooldown = time.Duration(cooldown) * time.Minute
	return p, nil
}

// node is a live node and its knob values as entry_nodes holds them.
type node struct {
	slug, label string
	values      map[string]string // knob → column value, "" when unset
}

// Tick runs the rule once over every live node and controllable knob.
func (c *Controller) Tick(ctx context.Context) error {
	p, err := c.policy(ctx)
	if err != nil || p.Mode != "auto" {
		return err
	}
	knobs, err := c.candidates(ctx)
	if err != nil || len(knobs) == 0 {
		return err
	}
	nodes, err := c.nodes(ctx)
	if err != nil {
		return err
	}
	now := c.now()
	for _, n := range nodes {
		for knob, cands := range knobs {
			if err := c.decideNode(ctx, p, n, knob, cands, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Controller) candidates(ctx context.Context) (map[string][]string, error) {
	rows, err := c.pool.Query(ctx, `SELECT knob, vals FROM stand_knobs WHERE enabled AND knob = ANY($1)`, []string{"fp", "sni", "alpn"})
	if err != nil {
		return nil, fmt.Errorf("controller candidates: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var k string
		var v []string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (c *Controller) nodes(ctx context.Context) ([]node, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT n.slug, n.label, coalesce(n.fingerprint, ''), coalesce(n.sni, ''), coalesce(n.alpn, '')
		FROM entry_nodes n JOIN entry_relays r ON r.id = n.relay_id
		WHERE r.status = 'alive' AND coalesce(n.label, '') <> ''
		ORDER BY n.slug`)
	if err != nil {
		return nil, fmt.Errorf("controller nodes: %w", err)
	}
	defer rows.Close()
	var out []node
	for rows.Next() {
		var n node
		var fp, sni, alpn string
		if err := rows.Scan(&n.slug, &n.label, &fp, &sni, &alpn); err != nil {
			return nil, err
		}
		n.values = map[string]string{"fp": fp, "sni": sni, "alpn": alpn}
		out = append(out, n)
	}
	return out, rows.Err()
}

// currentValue is the node's value of knob as the stand names it, "" when the
// controller cannot know it: an unset fingerprint is the link's default
// (internal/subscription links.go), an unset SNI or ALPN is whatever the
// relay's record gives, which no row names.
func currentValue(knob, column string) string {
	switch {
	case column != "":
		return standValue(knob, column)
	case knob == "fp":
		return "chrome"
	}
	return ""
}

// standValue and nodeValue convert between how the stand writes a value and
// how entry_nodes keeps it: they differ only for ALPN ("h2/http1.1" against
// "h2,http/1.1").
func standValue(knob, v string) string {
	if knob != "alpn" {
		return v
	}
	return strings.ReplaceAll(strings.ReplaceAll(v, "http/1.1", "http1.1"), ",", "/")
}

func nodeValue(knob, v string) string {
	if knob != "alpn" {
		return v
	}
	return strings.ReplaceAll(strings.ReplaceAll(v, "/", ","), "http1.1", "http/1.1")
}

func (c *Controller) decideNode(ctx context.Context, p Policy, n node, knob string, cands []string, now time.Time) error {
	current := currentValue(knob, n.values[knob])
	if current == "" {
		return nil
	}
	ev, err := c.evidence(ctx, p, n.label, knob, now)
	if err != nil {
		return err
	}
	lastSwitch, lastQuiet, err := c.lastEvents(ctx, n.slug, knob)
	if err != nil {
		return err
	}
	d := Decide(p, current, cands, ev, lastSwitch, now)
	if d.Kind == "" {
		return nil
	}
	evidence, _ := json.Marshal(ev)
	line := relayruns.KnobEvent{Node: n.slug, Profile: n.label, Knob: knob, Kind: d.Kind, From: current, To: d.To,
		Reason: d.Reason, Evidence: evidence}
	if d.Kind != relayruns.EventSwitch {
		if q, ok := lastQuiet[d.Kind]; ok && now.Sub(q) < repeatQuiet {
			return nil
		}
		return relayruns.RecordKnobEvent(ctx, c.pool, line)
	}
	return c.apply(ctx, n, knob, d.To, line)
}

// apply sets the node's value and writes the journal line in one transaction,
// only if the node still holds the value the decision was made on.
func (c *Controller) apply(ctx context.Context, n node, knob, to string, line relayruns.KnobEvent) error {
	col := nodeColumn[knob]
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE entry_nodes SET `+col+` = $1 WHERE slug = $2 AND coalesce(`+col+`, '') = $3`,
		nodeValue(knob, to), n.slug, n.values[knob])
	if err != nil {
		return fmt.Errorf("switch %s %s: %w", n.label, knob, err)
	}
	if tag.RowsAffected() == 0 {
		return nil // changed under us: the next tick decides on the new value
	}
	if err := relayruns.RecordKnobEvent(ctx, tx, line); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	applog.Info(ctx, "knobs", "node switched", applog.F("node", n.label), applog.F("knob", knob),
		applog.F("from", line.From), applog.F("to", to), applog.F("reason", line.Reason))
	return nil
}

// evidence sums the stand's rows of the node's profile over the window.
// A round whose core did not start counts as 50 asked and none answered.
func (c *Controller) evidence(ctx context.Context, p Policy, profile, knob string, now time.Time) (Evidence, error) {
	ev := Evidence{Window: p.Window.String(), Russia: map[string]Share{}, Control: map[string]Share{}}
	rows, err := c.pool.Query(ctx, `
		SELECT knob, value, vantage = ANY($4) AS control, count(*),
		       sum(CASE WHEN load_error THEN 0 ELSE burst END),
		       sum(CASE WHEN load_error THEN $5::int ELSE burst_total END)
		FROM relay_runs
		WHERE source = 'stand' AND profile = $1 AND (knob = $2 OR knob = 'baseline')
		  AND measured_at > $3
		GROUP BY 1, 2, 3`, profile, knob, now.Add(-p.Window), p.Control, Burst)
	if err != nil {
		return ev, fmt.Errorf("controller evidence: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		var control bool
		var s Share
		if err := rows.Scan(&k, &v, &control, &s.Rounds, &s.Answered, &s.Asked); err != nil {
			return ev, err
		}
		switch {
		case k == Baseline && !control:
			ev.Baseline.Rounds += s.Rounds
			ev.Baseline.Answered += s.Answered
			ev.Baseline.Asked += s.Asked
		case k == Baseline:
		case control:
			ev.Control[v] = s
		default:
			ev.Russia[v] = s
		}
	}
	return ev, rows.Err()
}

// lastEvents is when the node last switched knob, and when each quiet kind
// was last written.
func (c *Controller) lastEvents(ctx context.Context, slug, knob string) (time.Time, map[string]time.Time, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT kind, max(at) FROM relay_knob_events WHERE node = $1 AND knob = $2 GROUP BY kind`, slug, knob)
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("controller journal: %w", err)
	}
	defer rows.Close()
	var lastSwitch time.Time
	quiet := map[string]time.Time{}
	for rows.Next() {
		var kind string
		var at time.Time
		if err := rows.Scan(&kind, &at); err != nil {
			return time.Time{}, nil, err
		}
		if kind == relayruns.EventSwitch {
			lastSwitch = at
		} else {
			quiet[kind] = at
		}
	}
	return lastSwitch, quiet, rows.Err()
}

// NodeKnobs is a live node's values as the agent reads them.
type NodeKnobs struct {
	Node    string            `json:"node"`
	Profile string            `json:"profile"`
	Values  map[string]string `json:"values"`
}

// Nodes lists the live nodes and their knob values, an unset one as the
// controller reads it.
func (c *Controller) Nodes(ctx context.Context) ([]NodeKnobs, error) {
	nodes, err := c.nodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]NodeKnobs, 0, len(nodes))
	for _, n := range nodes {
		v := map[string]string{}
		for k, col := range n.values {
			if cur := currentValue(k, col); cur != "" {
				v[k] = cur
			}
		}
		out = append(out, NodeKnobs{Node: n.slug, Profile: n.label, Values: v})
	}
	return out, nil
}

// PolicyNow is the controller's policy as the agent reads it.
func (c *Controller) PolicyNow(ctx context.Context) (Policy, error) {
	return c.policy(ctx)
}
