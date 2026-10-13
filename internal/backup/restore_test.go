package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func restoreOpts(e *env) RestoreOptions {
	return RestoreOptions{Now: e.opts.Now, NewID: e.opts.NewID, Version: "v-test"}
}

func dirEmpty(t *testing.T, dir string) bool {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(ents) == 0
}

func TestRestore_RoundTrip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write(t, "import/mapping.csv", "a,b\n")
	e.write(t, "import/reports/r1.json", `{"r":1}`)
	run, m, dir := e.run(t)
	into := filepath.Join(t.TempDir(), "restored")
	rep, err := Restore(context.Background(), dir, into, restoreOpts(e))
	if err != nil {
		t.Fatal(err)
	}
	if rep.SchemaFrom != m.SchemaVersion || rep.SchemaTo < rep.SchemaFrom || rep.Counts["tracks"] != m.Counts["tracks"] {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Linked != mediaKinds(m) || rep.Files != mediaKinds(m)+2 {
		t.Errorf("report files = %+v (media %d)", rep, mediaKinds(m))
	}
	for _, f := range m.Files {
		p := filepath.Join(into, filepath.FromSlash(f.Path))
		st, err := os.Stat(p)
		if f.Kind == "export" {
			if err == nil {
				t.Errorf("%s restored", f.Path)
			}
			continue
		}
		// linked files keep the mode of the (frozen) inode they share with the snapshot
		snapSt, _ := os.Stat(filepath.Join(dir, filepath.FromSlash(f.Path)))
		want := snapSt.Mode().Perm()
		if f.Kind == "import" {
			want = 0o600
		}
		if err != nil || st.Mode().Perm() != want {
			t.Errorf("%s: %v mode %v want %v", f.Path, err, st, want)
		}
		if sum, _, err := hashFile(p); err != nil || sum != f.SHA256 {
			t.Errorf("%s: sha %v", f.Path, err)
		}
	}
	for _, d := range []string{"import", "import/reports"} {
		if st, err := os.Stat(filepath.Join(into, d)); err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", d, st, err)
		}
	}
	st, err := store.Open(context.Background(), filepath.Join(into, "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var state, snap string
	if err := st.R.QueryRow(`SELECT state, snapshot FROM backup_runs WHERE id = ?`, run.ID).Scan(&state, &snap); err != nil || state != "completed" || snap != *run.Snapshot {
		t.Errorf("run row: %q %q %v", state, snap, err)
	}
	page, err := audit.NewStore(st, e.opts.NewID).List(context.Background(), audit.Filter{Op: "cli:restore"})
	if err != nil || len(page.Items) != 1 || page.Items[0].PrincipalID != "system:cli" || page.Items[0].Transport != "cli" {
		t.Errorf("audit = %+v (%v)", page, err)
	}
}

func TestRestore_RefusesNonEmptyTarget(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, _, dir := e.run(t)
	into := t.TempDir()
	if err := os.WriteFile(filepath.Join(into, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Restore(context.Background(), dir, into, restoreOpts(e))
	if !errors.Is(err, ErrTargetNotEmpty) {
		t.Fatalf("err = %v", err)
	}
	ents, _ := os.ReadDir(into)
	if len(ents) != 1 || ents[0].Name() != "keep.txt" {
		t.Errorf("target touched: %v", ents)
	}
	// an existing empty directory is fine
	if _, err := Restore(context.Background(), dir, t.TempDir(), restoreOpts(e)); err != nil {
		t.Errorf("empty target: %v", err)
	}
}

func TestRestore_DetectsTamper(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.opts.Link = exdev // the snapshot holds its own copies, so tampering cannot touch the live file
	e.svc = e.newService()
	_, m, dir := e.run(t)
	var victim ManifestFile
	for _, f := range m.Files {
		if f.Kind == "master" {
			victim = f
			break
		}
	}
	p := filepath.Join(dir, filepath.FromSlash(victim.Path))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0x01
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(t.TempDir(), "r")
	_, err = Restore(context.Background(), dir, into, restoreOpts(e))
	var ve *VerifyError
	if !errors.As(err, &ve) || len(ve.Paths) != 1 || ve.Paths[0] != victim.Path || !strings.Contains(err.Error(), victim.Path) {
		t.Fatalf("err = %v", err)
	}
	if !dirEmpty(t, into) {
		t.Error("something was written")
	}
	if _, err := os.Stat(into); err == nil {
		t.Error("the target directory was created")
	}
}

func TestRestore_MissingFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.opts.Link = exdev
	e.svc = e.newService()
	_, m, dir := e.run(t)
	var victim ManifestFile
	for _, f := range m.Files {
		if f.Kind == "rendition" {
			victim = f
			break
		}
	}
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(victim.Path))); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(t.TempDir(), "r")
	_, err := Restore(context.Background(), dir, into, restoreOpts(e))
	var ve *VerifyError
	if !errors.As(err, &ve) || len(ve.Paths) != 1 || ve.Paths[0] != victim.Path {
		t.Fatalf("err = %v", err)
	}
	if !dirEmpty(t, into) {
		t.Error("something was written")
	}
}

