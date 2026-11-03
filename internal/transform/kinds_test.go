package transform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func okSpec(k Kind) KindSpec {
	return KindSpec{
		Kind:         k,
		Validate:     func(p json.RawMessage) (json.RawMessage, error) { return p, nil },
		Units:        func(UnitsIn) int64 { return 1 },
		Title:        func(TitleIn) string { return string(k) },
		Capabilities: func() KindCapabilities { return KindCapabilities{Kind: k} },
	}
}

// withCleanRegistry runs f against an empty registry and restores the real one.
func withCleanRegistry(t *testing.T, f func()) {
	t.Helper()
	kindsMu.Lock()
	saved := kindsReg
	kindsReg = map[Kind]KindSpec{}
	kindsMu.Unlock()
	defer func() { kindsMu.Lock(); kindsReg = saved; kindsMu.Unlock() }()
	f()
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", name)
		}
	}()
	f()
}

// TestKinds_RegistryMechanics: init-time registration panics on a duplicate, an unknown kind and an incomplete spec, and
// RegisteredKinds is in canonical (CHECK-list) order whatever the registration order.
func TestKinds_RegistryMechanics(t *testing.T) {
	withCleanRegistry(t, func() {
		RegisterKind(okSpec(KindBinaural))
		RegisterKind(okSpec(KindMorph))
		RegisterKind(okSpec(KindWiden))
		if got := RegisteredKinds(); !slices.Equal(got, []Kind{KindMorph, KindWiden, KindBinaural}) {
			t.Errorf("RegisteredKinds = %v, want canonical order", got)
		}
		if _, ok := LookupKind(KindWiden); !ok {
			t.Error("LookupKind(widen) failed")
		}
		if _, ok := LookupKind(KindGlacier); ok {
			t.Error("LookupKind found an unregistered kind")
		}
		mustPanic(t, "duplicate", func() { RegisterKind(okSpec(KindMorph)) })
		mustPanic(t, "unknown kind", func() { RegisterKind(okSpec("reverb")) })
		bad := okSpec(KindGlacier)
		bad.Validate = nil
		mustPanic(t, "no Validate", func() { RegisterKind(bad) })
		bad = okSpec(KindGlacier)
		bad.MemFactor = -1
		mustPanic(t, "negative MemFactor", func() { RegisterKind(bad) })
	})
}

// TestKinds_MatchCheckList: the Kind constants are exactly the CHECK list of the migration, in the same order (C225.1), and
// the kinds the registry knows are a subset of them.
func TestKinds_MatchCheckList(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "store", "migrations", "*_transforms.sql"))
	if err != nil || len(files) != 1 {
		t.Fatalf("migration files = %v (%v)", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`kind\s+TEXT NOT NULL CHECK \(kind IN \(([^)]*)\)\)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("kind CHECK not found in the migration")
	}
	var check []Kind
	for _, f := range strings.Split(string(m[1]), ",") {
		check = append(check, Kind(strings.Trim(strings.TrimSpace(f), "'")))
	}
	if !slices.Equal(check, AllKinds) || len(AllKinds) != 9 {
		t.Errorf("CHECK list %v != AllKinds %v", check, AllKinds)
	}
	for _, k := range AllKinds {
		if !k.Valid() {
			t.Errorf("%s is not Valid", k)
		}
	}
	if Kind("reverb").Valid() {
		t.Error("an unknown kind is Valid")
	}
	for _, k := range RegisteredKinds() {
		if !k.Valid() {
			t.Errorf("registered kind %q is outside the nine", k)
		}
	}
}

// TestLimits_C200: the constants other lanes build against.
func TestLimits_C200(t *testing.T) {
	t.Parallel()
	if MaxInputs != 2 || MaxInputS != 600 || ConcurrentRenders != 1 || MaxAttempts != 2 || LeaseTTL.Seconds() != 60 ||
		DSPVersion != "mm-dsp/1" || TargetLUFSDefault != -16.0 || CeilingDBTP != -1.0 || MaxLimitingLU != 6.0 || MaxTitleRunes != 200 {
		t.Error("a C200.4 constant drifted")
	}
}
