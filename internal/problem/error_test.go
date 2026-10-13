package problem

import (
	"errors"
	"testing"
)

func TestProblemError_Fields(t *testing.T) {
	t.Parallel()
	e := E(409, "DUEL_ALREADY_JUDGED", "already judged", map[string]any{"verdict_id": "v1"})
	if e.Status != 409 || e.Code != "DUEL_ALREADY_JUDGED" || e.Message != "already judged" || e.Details["verdict_id"] != "v1" {
		t.Fatalf("%+v", e)
	}
	if got := e.Error(); got != "DUEL_ALREADY_JUDGED: already judged" {
		t.Fatalf("Error() = %q", got)
	}
	var err error = e
	var pe *Error
	if !errors.As(err, &pe) || pe != e {
		t.Fatal("errors.As failed")
	}
	if E(404, "NOT_FOUND", "x", nil).Details != nil {
		t.Fatal("nil details must stay nil")
	}
}
