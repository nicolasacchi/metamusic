package sequencer

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Digest is the sha256 of the canonical inputs of a plan: the definition tag, the options, and every song in input
// order with its features and bases. Strings are length-prefixed, so field boundaries are unambiguous.
func Digest(songs []Song, o Options) string {
	h := sha256.New()
	str := func(s string) {
		h.Write([]byte(strconv.Itoa(len(s)) + ":" + s))
	}
	num := func(v int) { str(strconv.Itoa(v)) }
	str(ExplanationDef)
	str(string(o.Strategy))
	str(o.First)
	str(o.Last)
	num(len(songs))
	for _, s := range songs {
		str(s.ID)
		num(s.BPMx100)
		num(s.Energyx10000)
		if s.Key == nil {
			num(0)
		} else {
			num(1)
			num(s.Key.Num)
			num(int(s.Key.Letter))
		}
		str(s.Bases.BPM)
		str(s.Bases.Energy)
		str(s.Bases.Key)
	}
	return hex.EncodeToString(h.Sum(nil))
}
