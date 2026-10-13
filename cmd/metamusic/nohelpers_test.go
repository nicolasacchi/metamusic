package main

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/nicolasacchi/metamusic/"

// TestNoTestHelpersInBinary fails if the shipped binary links a test-only
// helper package of this module, or any module package that imports "testing".
func TestNoTestHelpersInBinary(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	helpers := map[string]bool{
		modulePath + "internal/testtools":               true,
		modulePath + "internal/media/flacmeta/flactest": true,
		modulePath + "internal/library/librarytest":     true,
		modulePath + "internal/servertest":              true,
		modulePath + "internal/archivetest":             true,
		// M2b (C43)
		modulePath + "internal/media/clip/cliptest": true,
		modulePath + "internal/duels/duelstest":     true,
		modulePath + "internal/ears/earstest":       true,
	}
	// Forbidden import edges (C43), asserted from the same `go list -deps` output: pairs of module-relative
	// package prefixes, from -> to. A prefix ending in "/" matches the whole subtree.
	forbidden := [][2]string{
		{"internal/duels", "internal/ears"}, {"internal/ears", "internal/duels"},
		{"internal/media/clip", "internal/duels"}, {"internal/media/clip", "internal/ears"},
		{"internal/library", "internal/media/clip"}, {"internal/library", "internal/duels"}, {"internal/library", "internal/ears"}, {"internal/library", "internal/trackctx"},
		{"internal/ears", "internal/trackctx"}, {"internal/duels", "internal/trackctx"},
		{"internal/media/clip", "internal/api"}, {"internal/ladder", "internal/api"}, {"internal/duels", "internal/api"}, {"internal/ears", "internal/api"},
		{"internal/analysis", "internal/api"}, {"internal/textfold", "internal/api"},
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], modulePath) {
			continue
		}
		checked++
		pkg := fields[0]
		for _, imp := range fields[1:] {
			for _, f := range forbidden {
				if under(pkg, f[0]) && under(imp, f[1]) {
					t.Errorf("forbidden import edge %s -> %s (C43)", pkg, imp)
				}
			}
		}
		if helpers[pkg] {
			t.Errorf("test helper package %s is linked into the binary", pkg)
		}
		for _, imp := range fields[1:] {
			if imp == "testing" {
				t.Errorf("package %s imports testing", pkg)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("only %d module packages listed; go list output unexpected", checked)
	}
}

// isHelperSubtree: "internal/media/clip" does not own its cliptest helper for the purpose of the edge rules
// (the helper may import duels; it is test-only and asserted absent from the binary above).
func isHelperSubtree(pkg, prefix string) bool {
	return strings.HasSuffix(pkg, "test") && strings.HasPrefix(pkg, prefix+"/")
}

// under reports whether pkg is prefix or inside it; test-only helper subtrees (cliptest, ...) are not part of
// the production package for the edge rules.
func under(pkg, prefix string) bool {
	pkg, prefix = strings.TrimPrefix(pkg, modulePath), strings.TrimPrefix(prefix, modulePath)
	return pkg == prefix || strings.HasPrefix(pkg, prefix+"/") && !isHelperSubtree(pkg, prefix)
}

func TestForbiddenEdgeMatcher(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		pkg, prefix string
		want        bool
	}{
		{modulePath + "internal/duels", "internal/duels", true},
		{modulePath + "internal/duels/duelstest", "internal/duels", false},
		{modulePath + "internal/media/clip", "internal/media/clip", true},
		{modulePath + "internal/media/clip/cliptest", "internal/media/clip", false},
		{modulePath + "internal/analysis/ffmpeg", "internal/analysis", true},
		{modulePath + "internal/duelsx", "internal/duels", false},
		{modulePath + "internal/library", "internal/media/clip", false},
	} {
		if got := under(tc.pkg, tc.prefix); got != tc.want {
			t.Errorf("under(%s, %s) = %v, want %v", tc.pkg, tc.prefix, got, tc.want)
		}
	}
}
