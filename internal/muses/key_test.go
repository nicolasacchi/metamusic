package muses_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/textfold"
)

func ptr(s string) *string { return &s }

func TestKey_Golden(t *testing.T) {
	t.Parallel()
	if a, b := muses.Key("Mixed CASE  title", ptr("Ärtist")), muses.Key("mixed case title", ptr("artist")); a != b {
		t.Errorf("keys differ: %q vs %q", a, b)
	}
	if a, b := muses.Key("T", nil), muses.Key("T", ptr("")); a != b {
		t.Errorf("nil artist must equal empty artist")
	}
	if muses.Key("T", nil) == muses.Key("T", ptr("A")) {
		t.Errorf("no artist must differ from an artist")
	}
	if want := textfold.Norm("x") + "\x1f" + textfold.Norm("y"); muses.Key("X", ptr("Y")) != want {
		t.Errorf("key shape")
	}
	// golden: sha256 of three keys (pins the separator and the normaliser)
	for _, tc := range []struct {
		title  string
		artist *string
		want   string
	}{
		{"Synthetic Muse 001", ptr("Synthetic Artist 001"), "baa7c82c00bdbeb0ef1c4aab84f51021233538d354e39dd93e5cfe5ca3520520"},
		{"Synthetic Muse 002", nil, "a1ba94905670748a1f0fd0d28cc907be42224965851b074786ae4aab5a15a2bd"},
		{"  Ünïcode   Title ", ptr("Åsa"), "009a25e7767c55e25c83c01d1dfbf83f33fbe2d5e98693cf07c0fee035bcda15"},
	} {
		h := sha256.Sum256([]byte(muses.Key(tc.title, tc.artist)))
		got := hex.EncodeToString(h[:])
		if got != tc.want {
			t.Errorf("golden %q: %s", tc.title, got)
		}
	}
}
