package drafts

import (
	"context"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// Refusals for reference tracks (C62, D-M2c-6). A reference track's stub recipe owns a request id and
// has no style or lyrics, so a draft that reuses the id or descends from the track is refused.

const codeRecipeUnknown = "recipe_unknown"

func recipeUnknownError(field, msg string) error {
	return &Error{Status: 422, Code: "RECIPE_UNKNOWN", Msg: msg, Details: map[string]any{"field": field}}
}

// unknownIDs returns the subset of requestIDs owned by a stub recipe (engine 'unknown'), in one query.
func (s *Service) unknownIDs(ctx context.Context, requestIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(requestIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(requestIDs))
	for i, id := range requestIDs {
		args[i] = id
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT request_id FROM recipes WHERE engine = 'unknown' AND request_id IN (`+placeholders(len(args))+`)`, args...) //nolint:gosec // G202: only "?" placeholders are concatenated
	if err != nil {
		return nil, fmt.Errorf("drafts: unknown recipes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// rejectUnknownID refuses a request id that belongs to a reference track.
func (s *Service) rejectUnknownID(ctx context.Context, requestID string) error {
	set, err := s.unknownIDs(ctx, []string{requestID})
	if err != nil {
		return err
	}
	if set[requestID] {
		return recipeUnknownError("id", "this id belongs to a reference track whose recipe is unknown")
	}
	return nil
}

// recipeUnknownIssue is the lint issue of an item whose id belongs to a reference track.
func recipeUnknownIssue() yue2.Issue {
	return yue2.Issue{Code: codeRecipeUnknown, Field: "id", Severity: "error",
		Message: "this id belongs to a reference track whose recipe is unknown"}
}

// flagUnknown adds the recipe_unknown error to every item of rep whose request id is a stub's.
func (s *Service) flagUnknown(ctx context.Context, rep *yue2.LintReport) error {
	ids := make([]string, 0, len(rep.Items))
	for _, it := range rep.Items {
		if it.RequestID != "" {
			ids = append(ids, it.RequestID)
		}
	}
	set, err := s.unknownIDs(ctx, ids)
	if err != nil || len(set) == 0 {
		return err
	}
	for i := range rep.Items {
		if set[rep.Items[i].RequestID] {
			rep.Items[i].Errors = append(rep.Items[i].Errors, recipeUnknownIssue())
			rep.OK = false
		}
	}
	return nil
}
