package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"strconv"
	"time"
)

const (
	// taskDomain is the hub's reprobe of its burned domains (roadmap 42
	// §11, internal/entry reprobe.go): one live XHTTP relay lends its
	// address, params carry the names, the verdicts go back per name.
	taskDomain = "domain"
	// taskBox is the hub's reprobe of a suspect machine (П9.11): its relays,
	// out of the pool and out of the routine rounds, probed like an
	// admission; the verdicts go back per relay.
	taskBox = "box"
)

// serveReprobes claims the hub's reprobes of the second chance and answers
// them. It is a loop of its own and a slow one: the hub queues at most one
// reprobe of a kind at a time and each suspect at most once per its interval,
// and suspects never ride the routine rounds.
func (cfg config) serveReprobes(ctx context.Context) {
	ticker := time.NewTicker(cfg.reprobePoll)
	defer ticker.Stop()
	for {
		answered := false
		for _, kind := range []string{taskDomain, taskBox} {
			task, err := cfg.hub.claim(ctx, kind)
			if err != nil {
				log.Printf("entry-prober: claim %s reprobe: %v", kind, err)
				continue
			}
			if task == nil {
				continue
			}
			answered = true
			if kind == taskDomain {
				cfg.answerReprobe(ctx, task)
			} else {
				cfg.answerBoxReprobe(ctx, task)
			}
		}
		if answered {
			continue // there may be more queued
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// answerReprobe tries every name of a reprobe and reports. Nothing goes to
// the dataset: its rows are keyed by relay and keep no domain, and a burned
// name is not a relay. The log carries counts for the same reason.
func (cfg config) answerReprobe(ctx context.Context, task *probeTask) {
	var p struct {
		Domains []string `json:"domains"`
	}
	lent := task.Relays[0]
	verdicts := map[string]string{}
	if err := json.Unmarshal(task.Params, &p); err != nil || len(p.Domains) == 0 {
		// Nothing to answer: the hub takes only a verdict per name it sent,
		// and never queues a reprobe without names. Left claimed; it blocks
		// nothing (the hub waits only on queued reprobes).
		log.Printf("entry-prober: reprobe %s carries no names (%v)", task.ID, err)
		return
	}
	verdicts = runReprobe(lent, p.Domains, cfg.k2Control, cfg.timeout)
	if err := cfg.hub.finish(ctx, task.ID, verdicts); err != nil {
		log.Printf("entry-prober: reprobe %s: report: %v", task.ID, err)
		return
	}
	counts := map[string]int{}
	for _, v := range verdicts {
		counts[v]++
	}
	log.Printf("entry-prober: reprobe %s answered: %v", task.ID, counts)
}

// answerBoxReprobe probes a suspect machine's relays and reports. Nothing goes
// to the dataset: a suspect is out of the pool, and its readings in the ban
// tempo would count a machine twice — the hub's ledger keeps the record.
func (cfg config) answerBoxReprobe(ctx context.Context, task *probeTask) {
	verdicts, _ := cfg.probeOnce(ctx, task, cfg.socksBase+300)
	if err := cfg.hub.finish(ctx, task.ID, verdicts); err != nil {
		log.Printf("entry-prober: box reprobe %s: report: %v", task.ID, err)
		return
	}
	log.Printf("entry-prober: box reprobe %s answered: %v", task.ID, verdicts)
}

// runReprobe tries each name as SNI against the lent relay's address. The
// bare handshake goes first and per name, so a name is judged against the
// address as it stood just then, not as it stood before an earlier name's
// ClientHello.
func runReprobe(lent edge, domains []string, k2Control string, timeout time.Duration) map[string]string {
	addr := net.JoinHostPort(lent.Address, strconv.Itoa(lent.Port))
	k2 := probeTLS(k2Control, hostOf(k2Control), timeout)
	verdicts := make(map[string]string, len(domains))
	for _, d := range domains {
		bare := probeTLS(addr, "", timeout)
		named := probeTLS(addr, d, timeout)
		verdicts[d] = classifyReprobe(named, bare, k2)
	}
	return verdicts
}
