package auth

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// subsonicKeyLiteral matches a Subsonic apiKey-shaped literal: the prefix followed by a run of
// base32 characters long enough to be a real credential. Assembled at runtime so this file holds no
// key-shaped literal itself.
var subsonicKeyLiteral = regexp.MustCompile(SubsonicAPIKeyPrefix + `[a-z2-7]{20,}`)

func TestNoSubsonicKeyLiterals(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	var scanned int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "bin", "data", "covdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		scanned++
		if loc := subsonicKeyLiteral.FindIndex(b); loc != nil {
			t.Errorf("%s: contains a Subsonic key literal (build keys at runtime from canaries, never commit one)", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d test files; the walk is not reaching the repository", scanned)
	}
}

func TestNoSubsonicKeyLiterals_Detector(t *testing.T) {
	t.Parallel()
	planted := `k := "` + SubsonicAPIKeyPrefix + strings.Repeat("a2b3", 13) + `"`
	if !subsonicKeyLiteral.MatchString(planted) {
		t.Error("detector misses a full-length key literal")
	}
	for _, ok := range []string{`"` + SubsonicAPIKeyPrefix + `"+strings.Repeat("a", 52)`, `apiKey=` + SubsonicAPIKeyPrefix + `short`, `^` + SubsonicAPIKeyPrefix + `[a-z2-7]{52}$`} {
		if subsonicKeyLiteral.MatchString(ok) {
			t.Errorf("detector flags %q", ok)
		}
	}
}
