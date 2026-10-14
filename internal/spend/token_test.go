package spend_test

import (
	"math/rand/v2"
	"regexp"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/spend"
)

var tokenRE = regexp.MustCompile(`^mmc_[a-z2-7]{52}$`)

type seeded struct{ r *rand.ChaCha8 }

func (s seeded) Read(p []byte) (int, error) { return s.r.Read(p) }

func newSeeded(b byte) seeded {
	var seed [32]byte
	seed[0] = b
	return seeded{rand.NewChaCha8(seed)}
}

func TestToken_FormatAndDeterminism(t *testing.T) {
	t.Parallel()
	mint := func(seed byte) []string {
		s := spend.New(spend.Options{Rand: newSeeded(seed), ConfirmKey: []byte("k")})
		var out []string
		for range 3 {
			tok, err := s.NewToken()
			if err != nil {
				t.Fatal(err)
			}
			if !tokenRE.MatchString(tok) {
				t.Fatalf("token %q", tok)
			}
			out = append(out, tok)
		}
		return out
	}
	a, b, c := mint(1), mint(1), mint(2)
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("same seed, different token %d", i)
		}
		if a[i] == c[i] {
			t.Errorf("different seeds, same token %d", i)
		}
	}
	if a[0] == a[1] {
		t.Error("two mints gave one token")
	}
	// crypto/rand default
	s := spend.New(spend.Options{ConfirmKey: []byte("k")})
	x, _ := s.NewToken()
	y, _ := s.NewToken()
	if x == y || !tokenRE.MatchString(x) {
		t.Errorf("default source: %q %q", x, y)
	}
	// only a keyed hash is stored
	h := s.HashToken(x)
	if len(h) != 64 || h == s.HashToken(y) || h != s.HashToken(x) || spend.New(spend.Options{ConfirmKey: []byte("other")}).HashToken(x) == h {
		t.Errorf("hash %q", h)
	}
}
