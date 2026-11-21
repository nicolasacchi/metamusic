package dsptest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/dsp"
)

// SF2Options configures WriteSF2.
type SF2Options struct {
	// Release is the volume-envelope release time. Zero is instant (about 1 ms); a 1 s release gives a measurable tail.
	Release time.Duration
	// Presets is the number of General MIDI programs (bank 0, programs 0..Presets-1) that all play the same instrument;
	// 0 means 1. A score with several voices needs one preset per program it uses.
	Presets int
}

// Sample geometry: a 100-point sine period at 44000 Hz is exactly 440 Hz, so a note on the root key 69 (A4) is 440 Hz and
// the loop of whole periods is seamless.
const (
	sf2Rate       = 44000
	sf2Period     = 100
	sf2Points     = 16 * sf2Period
	sf2LoopStart  = 4 * sf2Period
	sf2Pad        = 46
	sf2RootKey    = 69
	sf2Amp        = 0.8
	sf2NoRelease  = -12000 // timecents, about 1 ms
	sf2MaxPresets = 128
)

// SF2 generator operators used below.
const (
	genInstrument    = 41
	genAttackVolEnv  = 34
	genReleaseVolEnv = 38
	genSampleID      = 53
	genSampleModes   = 54
)

// WriteSF2 writes a generated SoundFont 2 file: one instrument (a looped sine sample tuned to root key 69, 440 Hz) behind
// o.Presets presets. Nothing is committed; tests write it to t.TempDir() and tools/sf2gen writes it for e2e and dev.
func WriteSF2(path string, o SF2Options) error {
	b, err := BuildSF2(o)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644) // #nosec G306 -- a generated test soundfont, not a secret
}

// BuildSF2 returns the bytes WriteSF2 writes.
func BuildSF2(o SF2Options) ([]byte, error) {
	n := o.Presets
	if n == 0 {
		n = 1
	}
	if n < 0 || n > sf2MaxPresets {
		return nil, errors.New("dsptest: SF2Options.Presets must be 1..128")
	}
	if o.Release < 0 {
		return nil, errors.New("dsptest: SF2Options.Release must not be negative")
	}
	relTC := int16(sf2NoRelease)
	if o.Release > 0 {
		tc := math.Round(1200 * dsp.Log(o.Release.Seconds()) / math.Ln2)
		relTC = int16(math.Max(-12000, math.Min(8000, tc))) // #nosec G115 -- clamped to the int16 range above
	}

	le := binary.LittleEndian
	chunk := func(id string, data []byte) []byte {
		out := append([]byte(id), 0, 0, 0, 0)
		le.PutUint32(out[4:], uint32(len(data))) // #nosec G115 -- a few hundred KiB at most
		out = append(out, data...)
		if len(data)%2 == 1 {
			out = append(out, 0)
		}
		return out
	}
	list := func(kind string, parts ...[]byte) []byte {
		return chunk("LIST", append([]byte(kind), bytes.Join(parts, nil)...))
	}
	name20 := func(s string) []byte {
		b := make([]byte, 20)
		copy(b, s)
		return b
	}
	u16 := func(v ...uint16) []byte {
		b := make([]byte, 2*len(v))
		for i, x := range v {
			le.PutUint16(b[2*i:], x)
		}
		return b
	}
	u32 := func(v ...uint32) []byte {
		b := make([]byte, 4*len(v))
		for i, x := range v {
			le.PutUint32(b[4*i:], x)
		}
		return b
	}
	gen := func(op uint16, amount int16) []byte { return u16(op, uint16(amount)) } // #nosec G115 -- two's complement reinterpretation is the SF2 encoding

	info := list("INFO",
		chunk("ifil", u16(2, 1)),
		chunk("isng", []byte("EMU8000\x00")),
		chunk("INAM", []byte("metamusic test sine\x00")))

	smpl := make([]byte, 2*(sf2Points+sf2Pad))
	for i := 0; i < sf2Points; i++ {
		v := math.Round(sf2Amp * 32767 * math.Sin(2*math.Pi*float64(i)/sf2Period))
		le.PutUint16(smpl[2*i:], uint16(int16(v))) // #nosec G115 -- |v| <= 0.8*32767
	}
	sdta := list("sdta", chunk("smpl", smpl))

	var phdr, pbag, pgen []byte
	for p := 0; p < n; p++ {
		phdr = append(phdr, name20("Test Sine")...)
		phdr = append(phdr, u16(uint16(p), 0, uint16(p))...) // #nosec G115 -- p < 128 // preset, bank, bag index
		phdr = append(phdr, u32(0, 0, 0)...)
		pbag = append(pbag, u16(uint16(p), 0)...) // #nosec G115 -- p < 128
		pgen = append(pgen, gen(genInstrument, 0)...)
	}
	phdr = append(phdr, name20("EOP")...)
	phdr = append(phdr, u16(0, 0, uint16(n))...) // #nosec G115 -- n <= 128
	phdr = append(phdr, u32(0, 0, 0)...)
	pbag = append(pbag, u16(uint16(n), 0)...) // #nosec G115 -- n <= 128
	pgen = append(pgen, u16(0, 0)...)
	pmod := make([]byte, 10)

	inst := append(name20("Sine"), u16(0)...)
	inst = append(inst, name20("EOI")...)
	inst = append(inst, u16(1)...)
	igen := bytes.Join([][]byte{
		gen(genAttackVolEnv, sf2NoRelease),
		gen(genReleaseVolEnv, relTC),
		gen(genSampleModes, 1),
		gen(genSampleID, 0),
		u16(0, 0),
	}, nil)
	ibag := append(u16(0, 0), u16(4, 0)...)
	imod := make([]byte, 10)

	shdr := name20("Sine440")
	shdr = append(shdr, u32(0, sf2Points, sf2LoopStart, sf2Points, sf2Rate)...)
	shdr = append(shdr, sf2RootKey, 0)    // original pitch, pitch correction
	shdr = append(shdr, u16(0, 1)...)     // sample link, monoSample
	shdr = append(shdr, name20("EOS")...) // terminal record, 46 bytes
	shdr = append(shdr, make([]byte, 26)...)

	pdta := list("pdta",
		chunk("phdr", phdr), chunk("pbag", pbag), chunk("pmod", pmod), chunk("pgen", pgen),
		chunk("inst", inst), chunk("ibag", ibag), chunk("imod", imod), chunk("igen", igen), chunk("shdr", shdr))

	body := append([]byte("sfbk"), bytes.Join([][]byte{info, sdta, pdta}, nil)...)
	out := append([]byte("RIFF"), 0, 0, 0, 0)
	le.PutUint32(out[4:], uint32(len(body))) // #nosec G115 -- small
	return append(out, body...), nil
}
