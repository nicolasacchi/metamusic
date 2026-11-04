package judgments

// VoiceRef is the spoken note of a comment (C259): only the 64 kbps MP3 rendition is ever linked (url). It sits in the
// output schema of first-class MCP tools, so it carries no docs and no enums (tools/list budget; container is webm, ogg or mp4
// and codec is opus, vorbis or aac).
type VoiceRef struct {
	ID        string  `json:"id"`
	DurationS float64 `json:"duration_s"`
	URL       string  `json:"url"`
	Container string  `json:"container"`
	Codec     string  `json:"codec"`
	Bytes     int64   `json:"bytes"`
}
