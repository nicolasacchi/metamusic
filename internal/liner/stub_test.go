package liner_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/liner"
)

// TestStubsV1_FrozenSignatures pins the signatures of C124.1; the stubs are empty. Lane V1 deletes stub_v1.go and this test and
// adds the goldens.
func TestStubsV1_FrozenSignatures(t *testing.T) {
	t.Parallel()
	l := liner.Build(liner.Input{Audience: liner.AudienceExport, Title: "Synthetic Record 001"})
	if l.Title != "Synthetic Record 001" || l.Markdown() != "" || l.Description() != "" {
		t.Errorf("stub output: %+v", l)
	}
	if liner.AudienceApp != "app" || liner.AudienceExport != "export" {
		t.Error("an audience name changed")
	}
}
