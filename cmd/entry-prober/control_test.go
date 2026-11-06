package main

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestControlHosts(t *testing.T) {
	if got := controlHosts("off"); got != nil {
		t.Fatalf("off: %v", got)
	}
	if got := controlHosts(" ya.ru , vk.com:8443,, "); !reflect.DeepEqual(got, []string{"ya.ru:443", "vk.com:8443"}) {
		t.Fatalf("list: %v", got)
	}
}

// A site that answers on the second try is reached; a site silent on both is
// failed, by name; the count is of sites, not of tries.
func TestRunControl_RetriesOnceAndNamesTheSilent(t *testing.T) {
	var mu sync.Mutex
	tries := map[string]int{}
	handshake := func(addr, sni string, _ time.Duration) bool {
		mu.Lock()
		defer mu.Unlock()
		tries[sni]++
		switch sni {
		case "flaky.ru":
			return tries[sni] == 2
		case "dead.ru":
			return false
		}
		return true
	}
	got := runControl([]string{"ya.ru:443", "flaky.ru:443", "dead.ru:443"}, time.Second, time.Millisecond, handshake)
	want := controlReport{Reached: 2, Total: 3, Failed: []string{"dead.ru"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if tries["ya.ru"] != 1 || tries["flaky.ru"] != 2 || tries["dead.ru"] != 2 {
		t.Fatalf("tries: %v", tries)
	}
}
