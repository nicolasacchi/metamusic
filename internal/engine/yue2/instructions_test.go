package yue2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInstructions_Golden(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "instructions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != 3 {
		t.Fatalf("golden has %d entries", len(want))
	}
	if diff := cmp.Diff(want, Instructions); diff != "" {
		t.Fatal(diff)
	}
}
