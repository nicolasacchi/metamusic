package config

import (
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Soundfont is one entry of MM_SOUNDFONTS: an operator-chosen name and the absolute path of a read-only SF2/SF3 file (T1b).
// The API only ever accepts the name.
type Soundfont struct{ Name, Path string }

var soundfontNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// maxSoundfonts is the longest MM_SOUNDFONTS list.
const maxSoundfonts = 8

// loadT1 reads the transform settings (C200). Errors name the variable, never a value.
func loadT1(c *Config, getenv func(string) string) error {
	c.TransformEnabled = true
	if v := getenv("MM_TRANSFORM_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return errors.New("config: MM_TRANSFORM_ENABLED must be a boolean")
		}
		c.TransformEnabled = b
	}
	for _, f := range []struct {
		name        string
		dst         *int
		def, lo, hi int64
	}{
		{"MM_TRANSFORM_QUEUE_MAX", &c.TransformQueueMax, 6, 1, 32},
		{"MM_TRANSFORM_MAX_CPU_S", &c.TransformMaxCPUS, 600, 30, 3600},
		{"MM_TRANSFORM_MAX_OUT_S", &c.TransformMaxOutS, 900, 60, 3600},
		{"MM_TRANSFORM_MAX_MEM_MB", &c.TransformMaxMemMB, 1024, 256, 8192},
		{"MM_TRANSFORM_AGENT_DAILY_CPU_S", &c.TransformAgentDailyCPUS, 1800, 0, 86400},
		{"MM_TRANSFORM_HUMAN_DAILY_CPU_S", &c.TransformHumanDailyCPUS, 14400, 0, 86400},
	} {
		n, err := intVar(getenv, f.name, f.def, f.lo, f.hi)
		if err != nil {
			return err
		}
		*f.dst = int(n)
	}
	if v := getenv("MM_SOUNDFONTS"); v != "" {
		sfs, err := parseSoundfonts(v)
		if err != nil {
			return err
		}
		c.Soundfonts = sfs
	}
	if v := getenv("MM_FLUIDSYNTH"); v != "" {
		if !filepath.IsAbs(v) {
			return errors.New("config: MM_FLUIDSYNTH must be an absolute path")
		}
		c.FluidsynthPath = v
	}
	return nil
}

// parseSoundfonts parses "name=/abs/file.sf2[;name=/abs/file.sf3…]": names ^[a-z][a-z0-9_-]{0,31}$, at most eight, unique,
// absolute clean paths ending in .sf2 or .sf3. The files are not opened here (a missing file disables only score_render).
func parseSoundfonts(raw string) ([]Soundfont, error) {
	bad := errors.New(`config: MM_SOUNDFONTS must be "<name>=<absolute .sf2/.sf3 path>[;…]" (unique names ^[a-z][a-z0-9_-]{0,31}$, at most 8)`)
	var out []Soundfont
	seen := map[string]bool{}
	for _, p := range strings.Split(raw, ";") {
		name, path, ok := strings.Cut(strings.TrimSpace(p), "=")
		name, path = strings.TrimSpace(name), strings.TrimSpace(path)
		ext := strings.ToLower(filepath.Ext(path))
		if !ok || !soundfontNameRE.MatchString(name) || seen[name] || !filepath.IsAbs(path) ||
			filepath.Clean(path) != path || (ext != ".sf2" && ext != ".sf3") {
			return nil, bad
		}
		seen[name] = true
		out = append(out, Soundfont{Name: name, Path: path})
	}
	if len(out) > maxSoundfonts {
		return nil, bad
	}
	return out, nil
}
