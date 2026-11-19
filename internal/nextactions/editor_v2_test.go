package nextactions

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// TestCandidateRulesEditorHidesSpendAndCallback pins C305: an Editor gets no spend or callback item, the Owner side all.
func TestCandidateRulesEditorHidesSpendAndCallback(t *testing.T) {
	t.Parallel()
	owner := auth.Principal{Kind: auth.KindHuman, ID: "alice", Role: auth.RoleOwner, Scopes: append(auth.ScopesFor(auth.RoleOwner), auth.ScopeGenerate)}
	editor := auth.Principal{Kind: auth.KindHuman, ID: "bob", Role: auth.RoleEditor, Scopes: append(auth.ScopesFor(auth.RoleEditor), auth.ScopeGenerate)}
	has := func(rs []*Rule, code string) bool {
		for _, r := range rs {
			if r.Code == code {
				return true
			}
		}
		return false
	}
	for code, aud := range map[string]Audience{"callback_waiting": AudienceHuman, "patch_ready_to_render": AudienceAgent} {
		if !editorHidden[code] {
			t.Errorf("%s is not hidden", code)
		}
		if !has(candidateRules(owner, aud, 0, nil), code) {
			t.Errorf("owner lacks %s", code)
		}
		if has(candidateRules(editor, aud, 0, nil), code) {
			t.Errorf("editor gets %s", code)
		}
	}
	e := candidateRules(editor, "", 0, nil)
	if !has(e, "record_undecided_slots") {
		t.Errorf("editor lost an ordinary rule")
	}
}
