package stand

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/m1d1x/hikkari-control/internal/capacity"
	"os/exec"
	"strings"
	"time"
)

// Profile is one entry of the fleet's links as the client parses it.
type Profile struct {
	Resolver    map[string]any `json:"resolver,omitempty"`
	Node        string         `json:"node,omitempty"`
	CapacityKey string         `json:"capacityKey"`
	Label       string         `json:"label"`
	Config      map[string]any `json:"config"`
}

// Profiles runs the kit's parser over the run's link to the fleet's links
// (Task.SubURL), fetched as a subscription. The link goes on stdin and never
// into an error: its token opens every relay's credential.
func Profiles(ctx context.Context, parser, subURL string) ([]Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, parser, "--with-config")
	cmd.Stdin = strings.NewReader(subURL + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.ReplaceAll(strings.TrimSpace(stderr.String()), subURL, "[subscription]")
		return nil, fmt.Errorf("census-parse: %v: %s", err, msg)
	}
	var out []Profile
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, errors.New("census-parse: unreadable answer")
	}
	kept := out[:0]
	for _, p := range out {
		p.Label = DatasetLabel(p)
		if p.Config != nil && reValue.MatchString(p.Label) && p.Label != "" {
			kept = append(kept, p)
		}
	}
	return kept, nil
}

// DatasetLabel is how the dataset names the profile: the node it reaches
// when the link names one, else "profile-" and the first 12 hex of its
// capacity key, else its own label. The hub takes only [A-Za-z0-9._,/-] in a
// row's profile, and a subscription's labels are the user's words
// ("нидерланды-002"), so the label is the last resort.
func DatasetLabel(p Profile) string {
	if p.Node != "" && reValue.MatchString(p.Node) {
		return p.Node
	}
	if !reValue.MatchString(p.Label) && len(p.CapacityKey) == 64 {
		return "profile-" + p.CapacityKey[:12]
	}
	return p.Label
}

// Applies says whether knob is a knob of the profile at all: a fingerprint
// is chosen only by a TLS profile, a flow only by VLESS, a mode only by XHTTP. A profile the knob
// does not apply to is not measured for it, rather than failing the run.
func Applies(knob string, ob map[string]any) bool {
	if ob["type"] == "wireguard" || ob["type"] == "awg" {
		return false
	}
	switch knob {
	case "fp", "alpn":
		// Naive's TLS is Chromium's own (cronet): no uTLS fingerprint and no
		// ALPN to choose, and the core refuses either. Hikkari's stream
		// channel is Naive.
		if t, _ := ob["type"].(string); t == "naive" || t == "hikkari" {
			return false
		}
		tls, ok := ob["tls"].(map[string]any)
		on, _ := tls["enabled"].(bool)
		return ok && on
	case "sni":
		tls, ok := ob["tls"].(map[string]any)
		on, _ := tls["enabled"].(bool)
		return ok && on
	case "flow":
		typ, _ := ob["type"].(string)
		return typ == "vless"
	case "mode":
		tr, _ := ob["transport"].(map[string]any)
		return tr["type"] == "xhttp"
	}
	return true
}

// Runner runs a hub's task on this machine.
type Runner struct {
	Kit Local
	// Bash runs the script; "bash" when empty.
	Bash string
	// Log says what the run is doing, a line at a time.
	Log func(format string, args ...any)
}

// Run measures every profile of the fleet the knob applies to, one
// after another, and returns the rows with the reasons any profile stopped
// short. Rows of a profile that stopped half-way are kept.
func (r Runner) Run(ctx context.Context, t Task) Result {
	res := Result{Core: r.Kit.Label}
	profiles, err := Profiles(ctx, r.Kit.Parser, t.SubURL)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if t.Knob == capacity.TaskKind {
		return r.runCapacity(ctx, profiles)
	}
	if t.Knob == CascadeTaskKind {
		return r.runCascade(ctx, t, profiles)
	}
	if t.Knob == LoadTaskKind {
		return r.runLoad(ctx, t, profiles)
	}
	var errs []string
	measured := 0
	for _, p := range profiles {
		if !Applies(t.Knob, p.Config) {
			continue
		}
		plan, err := NewPlan(p.Config, Spec{Profile: p.Label, Knob: t.Knob, Values: t.Values, Base: t.Base, BasePath: t.BaselinePath, Rounds: t.Rounds})
		if err != nil {
			errs = append(errs, p.Label+": "+err.Error())
			continue
		}
		plan.Core = r.Kit.Label
		rows, err := r.runPlan(ctx, plan)
		res.Rows = append(res.Rows, rows...)
		measured++
		if err != nil {
			errs = append(errs, p.Label+": "+err.Error())
		}
		r.log("stand: %s %s=%s: %d rows", p.Label, t.Knob, strings.Join(t.Values, ","), len(rows))
		if ctx.Err() != nil {
			break
		}
	}
	if measured == 0 && len(errs) == 0 {
		errs = append(errs, fmt.Sprintf("во флоте нет профиля, к которому применима ручка %s (профилей %d)", t.Knob, len(profiles)))
	}
	res.Error = strings.Join(errs, "; ")
	return res
}

func (r Runner) runPlan(ctx context.Context, p Plan) ([]Row, error) {
	script, err := Script(p, r.Kit.Core)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, p.Wait())
	defer cancel()
	bash := r.Bash
	if bash == "" {
		bash = "bash"
	}
	cmd := exec.CommandContext(ctx, bash, "-s")
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	rows := Parse(stdout.String(), "self", p)
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return rows, fmt.Errorf("script: %v: %s", err, msg)
	}
	return rows, nil
}

func (r Runner) log(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}
