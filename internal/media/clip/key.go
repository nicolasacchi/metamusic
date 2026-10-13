package clip

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Samples converts the window to sample indices of the source (exact for
// sample rates that are multiples of 10 Hz because windows sit on a 100 ms grid).
func (s Spec) Samples() (start, end int64) {
	return s.W.StartMS * int64(s.SampleRate) / 1000, s.W.EndMS * int64(s.SampleRate) / 1000
}

// keyDoc lists its fields in alphabetical order, so encoding/json emits the
// compact, key-sorted document the key is the hash of.
type keyDoc struct {
	Channels     int    `json:"channels"`
	Def          string `json:"def"`
	EndSample    int64  `json:"end_sample"`
	FadeMS       int    `json:"fade_ms"`
	GainCDB      int    `json:"gain_cdb"`
	MasterSHA256 string `json:"master_sha256"`
	OutRate      int    `json:"out_rate"`
	StartSample  int64  `json:"start_sample"`
	Tool         string `json:"tool"`
}

// Key is the hex sha256 of the canonical description of the clip (D-M2b-1).
// It does not include the master's path: the same bytes give the same clip.
func Key(s Spec, toolVersion string) string {
	st, en := s.Samples()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(keyDoc{s.Channels, Def, en, FadeMS, s.GainCDB, s.MasterSHA256, OutRate, st, toolVersion}) // cannot fail: plain fields
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return hex.EncodeToString(sum[:])
}
