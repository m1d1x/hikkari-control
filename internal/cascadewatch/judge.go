package cascadewatch

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// When a layer reads red. The thresholds are where a user would notice, not
// where a counter moves: a red layer is a claim the report makes about who
// is to blame, so it errs on the side of saying nothing.
const (
	// The box: CPU stolen by neighbours (eun16 at 47–90 % made a protocol
	// look slow, research 2026-11-09 hysteria2), a load far past what the
	// CPUs carry (a burst of our own measurements reaches 2.5 per CPU on
	// nekko-spb and hurts nothing), memory running out, conntrack near its
	// limit, the interface dropping more than 0.5 % of what it got.
	maxStealPct     = 20.0
	maxLoadPerCPU   = 4.0
	minMemAvailFrac = 0.05
	maxConntrack    = 0.8
	maxIfaceDrop    = 0.005
	// The path: fewer than 11 of 12 bare flows open (a censor kills flows by
	// source port), or ICMP loses a tenth, with ICMP answered at all.
	minPathFlows = 11
	maxPingLoss  = 0.1
	// The entry's pool below half its members is a trunk the hub must see.
	minMembers = 16
	// The protocol: what diag:load calls a working profile, and a call
	// losing no more than 5 %.
	minBurst     = 45
	maxVoiceLoss = 0.05
)

func num(d map[string]any, k string) float64 {
	switch v := d[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case bool:
		if v {
			return 1
		}
	}
	return 0
}

func has(d map[string]any, k string) bool { _, ok := d[k]; return ok }

// judgeVPS reads a box; prev is its previous reading (nil for none), for the
// interface's drops since then. It adds the differences to cur.
func judgeVPS(cur, prev map[string]any) (bool, string) {
	var why []string
	if s := num(cur, "steal_pct"); s > maxStealPct {
		why = append(why, fmt.Sprintf("steal %.0f%%", s))
	}
	if cpus := num(cur, "cpus"); cpus > 0 && num(cur, "load1") > maxLoadPerCPU*cpus {
		why = append(why, fmt.Sprintf("load %.1f на %d CPU", num(cur, "load1"), int(cpus)))
	}
	if t := num(cur, "mem_total_mb"); t > 0 && num(cur, "mem_avail_mb")/t < minMemAvailFrac {
		why = append(why, fmt.Sprintf("свободно %d МБ памяти", int(num(cur, "mem_avail_mb"))))
	}
	if m := num(cur, "conntrack_max"); m > 0 && num(cur, "conntrack")/m > maxConntrack {
		why = append(why, fmt.Sprintf("conntrack %d из %d", int(num(cur, "conntrack")), int(m)))
	}
	if prev != nil && num(cur, "uptime_sec") >= num(prev, "uptime_sec") && cur["iface"] == prev["iface"] {
		d := func(k string) float64 { return num(cur, k) - num(prev, k) }
		cur["d_rx_packets"], cur["d_tx_packets"] = d("rx_packets"), d("tx_packets")
		cur["d_rx_drop"], cur["d_rx_errs"], cur["d_tx_drop"], cur["d_tx_errs"] = d("rx_drop"), d("rx_errs"), d("tx_drop"), d("tx_errs")
		cur["d_retrans_segs"], cur["d_out_segs"] = d("retrans_segs"), d("out_segs")
		if p := d("rx_packets"); p > 1000 && (d("rx_drop")+d("rx_errs"))/p > maxIfaceDrop {
			why = append(why, fmt.Sprintf("интерфейс теряет %.1f%% входящих", 100*(d("rx_drop")+d("rx_errs"))/p))
		}
		if p := d("tx_packets"); p > 1000 && (d("tx_drop")+d("tx_errs"))/p > maxIfaceDrop {
			why = append(why, fmt.Sprintf("интерфейс теряет %.1f%% исходящих", 100*(d("tx_drop")+d("tx_errs"))/p))
		}
		if o := d("out_segs"); o > 0 {
			cur["retrans_pct"] = float64(int(1000*d("retrans_segs")/o)) / 10
		}
	} else if prev != nil {
		cur["rebooted"] = true
	}
	return len(why) == 0, strings.Join(why, ", ")
}

// judgePath reads bare TCP and ICMP between two boxes.
func judgePath(d map[string]any) (bool, string) {
	var why []string
	flows := num(d, "flows")
	opened := num(d, "opened")
	min := float64(minPathFlows)
	if flows < 12 {
		min = flows
	}
	if opened < min {
		why = append(why, fmt.Sprintf("TCP %d/%d", int(opened), int(flows)))
	}
	if sent, recv := num(d, "ping_sent"), num(d, "ping_recv"); sent > 0 && recv > 0 && (sent-recv)/sent > maxPingLoss {
		why = append(why, fmt.Sprintf("ICMP теряет %d из %d", int(sent-recv), int(sent)))
	}
	return len(why) == 0, strings.Join(why, ", ")
}

