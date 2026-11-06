package main

// control.go measures the prober itself, from inside Russia.
//
// Every few minutes it reaches for a handful of sites that are up for
// everyone inside the country and tells the hub how many answered. The hub
// retires the slot after two failed reports in a row (internal/entry
// vantage_control.go): a prober that cannot reach Yandex or VK from where it
// sits has no business judging a machine. This replaces a probation week: not
// how long the point has behaved, but whether it behaves now.
//
// Each site that does not answer is tried once more after a short wait, so a
// lost packet is not a failed control. Sites are probed with a bare TLS
// handshake, as the K2 control is: the page behind it does not matter, and a
// site that answers a bot with 403 is still a site that was reached.

import (
	"context"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultControlHosts are up for everyone inside Russia and do not need the
// foreign internet: each is a Russian service on a Russian address.
const defaultControlHosts = "ya.ru:443,vk.com:443,www.gosuslugi.ru:443,mail.ru:443,www.ozon.ru:443,www.rbc.ru:443"

// controlRetryWait is how long a silent site gets before its second try.
const controlRetryWait = 3 * time.Second

// controlReport is the body the hub's control route takes.
type controlReport struct {
	Reached int      `json:"reached"`
	Total   int      `json:"total"`
	Failed  []string `json:"failed"`
}

// controlHosts splits the env list; "off" or empty turns the control off.
func controlHosts(list string) []string {
	if strings.EqualFold(strings.TrimSpace(list), "off") {
		return nil
	}
	var out []string
	for _, h := range strings.Split(list, ",") {
		if h = strings.TrimSpace(h); h != "" {
			if _, _, err := net.SplitHostPort(h); err != nil {
				h = net.JoinHostPort(h, "443")
			}
			out = append(out, h)
		}
	}
	return out
}

// runControl tries every host at once, each twice at most, and counts those
// that answered. handshake is probeTLS in production.
func runControl(hosts []string, timeout, retryWait time.Duration, handshake func(addr, sni string, timeout time.Duration) bool) controlReport {
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		failed []string
	)
	for _, addr := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sni := hostOf(addr)
			ok := handshake(addr, sni, timeout)
			if !ok {
				time.Sleep(retryWait)
				ok = handshake(addr, sni, timeout)
			}
			if !ok {
				mu.Lock()
				failed = append(failed, sni)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Strings(failed)
	return controlReport{Reached: len(hosts) - len(failed), Total: len(hosts), Failed: failed}
}

// serveControl reports the control to the hub every interval. When the hub
// says the slot was retired by it, the prober stops: the token is void now.
func (cfg config) serveControl(ctx context.Context, stop context.CancelFunc) {
	if len(cfg.controlHosts) == 0 {
		log.Printf("entry-prober: RU control is off")
		return
	}
	ticker := time.NewTicker(cfg.controlEvery)
	defer ticker.Stop()
	for {
		rep := runControl(cfg.controlHosts, cfg.timeout, controlRetryWait, probeTLS)
		var answer struct {
			Passed bool `json:"passed"`
			Kicked bool `json:"kicked"`
		}
		if _, err := cfg.hub.do(ctx, "POST", "/control", rep, &answer); err != nil {
			log.Printf("entry-prober: control %d/%d reported: %v", rep.Reached, rep.Total, err)
		} else if answer.Kicked {
			log.Printf("entry-prober: the hub retired this point after a failed RU control (%d/%d, silent: %s) — stopping",
				rep.Reached, rep.Total, strings.Join(rep.Failed, ", "))
			stop()
			return
		} else if !answer.Passed {
			log.Printf("entry-prober: RU control FAILED %d/%d, silent: %s", rep.Reached, rep.Total, strings.Join(rep.Failed, ", "))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
