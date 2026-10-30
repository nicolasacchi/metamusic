package records

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func TestCov_Actor(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 200)
	for _, tc := range []struct {
		name     string
		p        auth.Principal
		wantKind string
		wantName string
	}{
		{"human", auth.Principal{Kind: auth.KindHuman, ID: "i", Name: "n"}, "human", "n"},
		{"device falls back to id", auth.Principal{Kind: auth.KindDevice, ID: "dev"}, "human", "dev"},
		{"agent", auth.Principal{Kind: auth.KindAgent, Name: "a"}, "agent", "a"},
		{"anonymous falls back to kind", auth.Principal{Kind: auth.KindAnonymous}, "system", "anonymous"},
		{"nothing at all", auth.Principal{}, "system", "unknown"},
		{"long name is cut by runes", auth.Principal{Kind: auth.KindHuman, Name: long}, "human", strings.Repeat("é", 120)},
	} {
		if got := actorKind(tc.p); got != tc.wantKind {
			t.Errorf("%s: kind = %q, want %q", tc.name, got, tc.wantKind)
		}
		if got := actorName(tc.p); got != tc.wantName {
			t.Errorf("%s: name = %q, want %q", tc.name, got, tc.wantName)
		}
	}
}

type coded struct{ c int }

func (e coded) Error() string { return fmt.Sprintf("code %d", e.c) }
func (e coded) Code() int     { return e.c }

func TestCov_IsBusy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"busy", coded{5}, true},
		{"locked", coded{6}, true},
		{"extended busy code", coded{5 | 2<<8}, true},
		{"wrapped", fmt.Errorf("x: %w", coded{6}), true},
		{"other code", coded{19}, false},
		{"text busy", errors.New("SQLITE_BUSY: nope"), true},
		{"text locked", errors.New("database is locked"), true},
		{"plain", errors.New("boom"), false},
	} {
		if got := isBusy(tc.err); got != tc.want {
			t.Errorf("%s: isBusy = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCov_TakeHelpers(t *testing.T) {
	t.Parallel()
	a, a2, b := "a", "a", "b"
	for _, tc := range []struct {
		x, y *string
		want bool
	}{{nil, nil, true}, {&a, nil, false}, {nil, &a, false}, {&a, &a2, true}, {&a, &b, false}} {
		if got := sameTake(tc.x, tc.y); got != tc.want {
			t.Errorf("sameTake(%v, %v) = %v", tc.x, tc.y, got)
		}
	}
	if takeStr(nil) != "no take" || takeStr(&b) != "b" {
		t.Error("takeStr")
	}
}
