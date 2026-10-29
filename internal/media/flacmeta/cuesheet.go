package flacmeta

import (
	"encoding/binary"
	"fmt"
	"io"
)

// LeadOutTrack is the track number of the lead-out entry of a CUESHEET block.
const LeadOutTrack = 255

// CueIndex is one index point of a track; Offset is in samples, relative to the track's offset.
type CueIndex struct {
	Offset uint64
	Number uint8
}

// CueTrack is an audio track of a CUESHEET block (no ISRC, no pre-emphasis); Offset is in samples from the first sample of
// the stream.
type CueTrack struct {
	Offset  uint64
	Number  uint8
	Indexes []CueIndex
}

// Cuesheet is the content of a CUESHEET block: empty media catalog number, lead-in 0, is_cd 0, and a lead-out whose offset
// is the total number of samples.
type Cuesheet struct {
	Tracks  []CueTrack
	LeadOut uint64
}

const (
	cueHeaderLen = 128 + 8 + 1 + 258 + 1
	cueTrackLen  = 8 + 1 + 12 + 1 + 13 + 1
	cueIndexLen  = 8 + 1 + 3
	// MaxCueTracks is the most tracks a CUESHEET block can hold (the track count is one byte and 255 is the lead-out).
	MaxCueTracks = 254
)

// CuesheetBlock builds the payload of a CUESHEET block (type 5). The caller validates the sheet (ValidateCuesheet).
func CuesheetBlock(c Cuesheet) []byte {
	b := make([]byte, cueHeaderLen, cueHeaderLen+(len(c.Tracks)+1)*cueTrackLen)
	// media catalog number (128 bytes), lead-in (8 bytes), is_cd bit and reserved: all zero
	b[cueHeaderLen-1] = byte(len(c.Tracks) + 1) //nolint:gosec // at most MaxCueTracks+1 (validated by the caller)
	for _, t := range c.Tracks {
		b = appendCueTrack(b, t.Offset, t.Number, t.Indexes)
	}
	return appendCueTrack(b, c.LeadOut, LeadOutTrack, nil)
}

func appendCueTrack(b []byte, off uint64, num uint8, idx []CueIndex) []byte {
	b = binary.BigEndian.AppendUint64(b, off)
	b = append(b, num)
	b = append(b, make([]byte, 12+1+13)...) // ISRC, type/pre-emphasis bits, reserved
	b = append(b, byte(len(idx)))           //nolint:gosec // at most 255 (validated by the caller)
	for _, i := range idx {
		b = binary.BigEndian.AppendUint64(b, i.Offset)
		b = append(b, i.Number, 0, 0, 0)
	}
	return b
}

// ValidateCuesheet checks what a reader may rely on: 1 to MaxCueTracks tracks numbered 1 to 254 in strictly increasing
// number and non-decreasing offset, each with at least one index (at most 255), all offsets at or before the lead-out.
func ValidateCuesheet(c Cuesheet) error {
	if len(c.Tracks) == 0 || len(c.Tracks) > MaxCueTracks {
		return fmt.Errorf("flacmeta: cuesheet has %d tracks", len(c.Tracks))
	}
	prevNum, prevOff := 0, uint64(0)
	for i, t := range c.Tracks {
		if t.Number == 0 || t.Number == LeadOutTrack || int(t.Number) <= prevNum {
			return fmt.Errorf("flacmeta: cuesheet track %d: bad number", i)
		}
		if t.Offset < prevOff || t.Offset > c.LeadOut {
			return fmt.Errorf("flacmeta: cuesheet track %d: bad offset", i)
		}
		if len(t.Indexes) == 0 || len(t.Indexes) > 255 {
			return fmt.Errorf("flacmeta: cuesheet track %d: %d indexes", i, len(t.Indexes))
		}
		prevNum, prevOff = int(t.Number), t.Offset
	}
	return nil
}

