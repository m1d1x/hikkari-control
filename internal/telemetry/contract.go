// Package telemetry receives the clients' product events and scrubbed error
// reports (hikkari-client docs/TELEMETRY.md) and hands them to storage.
//
// The client refuses anything outside the contract before it is queued; the
// hub checks again, because a request is just bytes from the internet. An
// envelope that is not v1 is refused whole. Inside a valid envelope, items the
// hub does not know are skipped and counted, not refused: a client one release
// ahead must not lose its whole batch to a single new event.
package telemetry

import (
	"encoding/json"
	"fmt"
	"regexp"
)

var (
	engines   = set("singbox", "xray")
	protos    = set("vless-reality", "vless-xhttp", "vmess", "trojan", "shadowsocks", "hysteria2", "other")
	oses      = set("windows", "macos", "linux", "android", "ios")
	channels  = set("stable", "beta", "dev")
	countBkts = set("0", "1", "2-5", "6+", "2-10", "11+", "2+")
)

type kind int

const (
	kEnum kind = iota
	kBool
	kUint
	kBucket
	kVersion
	kErrCode
)

type spec struct {
	kind   kind
	values map[string]bool
}

func enum(v map[string]bool) spec { return spec{kind: kEnum, values: v} }

var events = map[string]map[string]spec{
	"install": {},
	"launch":  {"cold": {kind: kBool}},
	"first_connect_ok": {
		"engine": enum(engines), "proto": enum(protos), "secs_since_install": {kind: kUint},
	},
	"connect": {
		"engine": enum(engines), "proto": enum(protos), "ok": {kind: kBool}, "secs": {kind: kUint},
		"error": {kind: kErrCode},
	},
	"daily_active":  {"connected_minutes": {kind: kUint}, "profiles": {kind: kBucket}},
	"engine_switch": {"from": enum(engines), "to": enum(engines)},
	"profile_import": {
		"kind": enum(set("link", "subscription", "file")), "ok": {kind: kBool}, "count": {kind: kBucket},
		"error": {kind: kErrCode},
	},
	"subscription_sync": {
		"ok": {kind: kBool}, "added": {kind: kBucket}, "removed": {kind: kBucket}, "error": {kind: kErrCode},
	},
	"routing_apply": {
		"mode": enum(set("bypass", "proxy")), "ok": {kind: kBool},
		"source": enum(set("manual", "catalog", "import")), "error": {kind: kErrCode},
	},
	"update": {
		"step":       enum(set("found", "downloaded", "applied", "rolled_back", "failed")),
		"to_version": {kind: kVersion}, "error": {kind: kErrCode},
	},
	"ui_open": {"screen": enum(set("settings", "logs", "routing", "profiles", "support", "donate"))},
	"cta":     {"kind": enum(set("subscribe", "renew", "donate"))},
}

var errorCodes = set(
	"engine.start", "engine.crash", "engine.config",
	"tunnel.dns", "tunnel.route", "tunnel.down",
	"import.parse", "import.fetch",
	"sync.http", "sync.decode", "sync.apply",
	"routing.compile", "routing.apply",
	"update.check", "update.download", "update.verify", "update.apply",
	"service.ipc", "service.start",
	"ui.exception",
)

var components = set("engine", "singbox", "xray", "service", "updater", "ui")

var (
	reInstall = regexp.MustCompile(`^t-[0-9a-f]{32}$`)
	reVersion = regexp.MustCompile(`^\d{1,4}\.\d{1,4}\.\d{1,6}$`)
	reOSMajor = regexp.MustCompile(`^[0-9]{0,3}$`)
	reArch    = regexp.MustCompile(`^[a-z0-9_]{1,12}$`)
	reFP      = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

const (
	msgMaxRunes = 300
	maxSecs     = 30 * 24 * 3600
)

// checkFields validates one event's fields and returns them re-encoded in a
// canonical form for storage. ok=false means the event is skipped.
func checkFields(name string, fields map[string]any) (string, bool) {
	sp, known := events[name]
	if !known {
		return "", false
	}
	for k, v := range fields {
		f, ok := sp[k]
		if !ok || !checkValue(f, v) {
			return "", false
		}
	}
	if len(fields) == 0 {
		return "{}", true
	}
	raw, err := json.Marshal(fields) // map keys come out sorted
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func checkValue(f spec, v any) bool {
	switch f.kind {
	case kEnum:
		s, ok := v.(string)
		return ok && f.values[s]
	case kBool:
		_, ok := v.(bool)
		return ok
	case kUint:
		n, ok := v.(float64)
		return ok && n >= 0 && n <= maxSecs && n == float64(int64(n))
	case kBucket:
		s, ok := v.(string)
		return ok && countBkts[s]
	case kVersion:
		s, ok := v.(string)
		return ok && reVersion.MatchString(s)
	case kErrCode:
		s, ok := v.(string)
		return ok && (s == "" || errorCodes[s])
	}
	return false
}

func checkApp(a App) error {
	switch {
	case a.Version != "dev" && !reVersion.MatchString(a.Version):
		return fmt.Errorf("app.version %q", a.Version)
	case !channels[a.Channel]:
		return fmt.Errorf("app.channel %q", a.Channel)
	case !oses[a.OS]:
		return fmt.Errorf("app.os %q", a.OS)
	case !reOSMajor.MatchString(a.OSMajor):
		return fmt.Errorf("app.os_major %q", a.OSMajor)
	case !reArch.MatchString(a.Arch):
		return fmt.Errorf("app.arch %q", a.Arch)
	}
	return nil
}

func set(v ...string) map[string]bool {
	m := make(map[string]bool, len(v))
	for _, s := range v {
		m[s] = true
	}
	return m
}