// judgeTrunk reads a chunk run: every chunk delivered.
func judgeTrunk(d map[string]any) (bool, string) {
	if num(d, "delivered") < num(d, "chunks") {
		return false, fmt.Sprintf("%s: %d из %d кусков, концы %v", d["mode"], int(num(d, "delivered")), int(num(d, "chunks")), d["ends"])
	}
	return true, ""
}

// judgeProtocol reads a profile of a cascade task: its load and its call.
func judgeProtocol(d map[string]any) (bool, string) {
	var why []string
	if v, _ := d["addr"].(string); v != "" && v != "no-protocol" {
		why = append(why, "адрес: "+v)
	}
	if !has(d, "dl") {
		why = append(why, "нет строки нагрузки")
	} else {
		if num(d, "dl") < num(d, "dl_total") || d["dl_end"] != "ok" {
			why = append(why, fmt.Sprintf("1 МиБ: %d байт, %v", int(num(d, "dl")), d["dl_end"]))
		}
		if num(d, "burst") < minBurst {
			why = append(why, fmt.Sprintf("залп %d/%d", int(num(d, "burst")), int(num(d, "burst_total"))))
		}
		if num(d, "after") < num(d, "after_total") {
			why = append(why, fmt.Sprintf("после %d/%d", int(num(d, "after")), int(num(d, "after_total"))))
		}
	}
	if sent := num(d, "voice_sent"); sent > 0 {
		if lost := (sent - num(d, "voice_got")) / sent; lost > maxVoiceLoss {
			why = append(why, fmt.Sprintf("звонок теряет %.0f%%", 100*lost))
		}
	} else if e, _ := d["voice_error"].(string); e != "" {
		why = append(why, "звонок: "+e)
	}
	return len(why) == 0, strings.Join(why, ", ")
}

// Verdict is a round's: the layers that read red, why, and the first of
// them from the bottom up.
type Verdict struct {
	FirstRed string              `json:"first_red,omitempty"`
	Red      map[string][]string `json:"red,omitempty"`
	// Missing are the layers the round has no reading of.
	Missing []string `json:"missing,omitempty"`
	// Notes are what is red but not the cascade's: a direct path or profile
	// that fails as well, the control vantage.
	Notes []string `json:"notes,omitempty"`
}

// judgeRound reads a round's rows and the entry's minute lines within it.
// ruSlots are the observation points in Russia: the cascade is for their
// users, so only their protocol readings through the entry decide it.
func judgeRound(rows []Run, minutes []Run, ruSlots map[string]bool) Verdict {
	v := Verdict{Red: map[string][]string{}}
	seen := map[string]bool{}
	red := func(layer, who, why string) {
		v.Red[layer] = append(v.Red[layer], who+": "+why)
	}
	for _, r := range rows {
		seen[r.Layer] = true
		if r.Layer == LayerVPS && r.Vantage == "exit" {
			relays, _ := r.Data["relays"].(map[string]any)
			names := make([]string, 0, len(relays))
			for n := range relays {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				if st, _ := relays[n].(string); st != "alive" {
					v.Notes = append(v.Notes, fmt.Sprintf("релей выхода %s вне пула: %s (testing: канарейка раскатки меняет его сборку и перезапускает)", n, st))
				}
			}
		}
		if r.OK == nil || *r.OK {
			continue
		}
		why, _ := r.Data["why"].(string)
		switch r.Layer {
		case LayerVPS:
			red(LayerVPS, r.Vantage, why)
		case LayerPath:
			if r.Vantage == "entry" {
				red(LayerPath, "вход → выход", why)
			} else {
				v.Notes = append(v.Notes, "путь с "+r.Vantage+" к выходу: "+why)
			}
		case LayerTrunk:
			if r.Target == "entry" && r.Vantage == "entry" {
				red(LayerTrunk, "через вход", why)
			} else {
				v.Notes = append(v.Notes, "магистраль прямо с "+r.Vantage+": "+why)
			}
		case LayerProtocol:
			switch {
			case r.Target == "entry" && ruSlots[r.Vantage]:
				red(LayerProtocol, r.Vantage+" через вход", why)
			default:
				v.Notes = append(v.Notes, "протокол "+r.Target+" с "+r.Vantage+": "+why)
			}
		}
	}
	for _, m := range minutes {
		if num(m.Data, "members") < minMembers {
			red(LayerTrunk, "пул", fmt.Sprintf("%s: членов %d", m.TS.Format("15:04"), int(num(m.Data, "members"))))
			break
		}
	}
	if len(minutes) > 0 {
		seen[LayerTrunk] = true
	}
	for _, l := range Order {
		if !seen[l] {
			v.Missing = append(v.Missing, l)
		}
		if v.FirstRed == "" && len(v.Red[l]) > 0 {
			v.FirstRed = l
		}
	}
	for _, l := range Order {
		sort.Strings(v.Red[l])
	}
	if len(v.Red) == 0 {
		v.Red = nil
	}
	return v
}

// roundOf is the start of the round t falls in.
func roundOf(t time.Time, every time.Duration) time.Time { return t.UTC().Truncate(every) }
