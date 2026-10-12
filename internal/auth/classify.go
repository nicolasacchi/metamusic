package auth

import "strings"

type PathClass int

const (
	ClassUI PathClass = iota
	ClassMachine
	ClassPublic
)

// Classify maps a request path to its auth class. Prefix entries match the
// directory itself or anything beneath it, never near-misses like /mcpx.
func Classify(path string) PathClass {
	switch path {
	case "/healthz", "/manifest.webmanifest", "/auth/login", "/auth/logout":
		return ClassPublic
	case "/oauth/token", "/oauth/register", "/oauth/revoke", "/mcp":
		return ClassMachine
	}
	for _, p := range []string{"/icons/", "/assets/"} {
		if strings.HasPrefix(path, p) {
			return ClassPublic
		}
	}
	for _, p := range []string{"/mcp/", "/rest/", "/media/", "/worker/", "/.well-known/"} {
		if strings.HasPrefix(path, p) {
			return ClassMachine
		}
	}
	return ClassUI
}
