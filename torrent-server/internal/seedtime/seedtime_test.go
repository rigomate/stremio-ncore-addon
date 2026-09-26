package seedtime

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSeedingTimeSurvivesRestartWithoutCountingDowntime(t *testing.T) {
	file := filepath.Join(t.TempDir(), "history.json")
	store, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Register("bh", "bithumen"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	observe := func(s *Store, at time.Time, active bool) {
		t.Helper()
		if err := s.Observe(at, map[string]bool{"bh": active}); err != nil {
			t.Fatal(err)
		}
	}
	observe(store, now, false)
	observe(store, now.Add(time.Minute), true)
	if store.Get("bh").Seconds != 0 {
		t.Fatal("counted time before pieces became available")
	}
	observe(store, now.Add(2*time.Minute), true)
	if store.Get("bh").Seconds != 60 {
		t.Fatal("did not count active seeding")
	}
	restarted, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	observe(restarted, now.Add(24*time.Hour), true)
	if restarted.Get("bh").Seconds != 60 {
		t.Fatal("counted downtime")
	}
	observe(restarted, now.Add(24*time.Hour+time.Minute), true)
	if restarted.Get("bh").Seconds != 120 {
		t.Fatal("lost persisted time")
	}
	observe(restarted, now.Add(48*time.Hour), true)
	if restarted.Get("bh").Seconds != 120 {
		t.Fatal("counted a suspended process")
	}
	observe(restarted, now.Add(48*time.Hour+time.Minute), false)
	observe(restarted, now.Add(48*time.Hour+2*time.Minute), true)
	if restarted.Get("bh").Seconds != 120 {
		t.Fatal("counted paused seeding")
	}
	if err := restarted.Register("bh", "ncore"); err != nil {
		t.Fatal(err)
	}
	if restarted.Get("bh").Source != "bithumen" {
		t.Fatal("lost protection for shared hash")
	}
	if err := restarted.Remove("bh"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Get("bh").Source != "" {
		t.Fatal("deletion did not reset history")
	}
}

func TestEightDaysOfSeeding(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Register("bh", "bithumen"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for minute := 0; minute <= 8*24*60; minute++ {
		if err := store.Observe(now.Add(time.Duration(minute)*time.Minute), map[string]bool{"bh": true}); err != nil {
			t.Fatal(err)
		}
	}
	if store.Get("bh").Seconds != 8*24*60*60 {
		t.Fatal("incorrect eight-day seeding duration")
	}
}
