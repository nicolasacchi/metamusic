// Package redact is the only path for HTTP-client errors into logs or stored rows.
package redact

import "regexp"

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var (
	credRE  = regexp.MustCompile(`\b(mm[pdc])_[A-Za-z0-9]{8,}`)
	bearRE  = regexp.MustCompile(`(?i)\b(Bearer)\s+[^\s,;"']+`)
	authRE  = regexp.MustCompile(`(?i)\b(Authorization):\s*[^\r\n]*`)
	queryRE = regexp.MustCompile(`\?[^\s"'<>]*`)
	runRE   = regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)
)

// String masks credentials, Authorization headers, URL query strings and long
// opaque runs (>= 32 hex or base64url characters).
func String(s string) string {
	s = authRE.ReplaceAllString(s, "$1: <redacted>")
	s = bearRE.ReplaceAllString(s, "$1 <redacted>")
	s = credRE.ReplaceAllString(s, "${1}_<redacted>")
	s = queryRE.ReplaceAllString(s, "?<redacted>")
	s = runRE.ReplaceAllStringFunc(s, func(m string) string {
		if uuidRE.MatchString(m) {
			return m
		}
		return "<redacted>"
	})
	return s
}

// Error returns String(err.Error()), "" for nil.
func Error(err error) string {
	if err == nil {
		return ""
	}
	return String(err.Error())
}
