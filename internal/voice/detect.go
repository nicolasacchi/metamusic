package voice

import "bytes"

// Container and codec names as stored.
const (
	ContainerWebM = "webm"
	ContainerOgg  = "ogg"
	ContainerMP4  = "mp4"

	CodecOpus   = "opus"
	CodecVorbis = "vorbis"
	CodecAAC    = "aac"
)

// headScan is how far into a WebM or Ogg file the codec marker is looked for (track headers come first).
const headScan = 64 << 10

// Detect names the container by magic number and the codec by its marker: WebM (EBML 1A 45 DF A3) with A_OPUS, Ogg with
// OpusHead or a vorbis identification header, MP4 (ftyp at offset 4) with mp4a (aac) or Opus. ok is false for anything else.
// The result only routes the upload to ffmpeg, which still has to decode it.
func Detect(b []byte) (container, codec string, ok bool) {
	head := b[:min(len(b), headScan)]
	switch {
	case bytes.HasPrefix(b, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		if bytes.Contains(head, []byte("A_OPUS")) {
			return ContainerWebM, CodecOpus, true
		}
	case bytes.HasPrefix(b, []byte("OggS")):
		switch {
		case bytes.Contains(head, []byte("OpusHead")):
			return ContainerOgg, CodecOpus, true
		case bytes.Contains(head, []byte("\x01vorbis")):
			return ContainerOgg, CodecVorbis, true
		}
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		switch {
		case bytes.Contains(b, []byte("mp4a")):
			return ContainerMP4, CodecAAC, true
		case bytes.Contains(b, []byte("Opus")):
			return ContainerMP4, CodecOpus, true
		}
	}
	return "", "", false
}
