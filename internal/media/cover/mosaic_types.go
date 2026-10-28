package cover

// MosaicVersion identifies the Record and album mosaic renderer (C123). M4a's Version ("mm-cover/1") keeps naming the
// track and lineage covers; the two key spaces never meet.
const MosaicVersion = "mm-cover/record-1"

// Part is one tile of a mosaic: a Song's lineage, never its current Take, so a Pick change cannot change a cover.
type Part struct {
	RootID string // the Song's primary root id (a takes-less draft Song contributes its draft id)
	Seed   int64  // that root's recipe seed (0 for a draft)
}

// Input is everything a mosaic is drawn from. Kind is "record" or "album"; ID is the Record or album id (it drives the
// palette); Seed drives the layout (a Record's cover_seed, 0 for an album); Parts are the first CoverParts Songs in order.
type Input struct {
	Kind  string
	ID    string
	Seed  int64
	Parts []Part
}

// The two kinds of mosaic Input.
const (
	KindRecord = "record"
	KindAlbum  = "album"
)
