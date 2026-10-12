package problem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestProblem_Write(t *testing.T) {
	t.Run("shape and nil details", func(t *testing.T) {
		w := httptest.NewRecorder()
		Write(w, http.StatusConflict, "CONFLICT", "nope", nil)
		if w.Code != 409 {
			t.Fatalf("status %d", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Fatalf("content type %q", ct)
		}
		if !strings.HasSuffix(w.Body.String(), "\n") {
			t.Fatal("no trailing newline")
		}
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"code": "CONFLICT", "message": "nope", "details": map[string]any{}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatal(diff)
		}
	})
	t.Run("hostile message stays valid JSON", func(t *testing.T) {
		msg := "say \"hi\"\nnew\\line\t</script>   \xff"
		w := httptest.NewRecorder()
		Write(w, 400, `BAD"CODE`, msg, map[string]any{"k": "v\"\n"})
		var got Body
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, w.Body.String())
		}
		if got.Code != `BAD"CODE` || got.Details["k"] != "v\"\n" {
			t.Fatalf("round trip: %+v", got)
		}
	})
}
