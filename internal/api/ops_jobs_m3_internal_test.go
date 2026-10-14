package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestJobPages_CutAtTheByteCap: a page never exceeds 90 000 bytes; the cut returns the cursor of the last
// kept item so the next page continues right after it (M1 C17).
func TestJobPages_CutAtTheByteCap(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	var jobs []JobSummary
	var batches []BatchSummary
	for i := range 100 {
		ts := store.FormatTime(at.Add(-time.Duration(i) * time.Minute))
		jobs = append(jobs, JobSummary{ID: fmt.Sprintf("job-%03d", i), CreatedAt: ts, Title: strings.Repeat("t", 3000)})
		batches = append(batches, BatchSummary{ID: fmt.Sprintf("bat-%03d", i), CreatedAt: ts, Rationale: ptr(strings.Repeat("r", 3000))})
	}
	kept, next := capJobPage(jobs, nil)
	if raw, _ := json.Marshal(kept); len(raw) > maxPageBytesJobs || len(kept) == 0 || len(kept) >= 100 || next == nil {
		t.Fatalf("jobs: %d kept, %v", len(kept), next)
	}
	if want := fmt.Sprintf("job-%03d", len(kept)-1); kept[len(kept)-1].ID != want {
		t.Errorf("last kept %s", kept[len(kept)-1].ID)
	}
	bk, bnext := capBatchPage(batches, nil)
	if raw, _ := json.Marshal(bk); len(raw) > maxPageBytesJobs || len(bk) == 0 || len(bk) >= 100 || bnext == nil {
		t.Fatalf("batches: %d kept, %v", len(bk), bnext)
	}
	// a page under the cap is returned as it is, with the service's own cursor
	small, keep := capJobPage(jobs[:3], ptr("c-1"))
	if len(small) != 3 || keep == nil || *keep != "c-1" {
		t.Errorf("small page: %d %v", len(small), keep)
	}
	one, _ := capJobPage([]JobSummary{{ID: "huge", Title: strings.Repeat("x", 200000)}}, nil)
	if len(one) != 1 {
		t.Error("a single oversize item is still returned (progress is guaranteed)")
	}
	if nextCursor("") != nil || *nextCursor("x") != "x" {
		t.Error("nextCursor")
	}
	if kind, id := keyKind("agent:tok"); kind != "agent" || id != "tok" {
		t.Errorf("keyKind %q %q", kind, id)
	}
}

func ptr[T any](v T) *T { return &v }
