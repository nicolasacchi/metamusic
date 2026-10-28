package cover

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// MosaicKey is the cache key of in at size: hex sha256 over MosaicVersion, Kind, ID, Seed, size and the first parts, each
// newline terminated (a part is its root id, 0x1F, its seed). Its key space never meets Key's, which hashes Version.
func MosaicKey(in Input, size int) string {
	var b strings.Builder
	b.WriteString(MosaicVersion + "\n" + in.Kind + "\n" + in.ID + "\n" + strconv.FormatInt(in.Seed, 10) + "\n" + strconv.Itoa(size) + "\n")
	for _, p := range in.Parts[:min(len(in.Parts), maxParts)] {
		b.WriteString(p.RootID + "\x1f" + strconv.FormatInt(p.Seed, 10) + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
