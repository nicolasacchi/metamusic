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
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], modulePath) {
			continue
		}
		checked++
		pkg := fields[0]
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
