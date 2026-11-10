package testerlegs

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type sink struct{ rows []Row }

func (s *sink) InsertTesterLegs(_ context.Context, rows []Row) error {
	s.rows = append(s.rows, rows...)
	return nil
}

type accounts map[string]string

func (a accounts) AccountsOf(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := a[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// TestRecordExit: an exit's row is filed under its key's account, the key
// itself nowhere; a row of a key the hub does not know, or malformed, is
// dropped.
func TestRecordExit(t *testing.T) {
	s := &sink{}
	r := &Recorder{Sink: s, Accounts: accounts{"tester-key": "acc-1"}}
	now := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	raw := []json.RawMessage{
		json.RawMessage(`{"id":"14c4c8b037d9dc9e","side":"exit","kind":"stream","phase":"end","key":"tester-key","atMs":` +
			"1794340000000" + `,"port":443,"ageMs":1200,"up":2078,"down":1904,"resent":3,"deaths":1,"maxWaitMs":410,"end":"fin"}`),
		json.RawMessage(`{"id":"24c4c8b037d9dc9e","kind":"stream","phase":"end","key":"stranger"}`),
		json.RawMessage(`{"id":"nothex","kind":"stream","phase":"end","key":"tester-key"}`),
		json.RawMessage(`not json`),
	}
	if err := r.RecordExit(context.Background(), "unl1-hk", raw, now); err != nil {
		t.Fatal(err)
	}
	if len(s.rows) != 1 {
		t.Fatalf("rows %+v", s.rows)
	}
	got := s.rows[0]
	if got.Subject != "acc-1" || got.Side != SideExit || got.Box != "unl1-hk" || got.Down != 1904 || got.Resent != 3 ||
		got.Deaths != 1 || got.MaxWaitMs != 410 || got.End != "fin" || !got.TS.Equal(time.UnixMilli(1794340000000)) {
		t.Fatalf("row %+v", got)
	}
}

// TestParseEntryLine: the entry's line as trunkentry/observe.go writes it.
func TestParseEntryLine(t *testing.T) {
	ts := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)
	msg := "2026/11/10 20:00:00 nekko-trunk-entry: observed id=14c4c8b037d9dc9e kind=stream phase=end port=8443 age_ms=1300 " +
		"up=1904 down=2054 chunks=2 resent=1 carriers=2 deaths=1 max_wait_ms=350 dgrams=0/0 copies=0 end=fin " +
		"client_rtt_ms=41.5 client_rttvar_ms=8.2 client_retrans=3 client_lost=0 client_unacked=0 client_cwnd=10 client_segs=12/14"
	if !IsEntryLine(msg) {
		t.Fatal("not recognised")
	}
	row, err := ParseEntryLine("spb", ts, msg)
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != "14c4c8b037d9dc9e" || row.Side != SideEntry || row.Subject != "" || row.Port != 8443 || row.Up != 1904 ||
		row.Down != 2054 || row.Resent != 1 || row.Deaths != 1 || row.MaxWaitMs != 350 || row.End != "fin" ||
		!row.HaveTCP || row.ClientRTTMs != 41.5 || row.ClientRetrans != 3 || row.ClientSegsIn != 14 {
		t.Fatalf("row %+v", row)
	}
	flow, err := ParseEntryLine("spb", ts, "nekko-trunk-entry: observed id=24c4c8b037d9dc9e kind=flow phase=mark port=8443 age_ms=5 up=0 down=0 chunks=0 resent=0 carriers=0 deaths=0 max_wait_ms=0 dgrams=7/6 copies=1 end=-")
	if err != nil || flow.DgOut != 7 || flow.DgIn != 6 || flow.DgCopy != 1 || flow.End != "" || flow.HaveTCP {
		t.Fatalf("flow %+v %v", flow, err)
	}
	if _, err := ParseEntryLine("spb", ts, "nekko-trunk-entry: observed id=xyz kind=stream phase=end"); err == nil {
		t.Fatal("a bad id was taken")
	}
}
