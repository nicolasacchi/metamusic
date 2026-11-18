package curation_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/curation"
)

// TestSettings_EditorAndListenerRefused: humans of the Editor and Listener roles are not the Owner (U1 C302.6).
func TestSettings_EditorAndListenerRefused(t *testing.T) {
	t.Parallel()
	s := curation.New(open(t), time.Now)
	for _, role := range []auth.Role{auth.RoleEditor, auth.RoleListener} {
		p := auth.Principal{Kind: auth.KindHuman, Role: role, ID: "bob", Scopes: auth.ScopesFor(role)}
		_, err := s.Set(context.Background(), p, curation.SettingsPatch{ExportMuseNames: ptr(true)})
		if pe := problemOf(t, err); pe.Status != 403 || pe.Code != "HUMAN_ONLY" {
			t.Errorf("%s: %+v", role, pe)
		}
	}
}
