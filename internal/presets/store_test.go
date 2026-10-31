package presets

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/db"
)

func liveStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run presets integration tests")
	}
	ctx := context.Background()
	p, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := db.RunMigrations(ctx, p); err != nil {
		t.Fatal(err)
	}
	// The tables are this package's alone; start each test from none.
	if _, err := p.Exec(ctx, `
		UPDATE presets SET current_release = NULL, next_release = NULL;
		DELETE FROM preset_releases; DELETE FROM presets; DELETE FROM preset_groups;
		UPDATE preset_epoch SET value = 2`); err != nil {
		t.Fatal(err)
	}
	return NewStore(p)
}

func steam() Group {
	return Group{ID: "steam", Name: "Steam", Icon: "steam", Rules: Rules{
		Domains:   []string{"steampowered.com"},
		Processes: map[string][]string{"windows": {"steam.exe"}},
	}}
}

func discord() Group {
	return Group{ID: "discord", Name: "Discord", Icon: "discord", Rules: Rules{Domains: []string{"discord.com"}}}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func serve(t *testing.T, s *Store, country string, bucket int) (Served, bool) {
	t.Helper()
	sv, ok, err := s.Serve(context.Background(), country, bucket)
	must(t, err)
	return sv, ok
}

// A release freezes the groups: editing one afterwards reaches no device
// until the next release, and each side becomes the service's defaultModes.
func TestAReleaseFreezesTheGroupsOnTheirSides(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	must(t, s.PutGroup(ctx, steam()))
	must(t, s.PutGroup(ctx, discord()))
	must(t, s.CreatePreset(ctx, "ru", PresetDraft{Name: "россия", Default: true, Bypass: []string{"steam"}, Proxy: []string{"discord"}}))

	if _, ok := serve(t, s, "", 0); ok {
		t.Fatal("a preset never released is served")
	}
	r, err := s.Release(ctx, "ru", 100, "owner")
	must(t, err)
	if r.V != 1 {
		t.Fatalf("first release is v%d", r.V)
	}

	edited := steam()
	edited.Rules.Domains = []string{"steamcommunity.com"}
	must(t, s.PutGroup(ctx, edited))

	sv, ok := serve(t, s, "", 42)
	if !ok || len(sv.Services) != 2 {
		t.Fatalf("served %v %+v", ok, sv)
	}
	if got := sv.Services[0]; got.ID != "steam" || got.Domains[0] != "steampowered.com" || got.DefaultModes[0] != SideBypass {
		t.Fatalf("steam served as %+v", got)
	}
	if got := sv.Services[1]; got.ID != "discord" || got.DefaultModes[0] != SideProxy {
		t.Fatalf("discord served as %+v", got)
	}
}

// Buckets below the stage get the release rolling out, the rest keep
// current, and the version grows on every step, back or forward, so no
// device ever sees it go down.
func TestTheRolloutSplitsByBucketAndTheVersionOnlyGrows(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	must(t, s.PutGroup(ctx, steam()))
	must(t, s.PutGroup(ctx, discord()))
	must(t, s.CreatePreset(ctx, "ru", PresetDraft{Name: "россия", Default: true, Bypass: []string{"steam"}}))
	v1, err := s.Release(ctx, "ru", 100, "owner")
	must(t, err)

	must(t, s.UpdatePreset(ctx, "ru", PresetDraft{Name: "россия", Default: true, Bypass: []string{"steam"}, Proxy: []string{"discord"}}))
	v2, err := s.Release(ctx, "ru", 25, "owner")
	must(t, err)

	low, _ := serve(t, s, "", 24)
	high, _ := serve(t, s, "", 25)
	if low.Release != v2.ID || high.Release != v1.ID {
		t.Fatalf("bucket 24 got %d, bucket 25 got %d; want %d and %d", low.Release, high.Release, v2.ID, v1.ID)
	}
	if low.Version != high.Version {
		t.Fatal("one epoch, two versions")
	}

	versions := []int64{low.Version}
	must(t, s.SetStage(ctx, "ru", 5))
	sv, _ := serve(t, s, "", 24)
	if sv.Release != v1.ID {
		t.Fatal("bucket 24 kept the release after the stage went back to 5")
	}
	versions = append(versions, sv.Version)

	must(t, s.Revert(ctx, "ru"))
	sv, _ = serve(t, s, "", 0)
	if sv.Release != v1.ID {
		t.Fatal("bucket 0 kept the reverted release")
	}
	versions = append(versions, sv.Version)

	_, err = s.Release(ctx, "ru", 100, "owner")
	must(t, err)
	sv, _ = serve(t, s, "", 99)
	versions = append(versions, sv.Version)
	if len(sv.Services) != 2 {
		t.Fatal("100% did not make the release current")
	}
	for i := 1; i < len(versions); i++ {
		if versions[i] <= versions[i-1] {
			t.Fatalf("version went %v", versions)
		}
	}
	if versions[0] < 3 {
		t.Fatalf("version %d is not above the bundled catalog's 2", versions[0])
	}
	if err := s.SetStage(ctx, "ru", 50); !errors.Is(err, ErrNoNext) {
		t.Fatalf("stage with nothing rolling out: %v", err)
	}
}

// A country's preset wins over the default, and the default flag moves.
func TestACountryPresetWinsOverTheDefault(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	must(t, s.PutGroup(ctx, steam()))
	must(t, s.PutGroup(ctx, discord()))
	must(t, s.CreatePreset(ctx, "world", PresetDraft{Name: "мир", Default: true, Proxy: []string{"discord"}}))
	must(t, s.CreatePreset(ctx, "ru", PresetDraft{Name: "россия", Countries: []string{"RU"}, Bypass: []string{"steam"}}))
	world, err := s.Release(ctx, "world", 100, "owner")
	must(t, err)
	ru, err := s.Release(ctx, "ru", 100, "owner")
	must(t, err)

	if sv, _ := serve(t, s, "RU", 0); sv.Release != ru.ID {
		t.Fatal("RU did not get its preset")
	}
	if sv, _ := serve(t, s, "DE", 0); sv.Release != world.ID {
		t.Fatal("DE did not get the default")
	}

	must(t, s.UpdatePreset(ctx, "ru", PresetDraft{Name: "россия", Default: true, Countries: []string{"RU"}, Bypass: []string{"steam"}}))
	ps, err := s.Presets(ctx)
	must(t, err)
	if !ps[0].Default || ps[0].ID != "ru" || ps[1].Default {
		t.Fatalf("default did not move: %+v", ps)
	}
	if err := s.DeletePreset(ctx, "ru"); !errors.Is(err, ErrDefault) {
		t.Fatalf("deleted the default: %v", err)
	}
	must(t, s.DeletePreset(ctx, "world"))
	if sv, _ := serve(t, s, "DE", 0); sv.Release != ru.ID {
		t.Fatal("DE did not move to the new default")
	}
}

func TestDraftsAndGroupsGuardEachOther(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	must(t, s.PutGroup(ctx, steam()))
	if err := s.CreatePreset(ctx, "ru", PresetDraft{Name: "россия", Bypass: []string{"nope"}}); !errors.Is(err, ErrUnknownGroup) {
		t.Fatalf("unknown group: %v", err)
	}
	if err := s.CreatePreset(ctx, "ru", PresetDraft{Name: "россия", Bypass: []string{"steam"}, Proxy: []string{"steam"}}); !errors.Is(err, ErrGroupTwice) {
		t.Fatalf("both sides: %v", err)
	}
	must(t, s.CreatePreset(ctx, "ru", PresetDraft{Name: "россия", Bypass: []string{"steam"}}))
	if err := s.CreatePreset(ctx, "ru", PresetDraft{Name: "ещё"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate preset: %v", err)
	}
	if err := s.DeleteGroup(ctx, "steam"); !errors.Is(err, ErrGroupInUse) {
		t.Fatalf("deleted a group in a draft: %v", err)
	}
	if _, err := s.Release(ctx, "ghost", 100, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("release of nothing: %v", err)
	}
	must(t, s.UpdatePreset(ctx, "ru", PresetDraft{Name: "россия"}))
	if _, err := s.Release(ctx, "ru", 100, ""); !errors.Is(err, ErrNothingToShip) {
		t.Fatalf("empty release: %v", err)
	}
	must(t, s.DeleteGroup(ctx, "steam"))
	if err := s.DeleteGroup(ctx, "steam"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
