package tagged_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestTagged_StillByteIdentical pins the sha256 of the whole tagged output (with and without a cover) so the extraction
// of the block builders into flacmeta (D-L2-40) cannot change a byte of a tagged download.
func TestTagged_StillByteIdentical(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cover []byte
		want  string
	}{
		{"cover", testPNG(t, 8), "563bbce4801bafb18dcfdc1ccd5472acfa0465d7b28429121e2e51d8b316511f"},
		{"no_cover", nil, "36e0ed6b80bdefe837ead4e1801e0c6eb570246804f1b7ee9d0645a5f03ceba7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, _ := readAll(t, masterSpec(), fullTags(), c.cover)
			sum := sha256.Sum256(out)
			if got := hex.EncodeToString(sum[:]); got != c.want {
				t.Fatalf("sha256 %s, want %s", got, c.want)
			}
		})
	}
}
