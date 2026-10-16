package cover_test

import (
	"regexp"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
)

// TestClampSize: 0 gives 512; 10 gives 32; 300 gives 320; 5000 gives 1024 (C110.1).
func TestClampSize(t *testing.T) {
	t.Parallel()
	for in, want := range map[int]int{-5: 512, 0: 512, 1: 32, 10: 32, 32: 32, 33: 64, 300: 320, 512: 512, 1000: 1024, 1024: 1024, 5000: 1024} {
		if got := cover.ClampSize(in); got != want {
			t.Errorf("ClampSize(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestKey_StableAndDistinct(t *testing.T) {
	t.Parallel()
	k := cover.Key("root-a", 1001, 512)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(k) {
		t.Fatalf("key %q is not hex sha256", k)
	}
	if cover.Key("root-a", 1001, 512) != k {
		t.Error("key is not deterministic")
	}
	for name, other := range map[string]string{
		"root": cover.Key("root-b", 1001, 512), "seed": cover.Key("root-a", 1002, 512), "size": cover.Key("root-a", 1001, 1024),
	} {
		if other == k {
			t.Errorf("key ignores the %s", name)
		}
	}
	if cover.Version != "mm-cover/1" {
		t.Errorf("version = %q", cover.Version)
	}
}
