package api

// Schema names of the Composer's Desk (M5b C359), registered per file (G1 D-G1-12). The score toolkit's names (Report, Op, Violation,
// Warning) are too generic for the one OpenAPI namespace; the desk shapes keep the Plan prefix.
func init() {
	addWireNames(map[string]string{
		"internal/desk.Page":               "PlanPage",
		"internal/desk.Plan":               "Plan",
		"internal/desk.PlanSummary":        "PlanSummary",
		"internal/desk.PlanBase":           "PlanBase",
		"internal/desk.PlanScore":          "PlanScore",
		"internal/desk.PlanVoice":          "PlanVoice",
		"internal/desk.PlanSection":        "PlanSection",
		"internal/desk.BarSpan":            "PlanBarSpan",
		"internal/desk.Display":            "PlanDisplay",
		"internal/desk.EditInfo":           "PlanEdit",
		"internal/desk.Child":              "PlanChild",
		"internal/desk.Verdicts":           "PlanVerdicts",
		"internal/desk.OwnerVerdict":       "PlanOwnerVerdict",
		"internal/desk.CriticVerdict":      "PlanCriticVerdict",
		"internal/desk.AuditionRef":        "PlanAudition",
		"internal/desk.RenderRef":          "PlanRender",
		"internal/score/abc.Op":            "ScoreEditOp",
		"internal/score/abc.Report":        "ScoreReport",
		"internal/score/abc.ReportError":   "ScoreReportError",
		"internal/score/abc.ReportHeader":  "ScoreReportHeader",
		"internal/score/abc.ReportVoice":   "ScoreReportVoice",
		"internal/score/abc.ReportSection": "ScoreReportSection",
		"internal/score/abc.Violation":     "ScoreViolation",
		"internal/score.Warning":           "ScoreWarning",
	})
}
