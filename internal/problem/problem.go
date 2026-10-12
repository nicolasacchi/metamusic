// Package problem is the one writer of application/problem+json bodies outside Huma.
// It is a leaf package: it imports nothing from this module.
package problem

import (
	"encoding/json"
	"net/http"
)

// Body is the single error body shape {"code","message","details"}.
type Body struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// Write sends a problem+json response. A nil details map is written as {}.
func Write(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Body{Code: code, Message: msg, Details: details})
}
