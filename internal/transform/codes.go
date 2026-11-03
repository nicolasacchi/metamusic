package transform

// Stable codes of the transform surface, frozen once by F1 so the generated openapi.json is regenerated once (C225.3).

// Warning codes. The estimate-time ones are computed from database rows only (no audio read); the render-time ones appear
// in TransformView.warnings and transform.json.
const (
	// estimate-time (C212.3)
	WarnTempoMeasuredLowConfidence = "tempo_measured_low_confidence"
	WarnTempoOctaveFolded          = "tempo_octave_folded"
	WarnKeyUnknownAssumedNone      = "key_unknown_assumed_none"
	WarnLargeTempoChange           = "large_tempo_change"
	WarnHeavyPitchShift            = "heavy_pitch_shift"
	WarnEnsembleFullMix            = "ensemble_full_mix"
	WarnBeatPhasePending           = "beat_phase_pending"
	WarnDownbeatAuto               = "downbeat_auto"
	WarnSectionUnavailable         = "section_unavailable"
	WarnStartDefaulted             = "start_defaulted"
	WarnSoundfontDefaultUsed       = "soundfont_default_used" // T1b

	// render-time (C212.3)
	WarnDownbeatAmbiguous        = "downbeat_ambiguous"
	WarnDownbeatOneDropSuspected = "downbeat_one_drop_suspected"
	WarnBeatPhaseWeak            = "beat_phase_weak"
	WarnTempoRefineRejected      = "tempo_refine_rejected"
	WarnDownbeatFromPlanAssumed  = "downbeat_from_plan_assumed"

	// hi-fi / 3D kinds (C223.3); the first five of binaural, plus after_binaural, are estimate-time
	WarnMasterHeavyCompression   = "master_heavy_compression"
	WarnMasterQuietBoost         = "master_quiet_boost"
	WarnMasterTargetLowered      = "master_target_lowered"
	WarnMasterInputClipped       = "master_input_clipped"
	WarnWidenAlreadyWide         = "widen_already_wide"
	WarnWidenOutOfPhaseInput     = "widen_out_of_phase_input"
	WarnWidenMonoSource          = "widen_mono_source"
	WarnWidenStrengthReduced     = "widen_strength_reduced"
	WarnWidenMonoFoldQuieter     = "widen_monofold_quieter"
	WarnBinauralHeadphonesOnly   = "binaural_headphones_only"
	WarnBinauralMonoIncompatible = "binaural_mono_incompatible"
	WarnBinauralParametricBasic  = "binaural_parametric_basic"
	WarnBinauralEarImbalance     = "binaural_ear_imbalance"
	WarnElevationIgnored         = "elevation_ignored"
	WarnAfterBinaural            = "after_binaural"
)

// Rule names of 422 INVALID_TRANSFORM {field, rule}.
const (
	RuleKindParamsMismatch = "kind_params_mismatch"
	RuleRequired           = "required"
	RuleRange              = "range"
	RuleSameInput          = "same_input"
	RuleSectionUnavailable = "section_unavailable"
	RuleLoopSourceRequired = "loop_source_required"
	RuleTooLong            = "too_long"
	RuleConvergeTooEarly   = "converge_too_early"
	RuleScoreTooLarge      = "score_too_large"
	RuleSoundfontUnknown   = "soundfont_unknown"
	RuleAlreadyBinaural    = "already_binaural" // the HRTF is never applied twice (C218.5)
	RulePresetUnknown      = "preset_unknown"
	RuleAzOrder            = "az_order" // left azimuth must be below the right one
)

// Error codes of the operations (problem+json) and of TransformView.error.
const (
	ErrInvalidTransform       = "INVALID_TRANSFORM"
	ErrAlignmentUnresolved    = "ALIGNMENT_UNRESOLVED"
	ErrScoreInvalid           = "SCORE_INVALID"
	ErrSoundfontUnknown       = "SOUNDFONT_UNKNOWN"
	ErrTransformTooLarge      = "TRANSFORM_TOO_LARGE"
	ErrTransformTakeNotMother = "TRANSFORM_TAKE_NOT_MOTHER"
	ErrQueueFull              = "TRANSFORM_QUEUE_FULL"
	ErrQuotaExceeded          = "TRANSFORM_QUOTA_EXCEEDED"
	ErrBudgetExceeded         = "TRANSFORM_BUDGET_EXCEEDED"
	ErrTimeout                = "TRANSFORM_TIMEOUT"
	ErrMemoryExceeded         = "TRANSFORM_MEMORY_EXCEEDED"
	ErrDSPNonFinite           = "DSP_NONFINITE"
	ErrDSPSilentOutput        = "DSP_SILENT_OUTPUT"
	ErrLoudnessUnsafe         = "LOUDNESS_UNSAFE"
	ErrSynthFailed            = "SYNTH_FAILED"
	ErrInputChanged           = "INPUT_CHANGED"
	ErrIngestFailedPrefix     = "INGEST_FAILED:"
	ErrInterrupted            = "INTERRUPTED"
	ErrCancelledByUser        = "CANCELLED_BY_USER"
	ErrTransformFailed        = "TRANSFORM_FAILED"
)

// Transform states and phases (the CHECK lists of the migration).
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)
