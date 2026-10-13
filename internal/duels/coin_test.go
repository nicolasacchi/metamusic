package duels

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestSlotCoin_Fair(t *testing.T) {
	t.Parallel()
	const n = 100000
	ones := 0
	for range n {
		if cryptoCoin() {
			ones++
		}
	}
	// +-4 sigma of Binomial(n, 1/2)
	if share := float64(ones) / n; share < 0.4937 || share > 0.5063 {
		t.Errorf("share of true = %.4f", share)
	}
}

func TestFitCache_LRU(t *testing.T) {
	t.Parallel()
	c := newFitCache(3)
	for i := int64(0); i < 3; i++ {
		c.put(fitKey{n: i}, &fitEntry{})
	}
	c.get(fitKey{n: 0}) // refresh the oldest: 1 is now least recently used
	c.put(fitKey{n: 3}, &fitEntry{})
	c.put(fitKey{n: 4}, &fitEntry{})
	if c.len() != 3 {
		t.Fatalf("len = %d", c.len())
	}
	for n, want := range map[int64]bool{0: true, 1: false, 2: false, 3: true, 4: true} {
		if _, ok := c.get(fitKey{n: n}); ok != want {
			t.Errorf("key %d present = %v, want %v", n, ok, want)
		}
	}
	if cacheEntries != 32 {
		t.Errorf("cacheEntries = %d, the plan says 32", cacheEntries)
	}
}

func TestIsUnique(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.W.Exec(`CREATE TABLE tt(x TEXT UNIQUE NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.Exec(`INSERT INTO tt VALUES ('a')`); err != nil {
		t.Fatal(err)
	}
	_, dup := st.W.Exec(`INSERT INTO tt VALUES ('a')`)
	_, null := st.W.Exec(`INSERT INTO tt VALUES (NULL)`)
	if !isUnique(dup) || isUnique(null) || isUnique(nil) || isUnique(errors.New("x")) {
		t.Errorf("isUnique: dup %v, not-null %v", isUnique(dup), isUnique(null))
	}
}
