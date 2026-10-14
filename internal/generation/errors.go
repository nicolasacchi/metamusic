package generation

import (
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Every service error is a *problem.Error: the API layer maps it onto the problem+json body unchanged.

func perr(status int, code, msg string, details map[string]any) error {
	return problem.E(status, code, msg, details)
}

// invalidItem is 422 INVALID_ITEM with details {item, field, rule} (C71.1).
func invalidItem(item int, field, rule string) error {
	return perr(http.StatusUnprocessableEntity, "INVALID_ITEM", "the item is not valid: "+rule,
		map[string]any{"item": item, "field": field, "rule": rule})
}

func disabledErr(reason string) error {
	return perr(http.StatusServiceUnavailable, "GENERATION_DISABLED", "generation is not enabled: "+reason, map[string]any{"reason": reason})
}

func staleErr(details map[string]any) error {
	return perr(http.StatusConflict, "ESTIMATE_STALE", "the estimate no longer matches the library; estimate again", details)
}

func notFound() error { return perr(http.StatusNotFound, "NOT_FOUND", "no such resource", nil) }

func unknownTrack(id string) error {
	return perr(http.StatusUnprocessableEntity, "UNKNOWN_TRACK", "no such track", map[string]any{"track_id": id})
}

func tooLarge(takes, items int) error {
	d := map[string]any{"takes": takes, "limit": MaxTakes}
	msg := "a batch holds at most 50 takes"
	if items > MaxItems {
		d = map[string]any{"items": items, "limit": MaxItems}
		msg = "an estimate holds at most 20 items"
	}
	return perr(http.StatusUnprocessableEntity, "BATCH_TOO_LARGE", msg, d)
}

// ready refuses a service built without its dependencies (the registry tests build empty services).
func (s *Service) ready() error {
	if s.o.Store == nil || s.o.Gate == nil || s.o.Spend == nil || s.o.Library == nil {
		return perr(http.StatusServiceUnavailable, "UNAVAILABLE", "generation is not available", map[string]any{"component": "generation"})
	}
	return nil
}
