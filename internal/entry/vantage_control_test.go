package entry

import (
	"context"
	"errors"
	"testing"
)

func TestVantageControl_PassNeedsThreeInFive(t *testing.T) {
	for _, c := range []struct {
		reached, total int
		want           bool
	}{
		{6, 6, true}, {4, 6, true}, {3, 5, true}, {2, 5, false}, {0, 6, false}, {1, 1, true}, {0, 0, false},
	} {
		if got := (VantageControl{Reached: c.reached, Total: c.total}).Passed(); got != c.want {
			t.Errorf("%d/%d: passed = %v, want %v", c.reached, c.total, got, c.want)
		}
	}
}

func controlSlot(t *testing.T, h *harness) string {
	t.Helper()
	const slot = "ctl-test"
	if _, err := h.pool.Exec(context.Background(), `
		INSERT INTO entry_vantages (slot, title, token_hash) VALUES ($1, 'control test', decode(repeat('ab', 32), 'hex'))
		ON CONFLICT (slot) DO UPDATE SET status = 'active', retired_at = NULL, token_hash = decode(repeat('ab', 32), 'hex'),
			control_at = NULL, control_fails = 0, control_note = '', kicked_reason = ''`, slot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `UPDATE entry_vantages SET status = 'retired', token_hash = NULL WHERE slot = $1`, slot)
	})
	return slot
}

func TestRecordVantageControl_TwoFailsInARowKickTheSlot(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	slot := controlSlot(t, h)
	bad := VantageControl{Reached: 1, Total: 6, Failed: []string{"ya.ru", "vk.com", "mail.ru", "www.rbc.ru", "www.ozon.ru"}}
	good := VantageControl{Reached: 6, Total: 6}

	// One failure is a spell, not a verdict; a pass wipes it out.
	if kicked, err := h.svc.RecordVantageControl(ctx, slot, bad); err != nil || kicked {
		t.Fatalf("first failure: kicked=%v err=%v", kicked, err)
	}
	if kicked, err := h.svc.RecordVantageControl(ctx, slot, good); err != nil || kicked {
		t.Fatalf("pass: kicked=%v err=%v", kicked, err)
	}
	if kicked, err := h.svc.RecordVantageControl(ctx, slot, bad); err != nil || kicked {
		t.Fatalf("failure after a pass counts from one: kicked=%v err=%v", kicked, err)
	}

	// The second in a row retires the slot, and says why.
	kicked, err := h.svc.RecordVantageControl(ctx, slot, bad)
	if err != nil || !kicked {
		t.Fatalf("second failure in a row: kicked=%v err=%v", kicked, err)
	}
	var status, by, reason string
	var tokenSet bool
	if err := h.pool.QueryRow(ctx, `SELECT status, updated_by, kicked_reason, token_hash IS NOT NULL FROM entry_vantages WHERE slot = $1`, slot).
		Scan(&status, &by, &reason, &tokenSet); err != nil {
		t.Fatal(err)
	}
	if status != VantageRetired || by != "hub:control" || reason == "" || tokenSet {
		t.Fatalf("kicked slot: status=%s by=%s reason=%q tokenSet=%v", status, by, reason, tokenSet)
	}

	// Its prober is a stranger now: reports are refused like any retired slot's.
	if _, err := h.svc.RecordVantageControl(ctx, slot, good); !errors.Is(err, ErrNoSuchVantage) {
		t.Fatalf("report from a retired slot: %v", err)
	}
	if _, err := h.svc.VantageByToken(ctx, "anything"); !errors.Is(err, ErrNoSuchVantage) {
		t.Fatalf("token lookup: %v", err)
	}
}

func TestRecordVantageControl_RefusesNonsense(t *testing.T) {
	h := newHarness(t)
	slot := controlSlot(t, h)
	for name, c := range map[string]VantageControl{
		"no sites":      {Reached: 0, Total: 0},
		"more than all": {Reached: 7, Total: 6},
		"negative":      {Reached: -1, Total: 6},
		"a crawl":       {Reached: 1, Total: 500},
	} {
		if _, err := h.svc.RecordVantageControl(context.Background(), slot, c); !errors.Is(err, ErrRejected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var fails int
	if err := h.pool.QueryRow(context.Background(), `SELECT control_fails FROM entry_vantages WHERE slot = $1`, slot).Scan(&fails); err != nil || fails != 0 {
		t.Fatalf("a refused report must not count: fails=%d err=%v", fails, err)
	}
}
