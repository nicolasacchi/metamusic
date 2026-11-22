package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, rel ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t)}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDockerfile_SoundfontPinned (C356.6): the app image fetches the soundfont package with ADD --checksum, checks the extracted
// file against its own pin, and both pins equal the scripts/tools.sh table. The .sf2 itself is never committed.
func TestDockerfile_SoundfontPinned(t *testing.T) {
	t.Parallel()
	df := strings.ReplaceAll(readRepoFile(t, "Dockerfile"), "\\\n", " ")
	tools := readRepoFile(t, "scripts", "tools.sh")
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)

	pin := func(key string) string {
		m := regexp.MustCompile(`(?m)^\s*\[` + regexp.QuoteMeta(key) + `\]=([0-9a-f]{64})\s*$`).FindStringSubmatch(tools)
		if m == nil {
			t.Fatalf("scripts/tools.sh has no pin %s", key)
		}
		return m[1]
	}
	deb, sf2 := pin("soundfont-deb"), pin("soundfont-sf2")
	if !sha.MatchString(deb) || !sha.MatchString(sf2) || deb == sf2 {
		t.Fatalf("soundfont pins malformed or equal: %s %s", deb, sf2)
	}

	add := regexp.MustCompile(`(?m)^ADD\s+--checksum=sha256:([0-9a-f]{64})\s+(https://snapshot\.debian\.org/\S+fluid-soundfont-gm_\S+\.deb)\s`).FindStringSubmatch(df)
	if add == nil {
		t.Fatal("Dockerfile has no ADD --checksum=sha256:<pin> of the snapshot.debian.org package")
	}
	if add[1] != deb {
		t.Errorf("Dockerfile ADD checksum %s != tools.sh soundfont-deb %s", add[1], deb)
	}
	dir, file := add[2][:strings.LastIndex(add[2], "/")+1], add[2][strings.LastIndex(add[2], "/")+1:]
	if !strings.Contains(tools, dir) || !strings.Contains(tools, "SOUNDFONT_DEB="+file) {
		t.Errorf("Dockerfile URL %s does not match the tools.sh snapshot URL and SOUNDFONT_DEB", add[2])
	}
	if !strings.Contains(df, sf2+"  ") && !strings.Contains(df, sf2+" ") {
		t.Errorf("Dockerfile does not check the extracted file against %s", sf2)
	}
	for _, want := range []string{"sha256sum -c", "FluidR3_GM.sf2", "/opt/mm/soundfonts/", "fluidsynth --version", "apk add"} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile lost %q", want)
		}
	}
	if !regexp.MustCompile(`apk add[^\n]*\bfluidsynth\b`).MatchString(df) {
		t.Error("final stage does not apk add fluidsynth")
	}
	// the soundfont layer comes before the binary COPY so it caches across releases
	if i, j := strings.Index(df, "COPY --from=soundfont"), strings.Index(df, "COPY --from=build /out/metamusic"); i < 0 || j < 0 || i > j {
		t.Error("COPY --from=soundfont must precede the binary COPY")
	}
	for _, l := range strings.Split(df, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "ENV") && strings.Contains(l, "MM_SOUNDFONTS") {
			t.Error("the image must not set MM_SOUNDFONTS (the operator does)")
		}
	}
	// no soundfont binary in the repo
	err := filepath.WalkDir(repoRoot(t), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "bin" || d.Name() == "data") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".sf2") {
			info, _ := d.Info()
			if info != nil && info.Size() > 1<<20 {
				t.Errorf("large soundfont committed: %s", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestThirdPartyNotices_ListsSoundfont (C356.4): notices name the soundfont and fluidsynth, and the MIT licence text with the
// upstream copyright line is committed.
func TestThirdPartyNotices_ListsSoundfont(t *testing.T) {
	t.Parallel()
	n := readRepoFile(t, "THIRD_PARTY_NOTICES.md")
	for _, want := range []string{"FluidR3_GM soundfont (app image)", "third_party/soundfonts/FluidR3_GM-LICENSE.txt", "MIT",
		"fluidsynth (app image", "LGPL-2.1-or-later", "separate process"} {
		if !strings.Contains(n, want) {
			t.Errorf("THIRD_PARTY_NOTICES.md lacks %q", want)
		}
	}
	lic := readRepoFile(t, "third_party", "soundfonts", "FluidR3_GM-LICENSE.txt")
	for _, want := range []string{"Copyright © 2000-2002, 2008 Frank Wen", "Permission is hereby granted, free of charge", "WITHOUT WARRANTY OF ANY KIND"} {
		if !strings.Contains(lic, want) {
			t.Errorf("licence text lacks %q", want)
		}
	}
}

// TestCI_InstallsFluidsynth: every apt step installs fluidsynth and the unit tier requires it, so the synth media tests cannot
// skip silently.
func TestCI_InstallsFluidsynth(t *testing.T) {
	t.Parallel()
	ci := readRepoFile(t, ".github", "workflows", "ci.yml")
	if got := strings.Count(ci, "scripts/apt-install.sh flac fluidsynth"); got != 2 {
		t.Errorf("ci.yml has %d 'apt-install.sh flac fluidsynth' calls, want 2", got)
	}
	if got := strings.Count(ci, "MM_REQUIRE_FLUIDSYNTH=1"); got < 1 {
		t.Error("ci.yml does not set MM_REQUIRE_FLUIDSYNTH=1")
	}
}
