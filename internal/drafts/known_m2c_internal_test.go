package drafts

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestKnownHashes_IgnoreStubs: a stub recipe's {id,seed,title} JSON never counts as a duplicate.
func TestKnownHashes_IgnoreStubs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lt.FixtureM2c(t, st, lt.FixedNow)
	s := New(st, library.New(st, nil, nil), nil, nil)
	var reqJSON, hash string
	if err := st.R.QueryRow(`SELECT request_json, hash FROM recipes WHERE engine = 'unknown' LIMIT 1`).Scan(&reqJSON, &hash); err != nil {
		t.Fatal(err)
	}
	items := []yue2.LintItem{{Request: json.RawMessage(reqJSON)}}
	known, err := s.knownHashes(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	if where, dup := known(hash, "", ""); dup {
		t.Errorf("stub counted as duplicate: %s", where)
	}
	if p, err := yue2.ParseRequest(items[0].Request, yue2.ParseOptions{}); err == nil {
		if where, dup := known(p.Hash, "", ""); dup {
			t.Errorf("stub counted as duplicate by canonical hash: %s", where)
		}
	}
}
