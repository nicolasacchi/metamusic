package flacmeta

import "encoding/binary"

// Picture is a PICTURE block (type 6): Type is the APIC picture type (3 = front cover), Depth the bits per pixel, Colors
// the palette size (0 for non-indexed images).
type Picture struct {
	Type       uint32
	MIME, Desc string
	W, H       uint32
	Depth      uint32
	Colors     uint32
	Data       []byte
}

// PictureBlock builds the payload of a PICTURE block. The caller checks the result against MaxBlockLen.
func PictureBlock(p Picture) []byte {
	b := make([]byte, 0, 32+len(p.MIME)+len(p.Desc)+len(p.Data))
	b = binary.BigEndian.AppendUint32(b, p.Type)
	b = binary.BigEndian.AppendUint32(b, u32(len(p.MIME)))
	b = append(b, p.MIME...)
	b = binary.BigEndian.AppendUint32(b, u32(len(p.Desc)))
	b = append(b, p.Desc...)
	b = binary.BigEndian.AppendUint32(b, p.W)
	b = binary.BigEndian.AppendUint32(b, p.H)
	b = binary.BigEndian.AppendUint32(b, p.Depth)
	b = binary.BigEndian.AppendUint32(b, p.Colors)
	b = binary.BigEndian.AppendUint32(b, u32(len(p.Data)))
	return append(b, p.Data...)
}
