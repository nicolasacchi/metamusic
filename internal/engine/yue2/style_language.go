package yue2

// StyleLanguage returns the ISO 639-1 code of the language a style string names ("italian" -> "it"), or "" when it names
// none. It is the tag the songwriting lint compares with the detected lyrics language.
func StyleLanguage(style string) string { return styleLanguage(style) }
