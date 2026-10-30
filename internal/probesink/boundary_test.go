package probesink

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"
	"time"
)

// К1 (а): a row's time is the sender's word. One from the future would stay
// the relay's "latest verdict" for as long as the dataset keeps it.
func TestIngestEntryRefusesATimeOutsideTheWindow(t *testing.T) {
	for name, ts := range map[string]time.Time{
		"an hour ahead":    time.Now().Add(time.Hour),
		"the far future":   time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		"a day behind":     time.Now().Add(-24 * time.Hour),
		"before the epoch": time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeEntryStore{}
			body := fmt.Sprintf(`{"probes":[{"ts":%q,"sourceRegion":"ru-spb","relayName":"ams1-1","verdict":"ip_banned"}]}`,
				ts.UTC().Format(time.RFC3339))
			rr := doEntry(t, store, "Bearer "+testToken, body)
			if rr.Code != http.StatusBadRequest || len(store.got) != 0 {
				t.Fatalf("got %d with %d rows stored, want 400 and nothing", rr.Code, len(store.got))
			}
		})
	}
}

// К1: the observation point is the token's, not the body's.
func TestIngestEntryWritesEveryRowUnderTheTokensSlot(t *testing.T) {
	store := &fakeEntryStore{}
	rr := doEntry(t, store, "Bearer "+testToken,
		`{"probes":[{"sourceRegion":"zz","relayName":"ams1-1","verdict":"ip_banned"}]}`)
	if rr.Code != http.StatusAccepted || len(store.got) != 1 {
		t.Fatalf("got %d with %d rows", rr.Code, len(store.got))
	}
	if r := store.got[0].SourceRegion; r != "ru-spb" {
		t.Fatalf("row written under %q, want the token's slot ru-spb", r)
	}
}

// The class (М: generative): whatever the sender puts in the fields it
// controls — the region it claims, the time, the relay, the verdict, the
// token — a row is either written under the token's own slot, inside the
// time window, or nothing is written at all.
func TestIngestEntrySendersWordNeverPicksTheSlotOrTheTime(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 7))
	regions := []string{"", "ru-spb", "zz", "a", "ru-msk", "ru spb", strings.Repeat("x", 40)}
	verdicts := []string{"", "clean", "ip_banned", "fingerprint", "domain_blocked", "probe_failed", "x"}
	tokens := []string{testToken, testToken, testToken, "", "nope", testToken + "x", strings.ToUpper(testToken)}
	accepted := 0
	for i := 0; i < 5000; i++ {
		now := time.Now().UTC()
		var rows []string
		for n := rng.IntN(4) + 1; n > 0; n-- {
			// Mostly near the hub's clock, where the edges are; some anywhere.
			ts := now.Add(time.Duration(rng.Int64N(int64(75*time.Minute))) - 70*time.Minute)
			switch rng.IntN(4) {
			case 0:
				ts = now.Add(time.Duration(rng.Int64N(int64(72*time.Hour))) - 36*time.Hour)
			case 1:
				ts = time.Date(1900+rng.IntN(300), 1, 1, 0, 0, 0, 0, time.UTC)
			}
			rows = append(rows, fmt.Sprintf(`{"ts":%q,"sourceRegion":%q,"relayName":"r%d","verdict":%q}`,
				ts.Format(time.RFC3339Nano), regions[rng.IntN(len(regions))], rng.IntN(5), verdicts[rng.IntN(len(verdicts))]))
		}
		token := tokens[rng.IntN(len(tokens))]
		store := &fakeEntryStore{}
		rr := doEntry(t, store, "Bearer "+token, `{"probes":[`+strings.Join(rows, ",")+`]}`)
		if token != testToken && (rr.Code != http.StatusUnauthorized || len(store.got) != 0) {
			t.Fatalf("token %q: got %d with %d rows, want 401 and nothing", token, rr.Code, len(store.got))
		}
		accepted += len(store.got)
		for _, r := range store.got {
			if r.SourceRegion != "ru-spb" {
				t.Fatalf("row under %q, want ru-spb (body %v)", r.SourceRegion, rows)
			}
			if r.Timestamp.After(time.Now().Add(maxClockSkew)) || r.Timestamp.Before(now.Add(-maxRowAge)) {
				t.Fatalf("row dated %s accepted (body %v)", r.Timestamp, rows)
			}
		}
	}
	if accepted == 0 {
		t.Fatal("no batch was ever accepted: the generator proves nothing")
	}
}

// The SFU prober's rows are dated by the sender too: same window.
func TestIngestSFURefusesATimeOutsideTheWindow(t *testing.T) {
	_, err := normalize([]incomingProbe{{TS: time.Now().Add(time.Hour), SourceRegion: "ru", TargetRegion: "eu"}}, time.Now())
	if err == nil {
		t.Fatal("an SFU probe from an hour ahead was accepted")
	}
}
