package entry_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

type trialFakeProvider struct {
	entry.BoxProvider
	mu             sync.Mutex
	calls, deletes int
	createErr      error
	list           []entry.BoxInstance
}

func (f *trialFakeProvider) Create(_ context.Context, o entry.BoxOrder) (entry.BoxInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.createErr != nil {
		return entry.BoxInstance{}, f.createErr
	}
	inst := entry.BoxInstance{ID: fmt.Sprint(f.calls), Label: o.Label}
	f.list = append(f.list, inst)
	return inst, nil
}
func (f *trialFakeProvider) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	return nil
}
func (f *trialFakeProvider) Instances(context.Context) ([]entry.BoxInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]entry.BoxInstance(nil), f.list...), nil
}
func guardedTrial(f *trialFakeProvider) *trialHost {
	h := &trialHost{BoxProvider: f, creates: new(atomic.Int32), attempts: new(atomic.Int32)}
	h.arm([]entry.BoxInstance{{ID: "baseline", Label: "nekko-old"}}, map[string]bool{"old": true})
	return h
}
func trialOrder(n int) entry.BoxOrder {
	name := fmt.Sprintf("ams%d", n)
	return entry.BoxOrder{Hostname: name, Label: "nekko-" + name}
}

func TestTrialConcurrentCreatesNeverExceedTwo(t *testing.T) {
	f := &trialFakeProvider{}
	h := guardedTrial(f)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, _ = h.Create(context.Background(), trialOrder(i)) }(i)
	}
	wg.Wait()
	if f.calls != 2 || h.creates.Load() != 2 {
		t.Fatalf("provider calls %d, reserved %d", f.calls, h.creates.Load())
	}
}
func TestTrialUnknownCreateKeepsItsSlot(t *testing.T) {
	f := &trialFakeProvider{createErr: context.DeadlineExceeded}
	h := guardedTrial(f)
	for i := 0; i < 3; i++ {
		_, _ = h.Create(context.Background(), trialOrder(i))
	}
	if f.calls != 2 {
		t.Fatalf("unknown answers allowed %d provider calls", f.calls)
	}
}
func TestTrialDefiniteRefusalReleasesItsSlot(t *testing.T) {
	f := &trialFakeProvider{createErr: entry.ErrProviderRefused}
	h := guardedTrial(f)
	for i := 0; i < 3; i++ {
		_, _ = h.Create(context.Background(), trialOrder(i))
	}
	if f.calls != 3 || h.creates.Load() != 0 {
		t.Fatalf("calls %d reserved %d", f.calls, h.creates.Load())
	}
}
func TestTrialDeletionRequiresOwnedID(t *testing.T) {
	f := &trialFakeProvider{}
	h := guardedTrial(f)
	for _, id := range []string{"baseline", "unrelated"} {
		if err := h.Delete(context.Background(), id); err == nil {
			t.Fatalf("allowed deletion %s", id)
		}
	}
	inst, err := h.Create(context.Background(), trialOrder(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Delete(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if f.deletes != 1 {
		t.Fatalf("forwarded %d deletes", f.deletes)
	}
}
func TestTrialLostAnswerRecoveredOnlyByAttemptedLabel(t *testing.T) {
	f := &trialFakeProvider{createErr: context.DeadlineExceeded}
	h := guardedTrial(f)
	_, _ = h.Create(context.Background(), trialOrder(1))
	f.list = []entry.BoxInstance{{ID: "baseline", Label: "nekko-ams1"}, {ID: "recovered", Label: "nekko-ams1"}, {ID: "foreign", Label: "nekko-ams9"}}
	if _, err := h.Instances(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Delete(context.Background(), "recovered"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"baseline", "foreign"} {
		if err := h.Delete(context.Background(), id); err == nil {
			t.Fatalf("allowed deletion %s", id)
		}
	}
}
func TestTrialRefusesUnarmedAndBaselineOrders(t *testing.T) {
	f := &trialFakeProvider{}
	h := guardedTrial(f)
	for _, o := range []entry.BoxOrder{{Hostname: "old", Label: "nekko-old"}, {Hostname: "ams1", Label: "wrong"}, {}} {
		if _, err := h.Create(context.Background(), o); !errors.Is(err, entry.ErrProviderRefused) {
			t.Fatalf("order %+v: %v", o, err)
		}
	}
	h.armed = false
	if _, err := h.Create(context.Background(), trialOrder(1)); !errors.Is(err, entry.ErrProviderRefused) {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Fatalf("forwarded %d creates", f.calls)
	}
}
func TestTrialBaselineMustRemainPresent(t *testing.T) {
	h := guardedTrial(&trialFakeProvider{})
	if err := h.baselinePresent([]entry.BoxInstance{{ID: "other"}}); err == nil {
		t.Fatal("missing baseline accepted")
	}
	if err := h.baselinePresent([]entry.BoxInstance{{ID: "baseline"}, {ID: "new"}}); err != nil {
		t.Fatal(err)
	}
	if h.ownsName("") || h.ownsName("old") {
		t.Fatal("baseline or empty name owned")
	}
}
func TestTrialCannotLoadRegistrar(t *testing.T) {
	c := &trialConnector{}
	if _, err := c.Registrar("porkbun", nil); !errors.Is(err, entry.ErrNoRegistrar) {
		t.Fatal(err)
	}
}
