package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestEstimateInput_NoMandateField (C368.4): EstimateIn.MandateID is Go-only. The estimate's request and response schemas, and so the
// MCP tool schema derived from them, carry no mandate field: an agent cannot name a mandate through the API.
func TestEstimateInput_NoMandateField(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"EstimateGenerationInBody", "Estimate", "EstimateItem", "CreateGenerationInBody"} {
		s, ok := doc.Components.Schemas[name]
		if !ok {
			if name == "EstimateGenerationInBody" || name == "Estimate" {
				t.Fatalf("schema %s missing", name)
			}
			continue
		}
		if strings.Contains(strings.ToLower(string(s)), "mandate") {
			t.Errorf("schema %s mentions a mandate", name)
		}
	}
}
