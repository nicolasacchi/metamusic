package duels_test

import (
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels"
)

func TestSourceFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		p    auth.Principal
		want string
		err  error
	}{
		{"operator", auth.Principal{Kind: auth.KindHuman, ID: "op"}, "human", nil},
		{"paired device", auth.Principal{Kind: auth.KindDevice, ID: "d1"}, "human", nil},
		{"PAT agent", auth.Principal{Kind: auth.KindAgent, ID: "t1"}, "critic", nil},
		{"system", auth.CLI, "", duels.ErrNoSource},
		{"anonymous", auth.Principal{Kind: auth.KindAnonymous}, "", duels.ErrNoSource},
		{"zero", auth.Principal{}, "", duels.ErrNoSource},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := duels.SourceFor(tc.p)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Errorf("SourceFor = %q, %v; want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}
