package clip

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// TileRule versions the loop rule of the reference renderer (cut raw, resample to 48 kHz, repeat the window end to
// end, stop at exactly RefMS). It is part of every reference key, so changing the rule can never serve an old clip.
const TileRule = "tile/1"

// refKeyDoc lists its fields in alphabetical order, so encoding/json emits the compact, key-sorted document the key is
// the hash of (the same scheme as keyDoc).
type refKeyDoc struct {
	Channels     int    `json:"channels"`
	Def          string `json:"def"`
	FromMS       int64  `json:"from_ms"`
	MasterSHA256 string `json:"master_sha256"`
	Rate         int    `json:"rate"`
	Tile         string `json:"tile"`
	ToMS         int64  `json:"to_ms"`
	Tool         string `json:"tool"`
}

// RefKey is the cache key of a reference clip: sha256 over RefDef, the master sha, the window, rate, channels, the tool
// version and the tile-rule version. RefDef differs from Def, so it never collides with a duel clip's key. Like Key it
// ignores the master's path: the same bytes give the same clip.
func RefKey(sp RefSpec, toolVersion string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(refKeyDoc{sp.Channels, RefDef, sp.FromMS, sp.MasterSHA256, sp.SampleRate, TileRule, sp.ToMS, toolVersion}) // cannot fail: plain fields
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return hex.EncodeToString(sum[:])
}

// RefTailWindow is the default reference window of a track of durationMS: its last 20 s on the 100 ms grid, or all of
// it when shorter (such a window may still be refused by Reference when under 2 s).
func RefTailWindow(durationMS int64) (fromMS, toMS int64) {
	toMS = durationMS / RefGridMS * RefGridMS
	return max(0, toMS-RefDefaultTailMS), toMS
}

// RefDefaultTailMS is the default reference window length.
const RefDefaultTailMS = 20000
