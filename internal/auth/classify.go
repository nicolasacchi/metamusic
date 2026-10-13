package auth

import (
	"slices"
	"strings"
)

type PathClass int

const (
	ClassUI PathClass = iota
	ClassMachine
	ClassPublic
)

// Classify maps a request path to its auth class. Prefix entries match the
// directory itself or anything beneath it, never near-misses like /mcpx.
func Classify(path string) PathClass {
	// Dot-segment paths are never public or machine: handlers must not see
	// them anonymously.
	if slices.Contains(strings.Split(path, "/"), "..") {
		return ClassUI
	}
	switch path {
	case "/healthz", "/manifest.webmanifest", "/auth/login", "/auth/logout",
		"/start", "/pair", "/pair.js", "/auth/pair":
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
