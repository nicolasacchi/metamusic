// Package redact is the only path for HTTP-client errors into logs or stored rows.
package redact

import (
	"regexp"
	"strings"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var (
	credRE  = regexp.MustCompile(`\b(mm[pdcs])_[A-Za-z0-9]{8,}`)
	bearRE  = regexp.MustCompile(`(?i)\b(Bearer)\s+[^\s,;"']+`)
	authRE  = regexp.MustCompile(`(?i)\b(Authorization):\s*[^\r\n]*`)
	queryRE = regexp.MustCompile(`\?[^\s"'<>]*`)
	jwtRE   = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	// form-encoded secrets (Subsonic t/s/p/apiKey, OAuth code and token parameters)
	formRE = regexp.MustCompile(`(^|[\s&?;,])(t|s|p|apiKey|code|code_verifier|refresh_token|access_token|client_secret)=[^&\s"']*`)
	runRE  = regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)

	// M3 additions (C80): cookie headers and the RunPod endpoint id inside job-API paths.
	cookieRE = regexp.MustCompile(`(?i)\b(Cookie|Set-Cookie):\s*[^\r\n]*`)
	// /v2/<id>/run|status|... (any host or none) and the bare job-API form api.runpod.ai/v2/<id>.
	jobPathRE  = regexp.MustCompile(`(/v2/)[A-Za-z0-9_-]+(/(?:runsync|run|status|stream|cancel|retry|health|purge-queue)\b)`)
	jobHostRE  = regexp.MustCompile(`(api\.runpod\.ai/v2/)[A-Za-z0-9_-]+`)
	mgmtPathRE = regexp.MustCompile(`(/v2/endpoints/)[A-Za-z0-9_-]+`)
)

const endpointPlaceholder = "<endpoint>"

// String masks credentials, Authorization headers, URL query strings and long
// opaque runs (>= 32 hex or base64url characters).
func String(s string) string {
	s = authRE.ReplaceAllString(s, "$1: <redacted>")
	s = cookieRE.ReplaceAllString(s, "$1: <redacted>")
	s = jobPathRE.ReplaceAllString(s, "${1}"+endpointPlaceholder+"$2")
	s = jobHostRE.ReplaceAllString(s, "${1}"+endpointPlaceholder)
	s = mgmtPathRE.ReplaceAllString(s, "${1}"+endpointPlaceholder)
	s = bearRE.ReplaceAllString(s, "$1 <redacted>")
	s = jwtRE.ReplaceAllString(s, "<jwt>")
	s = formRE.ReplaceAllString(s, "${1}${2}=<redacted>")
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

// Redactor is String plus literal replacement of values the process knows are private: the configured
// RunPod endpoint id wherever it appears, not only in /v2/<id>/ paths (M3 C77.5). It is a value, not a
// package global: each provider client builds its own.
type Redactor struct{ literals []string }

// minLiteral keeps a degenerate value (empty, one character) from rewriting ordinary text.
const minLiteral = 4

// WithEndpoint returns a Redactor that also replaces endpointID literally with "<endpoint>". An id
// shorter than four characters is ignored.
func WithEndpoint(endpointID string) Redactor {
	if len(endpointID) < minLiteral {
		return Redactor{}
	}
	return Redactor{literals: []string{endpointID}}
}

// String applies the literal replacements first (so a longer match is not split by the generic rules),
// then the package rules.
func (r Redactor) String(s string) string {
	for _, l := range r.literals {
		s = strings.ReplaceAll(s, l, endpointPlaceholder)
	}
	return String(s)
}

// Error returns r.String(err.Error()), "" for nil.
func (r Redactor) Error(err error) string {
	if err == nil {
		return ""
	}
	return r.String(err.Error())
}
