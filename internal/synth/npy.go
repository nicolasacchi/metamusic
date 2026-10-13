package synth

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strings"
)

// NPYOptions describes a synthetic semantic.npy (C55).
type NPYOptions struct {
	TokensPerS, Seconds float64
	Vocab               int
	Seed                uint64
	LoopAtS             float64
	Period, Repeats     int // Repeats 0 means no loop
}

const maxNPYTokens = 1 << 22

// SemanticNPY writes an '<i8' array of shape (1, n): uniform tokens from
// PCG(Seed); when Repeats > 0, one random Period-token pattern repeats Repeats
// times from LoopAtS. dst must be absolute.
func SemanticNPY(dst string, o NPYOptions) error {
	if err := needAbs(dst); err != nil {
		return err
	}
	if !(o.TokensPerS > 0) || !(o.Seconds > 0) || math.IsInf(o.TokensPerS*o.Seconds, 0) || o.TokensPerS*o.Seconds > maxNPYTokens {
		return errors.New("synth: semantic npy needs a positive, bounded token count")
	}
	if o.Vocab < 1 || o.LoopAtS < 0 || o.Period < 0 || o.Repeats < 0 || (o.Repeats > 0 && o.Period < 1) {
		return errors.New("synth: invalid semantic npy options")
	}
	n := int(math.Round(o.TokensPerS * o.Seconds))
	r := rand.New(rand.NewPCG(o.Seed, o.Seed^0x9e3779b97f4a7c15)) // #nosec G404 -- deterministic fixture data
	tok := make([]int64, n)
	for i := range tok {
		tok[i] = int64(r.IntN(o.Vocab))
	}
	if o.Repeats > 0 {
		pat := make([]int64, o.Period)
		for i := range pat {
			pat[i] = int64(r.IntN(o.Vocab))
		}
		start := int(math.Round(o.LoopAtS * o.TokensPerS))
		if start+o.Period*o.Repeats > n {
			return errors.New("synth: loop region exceeds the stream")
		}
		for i := 0; i < o.Period*o.Repeats; i++ {
			tok[start+i] = pat[i%o.Period]
		}
	}
	hdr := fmt.Sprintf("{'descr': '<i8', 'fortran_order': False, 'shape': (1, %d), }", n)
	pad := 64 - (10+len(hdr)+1)%64
	hdr += strings.Repeat(" ", pad%64) + "\n"
	var b bytes.Buffer
	b.WriteString("\x93NUMPY\x01\x00")
	_ = binary.Write(&b, binary.LittleEndian, uint16(len(hdr))) // #nosec G115 -- header is < 128 bytes
	b.WriteString(hdr)
	for _, v := range tok {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	tmp := dst + ".tmp"
	return publish(tmp, dst, os.WriteFile(tmp, b.Bytes(), 0o600))
}
