package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSF2Gen(t *testing.T) {
	out := filepath.Join(t.TempDir(), "t.sf2")
	if err := run([]string{"-out", out, "-release", "1s", "-presets", "4"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil || len(b) < 1000 || string(b[:4]) != "RIFF" || string(b[8:12]) != "sfbk" {
		t.Fatalf("%d bytes, err %v", len(b), err)
	}
	for _, bad := range [][]string{nil, {"-out", out, "extra"}, {"-out", out, "-presets", "0x"}, {"-out", out, "-presets", "500"}, {"-bogus"}} {
		if err := run(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
