package autoindex

import (
	"testing"
	"time"

	"github.com/VeyrForge/codehelper/internal/indexer"
	"github.com/VeyrForge/codehelper/internal/meta"
	"github.com/VeyrForge/codehelper/internal/watcher"
)

func TestApplyScaleDefaults_LargeRepoAppliesSmootherDefaults(t *testing.T) {
	opt := Options{}
	m := &meta.Data{FileCount: 9000, SymbolCount: 50000}
	got := applyScaleDefaults(opt, m)
	if got.Cooldown != 1500*time.Millisecond {
		t.Fatalf("cooldown=%s", got.Cooldown)
	}
	if got.Debounce != 650*time.Millisecond {
		t.Fatalf("debounce=%s", got.Debounce)
	}
	if got.MaxWait != 5*time.Second {
		t.Fatalf("max_wait=%s", got.MaxWait)
	}
	if got.Invalidation != indexer.InvalidationLazy {
		t.Fatalf("invalidation=%q", got.Invalidation)
	}
}

func TestApplyScaleDefaults_SmallProfileUsesFastDefaults(t *testing.T) {
	opt := Options{WatchProfile: "small"}
	got := applyScaleDefaults(opt, &meta.Data{FileCount: 50000, SymbolCount: 500000})
	if got.Cooldown != 750*time.Millisecond {
		t.Fatalf("cooldown=%s", got.Cooldown)
	}
	if got.Debounce != watcher.DefaultDebounce {
		t.Fatalf("debounce=%s", got.Debounce)
	}
	if got.MaxWait != watcher.DefaultMaxWait {
		t.Fatalf("max_wait=%s", got.MaxWait)
	}
	if got.Invalidation != indexer.InvalidationEager {
		t.Fatalf("invalidation=%q", got.Invalidation)
	}
}

func TestApplyScaleDefaults_AutoWithoutMetaFallsBackToSmall(t *testing.T) {
	got := applyScaleDefaults(Options{}, nil)
	if got.WatchProfile != "auto" {
		t.Fatalf("watch_profile=%q", got.WatchProfile)
	}
	if got.Cooldown != 750*time.Millisecond {
		t.Fatalf("cooldown=%s", got.Cooldown)
	}
}

func TestApplyScaleDefaults_RespectsExplicitOptions(t *testing.T) {
	opt := Options{
		Cooldown:     time.Second,
		Debounce:     123 * time.Millisecond,
		MaxWait:      2 * time.Second,
		Invalidation: indexer.InvalidationEager,
	}
	m := &meta.Data{FileCount: 20000, SymbolCount: 200000}
	got := applyScaleDefaults(opt, m)
	if got.Cooldown != opt.Cooldown || got.Debounce != opt.Debounce || got.MaxWait != opt.MaxWait {
		t.Fatalf("explicit timings changed: %+v", got)
	}
	if got.Invalidation != indexer.InvalidationEager {
		t.Fatalf("explicit invalidation changed: %q", got.Invalidation)
	}
}
