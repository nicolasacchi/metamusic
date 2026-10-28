package flacmeta

// Comment is one Vorbis comment (C128.3). Key is ASCII 0x20-0x7D without '='; Value is UTF-8. The tape lane (T1) builds
// the VORBIS_COMMENT, CUESHEET and PICTURE writers around it; this file is the frozen type the tape snapshot already
// carries.
type Comment struct{ Key, Value string }
