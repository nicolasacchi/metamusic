package api

import (
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Constructors of the E2 problem errors (C149.2, C151, C158.5). Lanes build every E2 error through these so the code, status
// and details names stay one set; the *problem.Error they return passes genErr and m2bErr unchanged.

// ErrEngineCapability is 422 ENGINE_CAPABILITY {engine, mode?, capability}: the engine does not offer the mode or limit.
func ErrEngineCapability(engine, mode, capability string) error {
	d := map[string]any{"engine": engine, "capability": capability}
	if mode != "" {
		d["mode"] = mode
	}
	return problem.E(http.StatusUnprocessableEntity, CodeEngineCapability, "the engine does not offer this: "+capability, d)
}

// ErrEngineNotAdopted is 409 ENGINE_NOT_ADOPTED {engine, hint}.
func ErrEngineNotAdopted(engine, hint string) error {
	return problem.E(http.StatusConflict, CodeEngineNotAdopted, "the engine has not been adopted", map[string]any{"engine": engine, "hint": hint})
}

// ErrBackendUnavailable is 422 BACKEND_UNAVAILABLE {engine, backend}: no enabled backend serves the engine.
func ErrBackendUnavailable(engine, backend string) error {
	return problem.E(http.StatusUnprocessableEntity, CodeBackendUnavailable, "no enabled backend serves this engine", map[string]any{"engine": engine, "backend": backend})
}

// ErrEngineParamsStale is 422 ENGINE_PARAMS_STALE {engine_params_version, current}.
func ErrEngineParamsStale(version, current string) error {
	return problem.E(http.StatusUnprocessableEntity, CodeEngineParamsStale, "the engine parameters of the request are not the pinned version",
		map[string]any{"engine_params_version": version, "current": current})
}

// ErrConcurrencyExceedsCap is 422 CONCURRENCY_EXCEEDS_CAP {backends, g_micro_per_hour, x_micro} (D-E2-30).
func ErrConcurrencyExceedsCap(backends []string, gMicroPerHour, xMicro int64) error {
	return problem.E(http.StatusUnprocessableEntity, CodeConcurrencyExceedsCap, "the endpoints together could exceed the spend cap",
		map[string]any{"backends": backends, "g_micro_per_hour": gMicroPerHour, "x_micro": xMicro})
}

// ErrInvalidItem is 422 INVALID_ITEM {item, field, rule} (C71.1); rule is one of ItemRules or an engine issue code.
func ErrInvalidItem(item int, field, rule string) error {
	return problem.E(http.StatusUnprocessableEntity, CodeInvalidItem, "the item is not valid: "+rule, map[string]any{"item": item, "field": field, "rule": rule})
}

// ErrReferenceChanged is 409 ESTIMATE_STALE reason reference_changed: the clip digest differs at create (C149.3).
func ErrReferenceChanged(trackID string) error {
	return problem.E(http.StatusConflict, CodeEstimateStale, "the estimate no longer matches the library; estimate again",
		map[string]any{"reason": "reference_changed", "track_id": trackID})
}
