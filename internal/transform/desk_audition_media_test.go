package transform_test

// go-media tier of the Desk's auditions (DESK-S2): the real fluidsynth renders a plan's score with the generated soundfont, the real
// ffmpeg encodes the MP3, the real `metamusic dsp-run` child finishes the PCM. Skips like the other score media tests.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// TestAudition_EndToEnd: a plan's score becomes a stored MP3 of the promised length, a repeat is a free cache hit, the audition is
// served only through the resolver and the plan lists it.
func TestAudition_EndToEnd(t *testing.T) {
	r := scoreMediaRig(t)
	ctx := context.Background()
	text := []byte(mediaTune)
	if err := r.data.MkdirAll("plans/p1", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.data.WriteFile("plans/p1/score.abc", text, 0o444); err != nil {
		t.Fatal(err)
	}
	sum := librarytest.Sha(string(text))
	if _, err := r.st.W.Exec(`INSERT INTO desk_plans(id, source, track_id, engine, request_json, seed, cot, score_path, score_sha256, score_bytes, dialect,
		bars, meter, aligned, sections_json, expected_audio_s, title, principal_id, principal_kind, principal_name, created_at)
		VALUES ('p1','take',?,'yue2','{}',1,'full','plans/p1/score.abc',?,?,'standard',2,'4/4',0,'[]',4,'Synthetic','human:alice','human','alice','2026-11-21T10:00:00.000Z')`,
		r.fx["A"], sum, len(text)); err != nil {
		t.Fatal(err)
	}
	svc := desk.New(desk.Options{Store: r.st, Data: r.data, DataDir: r.dir, Transform: r.svc, FFmpeg: testtools.Tools().FFmpeg, Now: time.Now,
		Signer: serve.NewSigner([]byte("0123456789abcdef0123456789abcdef"), time.Now)})
	alice := auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", User: "alice", Role: auth.RoleOwner}
	tail := 1.0
	au, created, err := svc.Audition(ctx, alice, "p1", desk.AuditionIn{Soundfont: "gm", Chords: "none", TailS: &tail})
	if err != nil || !created {
		t.Fatalf("audition: created %v err %v", created, err)
	}
	// 4 s of score and a 1 s tail
	if au.Seconds < 4.99 || au.Seconds > 5.05 || au.SynthVersion == "" || au.URL == "" {
		t.Errorf("audition = %+v", au)
	}
	var rel, sha string
	if err := r.st.R.QueryRow(`SELECT mp3_path, mp3_sha256 FROM desk_auditions WHERE id = ?`, au.ID).Scan(&rel, &sha); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(rel)))
	if err != nil || len(b) < 1000 || b[0] != 0xff {
		t.Fatalf("mp3 file: %d bytes, err %v", len(b), err)
	}
	if !strings.HasPrefix(rel, "derived/auditions/") {
		t.Errorf("path %s", rel)
	}
	if _, err := svc.Resolve(ctx, sha, "mp3"); err != nil {
		t.Errorf("resolve: %v", err)
	}
	again, created, err := svc.Audition(ctx, alice, "p1", desk.AuditionIn{Soundfont: "gm", Chords: "none", TailS: &tail})
	if err != nil || created || again.ID != au.ID {
		t.Fatalf("repeat: %+v created %v err %v", again, created, err)
	}
	// a lost file is rebuilt in place and must come out byte-identical (the immutable row keeps its mp3_sha256 and ETag)
	abs := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if _, created, err = svc.Audition(ctx, alice, "p1", desk.AuditionIn{Soundfont: "gm", Chords: "none", TailS: &tail}); err != nil || created {
		t.Fatalf("rebuild: created %v err %v", created, err)
	}
	if nb, err := os.ReadFile(abs); err != nil || !bytes.Equal(nb, b) {
		t.Errorf("rebuilt mp3 differs from the stored one (%d vs %d bytes, err %v)", len(nb), len(b), err)
	}
	// chords change the audio: another audition
	other, created, err := svc.Audition(ctx, alice, "p1", desk.AuditionIn{Soundfont: "gm", Chords: "block", TailS: &tail})
	if err != nil || !created || other.ID == au.ID {
		t.Fatalf("block chords: %+v created %v err %v", other, created, err)
	}
	pl, err := svc.Get(ctx, alice, "p1", desk.DefaultInclude)
	if err != nil || pl.Auditions == nil || len(*pl.Auditions) != 2 {
		t.Errorf("plan auditions: %v %v", err, pl.Auditions)
	}
	if ents, _ := os.ReadDir(filepath.Join(r.dir, "derived", "auditions")); func() bool {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "work-") {
				return true
			}
		}
		return false
	}() {
		t.Error("an audition work directory was left behind")
	}
}
