// Package voice holds walkie-talkie voice notes (C259, D-M5-23): validation, content-addressed storage, the MP3 playback
// rendition and the media resolver. M5-F2 froze the service type; the optional lane M5-V1 fills it.
package voice

// Options builds the service (store, data root, ffmpeg, judgments, clock, ids); lane M5-V1 fills it.
type Options struct{}

// Service owns the voice_notes table.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service { return &Service{o: o} }
