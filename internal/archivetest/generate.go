package archivetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/drafts"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	genOnce sync.Once
	genDir  string
	genErr  error
)

// GenerateArchive is the template of the M3 acceptance suites (C86.2): SyntheticArchive(tb, "acceptance")
// plus six drafts created through drafts.Service.Create whose parent_track_id are six distinct lineage
// roots other than the root the imported varianti draft hangs off, so together with that draft there are
// 7 drafts with 7 distinct mothers (the acceptance profile has 7 lineage roots). Heavy: built once per
// test binary (sync.Once); every test that generates takes its own Template.Copy, never a shared DB.
func GenerateArchive(tb testing.TB) Template {
	tb.Helper()
	base := SyntheticArchive(tb, "acceptance")
	genOnce.Do(func() { genDir, genErr = buildGenerateArchive(base) })
	if genErr != nil {
		tb.Fatalf("archivetest: building the generate archive: %v", genErr)
	}
	return Template{dir: genDir, profile: "custom"}
}

func buildGenerateArchive(base Template) (string, error) {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "mm-archivetest-generate-")
	if err != nil {
		return "", err
	}
	mu.Lock()
	roots = append(roots, root)
	mu.Unlock()
	dir := filepath.Join(root, "data")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	tb := &builderTB{}
	base.CopyTo(tb, dir)
	if tb.err != nil {
		return "", tb.err
	}
	data, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = data.Close() }()
	st, err := store.Open(ctx, filepath.Join(dir, "metamusic.db"))
	if err != nil {
		return "", err
	}
	if err := addGenerateDrafts(ctx, st, data); err != nil {
		_ = st.Close()
		return "", err
	}
	return dir, st.Close() // closing the last connection checkpoints the WAL: one .db file
}

func addGenerateDrafts(ctx context.Context, st *store.Store, data *os.Root) error {
	// the lineage roots (imported, with a real recipe) and the mother of the varianti draft
	rows, err := st.R.QueryContext(ctx, `SELECT t.id FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		WHERE t.id = t.root_track_id AND r.engine <> 'unknown' ORDER BY t.id`)
	if err != nil {
		return err
	}
	var rootIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		rootIDs = append(rootIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var varianti string
	if err := st.R.QueryRowContext(ctx, `SELECT parent_track_id FROM recipe_drafts WHERE parent_track_id IS NOT NULL AND origin = 'varianti_import' LIMIT 1`).Scan(&varianti); err != nil {
		return fmt.Errorf("the varianti draft's mother: %w", err)
	}
	var mothers []string
	for _, id := range rootIDs {
		if id != varianti {
			mothers = append(mothers, id)
		}
	}
	if len(rootIDs) != 7 || len(mothers) != 6 {
		return fmt.Errorf("the acceptance profile has %d roots (%d besides the varianti mother), want 7 (6)", len(rootIDs), len(mothers))
	}
	lib := library.New(st, data, time.Now)
	svc := drafts.New(st, lib, time.Now, nil)
	p := auth.Principal{Kind: auth.KindHuman, ID: "generate-archive", Name: "generate-archive", Via: "test"}
	for i, mother := range mothers {
		m := mother
		req := fmt.Sprintf(`{"id":"gen-draft-%d","style":"english, synth pop, 100 bpm","lyrics":"[Verse]\nla la\nla\nla\nla\n[Chorus]\nla\nla\nla\nla","cot":"full","seed":%d}`, i+1, 7100+i)
		if _, err := svc.Create(ctx, p, drafts.NewDraft{Request: []byte(req), ParentTrackID: &m}); err != nil {
			return fmt.Errorf("draft %d: %w", i+1, err)
		}
	}
	return nil
}

// builderTB lets the sync.Once body reuse Template.CopyTo, which reports failures through testing.TB.
type builderTB struct {
	testing.TB
	err error
}

func (b *builderTB) Helper()                   {}
func (b *builderTB) Fatalf(f string, a ...any) { b.err = fmt.Errorf(f, a...) }
func (b *builderTB) Fatal(a ...any)            { b.err = fmt.Errorf("%v", a) }