func TestRestore_NewerSchemaRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, m, dir := e.run(t)
	m.SchemaVersion += 5
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, manifestFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	into := filepath.Join(t.TempDir(), "r")
	_, err := Restore(context.Background(), dir, into, restoreOpts(e))
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("err = %v", err)
	}
	if !dirEmpty(t, into) {
		t.Error("something was written")
	}
}

func TestRestore_BadSnapshot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for name, setup := range map[string]func(dir string){
		"not a snapshot": func(string) {},
		"unsafe path": func(dir string) {
			b, _ := os.ReadFile(filepath.Join(dir, manifestFile))
			var m Manifest
			_ = json.Unmarshal(b, &m)
			m.Files = append(m.Files, ManifestFile{Path: "../escape", Kind: "master"})
			b, _ = json.Marshal(m)
			_ = os.WriteFile(filepath.Join(dir, manifestFile), b, 0o600)
		},
	} {
		dir := t.TempDir()
		if name != "not a snapshot" {
			_, _, dir = e.run(t)
		}
		setup(dir)
		into := filepath.Join(t.TempDir(), "r")
		if _, err := Restore(context.Background(), dir, into, restoreOpts(e)); !errors.Is(err, ErrBadSnapshot) {
			t.Errorf("%s: err = %v", name, err)
		}
		if !dirEmpty(t, into) {
			t.Errorf("%s: something was written", name)
		}
	}
}

func TestRestore_ImportFilesWritable(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.write(t, "import/mapping.csv", "x,y\n1,2\n")
	_, _, dir := e.run(t)
	into := filepath.Join(t.TempDir(), "r")
	if _, err := Restore(context.Background(), dir, into, restoreOpts(e)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(into, "import", "mapping.csv")
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	a, _ := os.ReadFile(p)
	b, _ := os.ReadFile(filepath.Join(dir, "import", "mapping.csv"))
	if string(a) != string(b) || string(a) != "x,y\n1,2\n" {
		t.Errorf("content %q vs %q", a, b)
	}
	if err := os.WriteFile(p, []byte("changed"), 0o600); err != nil {
		t.Errorf("not writable: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "import", "mapping.csv")); string(got) != "x,y\n1,2\n" {
		t.Error("the snapshot copy changed with the restored one (shared inode)")
	}
}

func TestRestore_CopiesWhenLinkFails(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	_, m, dir := e.run(t)
	o := restoreOpts(e)
	o.Link = exdev
	into := filepath.Join(t.TempDir(), "r")
	rep, err := Restore(context.Background(), dir, into, o)
	if err != nil || rep.Linked != 0 || rep.Copied != mediaKinds(m) {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, f := range m.Files {
		if f.Kind != "master" {
			continue
		}
		st, err := os.Stat(filepath.Join(into, filepath.FromSlash(f.Path)))
		if err != nil || st.Mode().Perm() != 0o444 {
			t.Errorf("%s: %v %v", f.Path, st, err)
		}
	}
}
