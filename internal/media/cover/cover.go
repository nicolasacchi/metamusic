// Package cover renders deterministic cover art (C110.1, D-M4a-15): a pure-Go paletted PNG derived from a
// track's lineage root and seed, cached input-addressed under derived/covers/.
//
// The exported API is frozen by M4a-B2 (ClampSize, Key); Render lives in render.go, the Service in service.go.
package cover

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Version is hashed into every cache key; bump it when the algorithm changes.
const Version = "mm-cover/1"

// ClampSize maps a requested size to a rendered one: n <= 0 gives 512, else n is clamped to [32, 1024] and
// rounded up to a multiple of 32.
func ClampSize(n int) int {
	if n <= 0 {
		return 512
	}
	n = min(max(n, 32), 1024)
	return (n + 31) / 32 * 32
}

// Key is the hex sha256 of Version, root id, seed and size, newline separated.
func Key(rootID string, seed int64, size int) string {
	sum := sha256.Sum256([]byte(Version + "\n" + rootID + "\n" + strconv.FormatInt(seed, 10) + "\n" + strconv.Itoa(size)))
	return hex.EncodeToString(sum[:])
}