// ReadCuesheet parses the first CUESHEET block of the file described by info. ok is false when there is none. The lead-out
// entry becomes Cuesheet.LeadOut.
func ReadCuesheet(r io.ReaderAt, size int64, info Info) (c Cuesheet, ok bool, err error) {
	for _, bl := range info.Blocks {
		if bl.Type != BlockCueSheet {
			continue
		}
		b, err := readAt(r, size, bl.Offset+4, bl.Length)
		if err != nil {
			return Cuesheet{}, false, wrapEOF(err)
		}
		c, err = parseCuesheet(b)
		return c, err == nil, err
	}
	return Cuesheet{}, false, nil
}

func parseCuesheet(b []byte) (Cuesheet, error) {
	bad := func(what string) error { return fmt.Errorf("%w: CUESHEET %s", ErrCorrupt, what) }
	if len(b) < cueHeaderLen {
		return Cuesheet{}, bad("too short")
	}
	n := int(b[cueHeaderLen-1])
	p := cueHeaderLen
	var c Cuesheet
	sawLeadOut := false
	for i := 0; i < n; i++ {
		if len(b)-p < cueTrackLen {
			return Cuesheet{}, bad("truncated track")
		}
		off := binary.BigEndian.Uint64(b[p:])
		num := b[p+8]
		ni := int(b[p+cueTrackLen-1])
		p += cueTrackLen
		if len(b)-p < ni*cueIndexLen {
			return Cuesheet{}, bad("truncated indexes")
		}
		idx := make([]CueIndex, ni)
		for j := range idx {
			idx[j] = CueIndex{Offset: binary.BigEndian.Uint64(b[p:]), Number: b[p+8]}
			p += cueIndexLen
		}
		if num == LeadOutTrack {
			if sawLeadOut || i != n-1 {
				return Cuesheet{}, bad("lead-out position")
			}
			sawLeadOut, c.LeadOut = true, off
			continue
		}
		c.Tracks = append(c.Tracks, CueTrack{Offset: off, Number: num, Indexes: idx})
	}
	if !sawLeadOut {
		return Cuesheet{}, bad("no lead-out")
	}
	return c, nil
}

// ReadPictures parses every PICTURE block of the file described by info.
func ReadPictures(r io.ReaderAt, size int64, info Info) ([]Picture, error) {
	var out []Picture
	for _, bl := range info.Blocks {
		if bl.Type != BlockPicture {
			continue
		}
		b, err := readAt(r, size, bl.Offset+4, bl.Length)
		if err != nil {
			return nil, wrapEOF(err)
		}
		p, err := parsePicture(b)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func parsePicture(b []byte) (Picture, error) {
	bad := func(what string) error { return fmt.Errorf("%w: PICTURE %s", ErrCorrupt, what) }
	var p Picture
	pos := 0
	u32 := func() (uint32, bool) {
		if len(b)-pos < 4 {
			return 0, false
		}
		v := binary.BigEndian.Uint32(b[pos:])
		pos += 4
		return v, true
	}
	str := func() (string, bool) {
		n, ok := u32()
		if !ok || int64(n) > int64(len(b)-pos) {
			return "", false
		}
		s := string(b[pos : pos+int(n)])
		pos += int(n)
		return s, true
	}
	var ok bool
	if p.Type, ok = u32(); !ok {
		return p, bad("too short")
	}
	if p.MIME, ok = str(); !ok {
		return p, bad("MIME")
	}
	if p.Desc, ok = str(); !ok {
		return p, bad("description")
	}
	for _, f := range []*uint32{&p.W, &p.H, &p.Depth, &p.Colors} {
		if *f, ok = u32(); !ok {
			return p, bad("dimensions")
		}
	}
	n, ok := u32()
	if !ok || int64(n) > int64(len(b)-pos) {
		return p, bad("data length")
	}
	p.Data = append([]byte(nil), b[pos:pos+int(n)]...)
	return p, nil
}
