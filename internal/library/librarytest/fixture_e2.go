package librarytest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// The E2 fixture (C162.2) is FixtureL3 plus the rows the ACE-Step lanes read, SQL-only through the library write path and
// synthetic. L3 already supplies the real-world shapes (draft-only Songs d1-d4 and u2, the reference FLAC and lossy imports,
// Songs with and without Picks, human-rated takes T1-T6 in their own Songs); E2 adds:
//
//	golden      the three highest-rated yue2 takes T1, T2 and T3 (human stars 5, 4, 5), one playlist "Synthetic golden" holding them
//	AP          an ACE-Step `port` child of T1 (same Song): operator engine_port, cross_engine, lyrics.lrc and stem-vocals.flac artifacts
//	AR          an ACE-Step compose_new root in a Song of its own (a draft-only Song d1 stays draft-only)
//	AREF        a `reference` child of AR: recipe ref {AR, clip sha, 10.0..30.0 s}, operator reference_render (the dashed edge)
//	AREP        a `repaint` child of AP: recipe src {AP, master sha}, range 8.0..14.0 s, operator repaint
//	record      a draft Record "Synthetic E2 Draft" with three slots (T1, T2, T3 Songs)
//
// The ACE takes are job takes of the fake-acestep backend (batch, job and attempt rows, environment fingerprint
// acestep15); no audio exists here. The Reconciler assigns Songs later, as for any generated take.

// E2Info lists the E2 rows on top of L3Info.
type E2Info struct {
	L3Info
	Golden   [3]string // T1, T2, T3 take ids
	Playlist string
	// ACE take ids.
	Port, Root, Reference, Repaint string
	// Record is the draft Record id; RecordSlots its three slot ids in order.
	Record      string
	RecordSlots [3]string
	// Env is the acestep15 environment fingerprint id.
	Env string
}

const (
	e2Space = 0xe20000
	// E2Env is the environment fingerprint of the ACE-Step takes.
	E2Env = "env_e2acestep00001"
	// E2LRC is the body the lyrics.lrc artifact stands for (the file is not written; the row carries its digest).
	e2LRCName = "lyrics.lrc"
)

func e2ID(n int) string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", e2Space+n) }

type e2Builder struct {
	tb  testing.TB
	st  *store.Store
	now time.Time
	out E2Info
	n   int
}

func (b *e2Builder) id() string { b.n++; return e2ID(b.n) }

func (b *e2Builder) exec(q string, a ...any) { mustExec(b.tb, b.st, q, a...) }

func (b *e2Builder) env() {
	b.exec(`INSERT OR IGNORE INTO env_fingerprints(env_id, engine, pkg_version, code_commit, weights_json, decoder, backend, gpu_class, image_digest, detail_json, first_seen_at)
		VALUES (?, 'acestep15', '1.5.0', 'abcdef1', '{"ACE-Step/Ace-Step1.5":"0123456789abcdef0123456789abcdef01234567"}', 'ACE-Step/Ace-Step1.5:vae', 'fake-acestep', 'RTX 4090', 'sha256:fixture-ace',
		'{"gpu_name":"Fixture GPU","cuda":"12.8","torch":"2.10","worker_build":"fixture","python":"3.12"}', ?)`, E2Env, tsM3(b.now.Add(-2*time.Hour)))
	b.exec(`INSERT OR IGNORE INTO provider_state(backend, armed, changed_by, changed_at) VALUES ('fake-acestep', 1, 'system:default', ?)`, tsM3(b.now.Add(-24*time.Hour)))
	b.out.Env = E2Env
}

// aceTake inserts one ACE-Step job take (batch, estimate, job, attempt-less) through InsertTake. parent "" makes a root.
func (b *e2Builder) aceTake(label, request, operator string, params map[string]any, parent, root string, gen int, age time.Duration, artifacts []library.NewArtifact) string {
	b.tb.Helper()
	ctx := context.Background()
	svc := library.New(b.st, nil, func() time.Time { return b.now })
	svc.UseIDFunc(b.id)
	eng := acestep.Engine{}
	rec, issues := eng.ParseRecipe([]byte(request))
	if acestep.HasErrors(issues) {
		b.tb.Fatalf("fixture E2: %s request: %v", label, issues)
	}
	t0 := b.now.Add(-age)
	batch, est, job, trackID := b.id(), b.id(), b.id(), b.id()
	insertEstimateAndBatch(b.tb, b.st, batchSpec{engine: "acestep15", backend: "fake-acestep", id: batch, estimateID: est, principal: humanKey, kind: "human",
		name: "fixture", via: "human_create", state: "active", jobs: []string{job}, created: t0})
	tx := begin(b.tb, b.st)
	rid, _, err := svc.InsertEngineRecipe(ctx, tx, acestep.Name, rec, humanKey)
	check(b.tb, err)
	check(b.tb, tx.Commit())
	insertJob(b.tb, b.st, jobSpec{engine: "acestep15", backend: "fake-acestep", id: job, batchID: batch, recipeID: rid, parent: parent, operator: operator,
		title: "Synthetic ACE " + label, state: "queued", created: t0,
		mutation: fmt.Sprintf(`{"operator":%q,"params":{},"recipe_patch":null,"rationale":null,"principal_id":%q}`, operator, humanKey)})
	for i, to := range []string{"submitting", "starting", "generating", "transferring", "ingesting"} {
		b.exec(`UPDATE jobs SET state = ?, updated_at = ? WHERE id = ?`, to, tsM3(t0.Add(time.Duration(i+1)*time.Minute)), job)
	}
	h := sha([]byte("e2-master/" + label))
	slug := yue2.Slug("synthetic ace " + label)
	nt := library.NewTake{
		ID: trackID, Title: "Synthetic ACE " + label, Slug: slug, RecipeID: rid, DurationS: 30, SampleRate: 48000, BitsPerSample: 24, Channels: 2, TotalSamples: 30 * 48000,
		MasterPath: "audio/masters/" + slug + "__" + h[:8] + ".flac", MasterSHA256: h, MasterBytes: 64, SourceSHA256: sha([]byte("e2-src/" + label)),
		FramesSHA256: sha([]byte("e2-frames/" + label)), AudioMD5: h[:32], ImportRule: "job", Origin: "job", StatusSetBy: "system:job", JobID: job, EnvID: E2Env,
		PrincipalID: humanKey, RecordedAt: t0.Add(5 * time.Minute), ParentTrackID: parent, RootTrackID: root, Generation: gen, Artifacts: artifacts,
		Renditions: []library.NewRendition{{Kind: "peaks", SHA256: sha([]byte("e2-peaks/" + label)), Bytes: 32, Path: "derived/e2/" + h[:16] + ".peaks.json", Params: map[string]any{"format": analysis.PeaksFormat}}},
		Loudness: analysis.Loudness{IntegratedLUFS: -14, LRA: 5, LRALowLUFS: -19, LRAHighLUFS: -13, TruePeakDBTP: -1.2, SamplePeakDBFS: -1.5, ShortTermMaxLUFS: -12,
			ShortTerm1Hz: []float64{-14, -13.5}},
		Spectrum: analysis.Spectrum{BandwidthHz: bptrInt(16000), RefDB: -40, ThresholdDB: -110, Frames: 30}, ToolVersion: "ffmpeg n9.0.1", ReplayGainDB: -4,
		Mutation: &library.NewMutation{Operator: operator, JobID: job, Params: params},
	}
	if parent == "" {
		nt.RootTrackID = ""
	}
	tx = begin(b.tb, b.st)
	check(b.tb, svc.InsertTake(ctx, tx, nt))
	check(b.tb, tx.Commit())
	b.exec(`UPDATE jobs SET state = 'completed', outcome = 'clean', track_id = ?, env_id = ?, audio_s = 30, started_at = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
		trackID, E2Env, tsM3(t0.Add(time.Minute)), tsM3(t0.Add(6*time.Minute)), tsM3(t0.Add(6*time.Minute)), job)
	return trackID
}

func (b *e2Builder) world() {
	b.tb.Helper()
	b.env()
	take := func(l string) string { return b.out.Takes[l] }
	b.out.Golden = [3]string{take("T1"), take("T2"), take("T3")}

	// the golden playlist: items are track references with positions
	items := fmt.Sprintf(`[{"track_id":%q},{"track_id":%q},{"track_id":%q}]`, b.out.Golden[0], b.out.Golden[1], b.out.Golden[2])
	b.out.Playlist = b.id()
	at := tsM3(b.now.Add(-2 * time.Hour))
	b.exec(`INSERT INTO playlists(id,name,description,items_json,items_count,revision,created_by,updated_by,created_at,updated_at) VALUES(?,?,?,?,3,1,?,?,?,?)`,
		b.out.Playlist, "Synthetic golden", "Synthetic engine trial candidates", items, FixtureHuman, FixtureHuman, at, at)

	// ACE takes
	const base = `{"id":%q,"task":%q,"caption":"synthwave, female vocal","lyrics":"[verse]\nla la","instrumental":false,"vocal_language":"en","bpm":96,"seed":%d,%s"thinking":%t,"engine_params_version":"ace1","inference_steps":8%s}`
	port := fmt.Sprintf(base, "e2-ace-port", "text2music", 4242, `"duration_ds":300,`, true, "")
	rootT1 := b.out.Golden[0]
	lrc, stem := sha([]byte("e2-lrc")), sha([]byte("e2-stem-vocals"))
	b.out.Port = b.aceTake("port", port, "engine_port", map[string]any{"mode": "port", "cross_engine": true}, rootT1, rootT1, 1, 80*time.Minute, []library.NewArtifact{
		{Name: e2LRCName, SHA256: lrc, Path: "takes/e2-port/lyrics.lrc", Bytes: 120},
		{Name: "stem-vocals.flac", SHA256: stem, Path: "takes/e2-port/stem-vocals.flac", Bytes: 2048},
	})
	rootReq := fmt.Sprintf(base, "e2-ace-root", "text2music", 4343, `"duration_ds":300,`, false, "")
	b.out.Root = b.aceTake("root", rootReq, "compose_new", map[string]any{"mode": "compose_new"}, "", "", 0, 70*time.Minute, nil)
	refClip := sha([]byte("e2-ref-clip"))
	refReq := fmt.Sprintf(base, "e2-ace-ref", "text2music", 4344, `"duration_ds":300,`, false,
		fmt.Sprintf(`,"ref":{"track_id":%q,"clip_sha256":%q,"from_ds":100,"to_ds":300}`, b.out.Root, refClip))
	b.out.Reference = b.aceTake("ref", refReq, "reference_render", map[string]any{"mode": "reference"}, b.out.Root, b.out.Root, 1, 60*time.Minute, nil)
	srcSHA := sha([]byte("e2-master/port"))
	repReq := fmt.Sprintf(base, "e2-ace-rep", "repaint", 4242, "", false,
		fmt.Sprintf(`,"src":{"track_id":%q,"master_sha256":%q,"duration_ds":300},"repaint_from_ds":80,"repaint_to_ds":140`, b.out.Port, srcSHA))
	b.out.Repaint = b.aceTake("rep", repReq, "repaint", map[string]any{"mode": "repaint", "cross_engine": false}, b.out.Port, rootT1, 2, 50*time.Minute, nil)

	// a draft Record of the golden Songs (three floating slots)
	b.out.Record = b.id()
	created := tsM3(b.now.Add(-time.Hour))
	b.exec(`INSERT INTO records(id,title,subtitle,liner_note,cover_seed,cover_rerolls,status,revision,created_by,created_at,updated_by,updated_at)
		VALUES(?,?,?,?,?,0,'draft',1,?,?,?,?)`, b.out.Record, "Synthetic E2 Draft", "", "", l2CoverSeed(b.out.Record, 0), FixtureHuman, created, FixtureHuman, created)
	order := make([]string, 0, 3)
	for i, l := range []string{"T1", "T2", "T3"} {
		var song string
		if err := b.st.R.QueryRow(`SELECT song_id FROM song_roots WHERE root_track_id = ?`, take(l)).Scan(&song); err != nil {
			b.tb.Fatalf("fixture E2: song of %s: %v", l, err)
		}
		sid := b.id()
		b.out.RecordSlots[i] = sid
		order = append(order, `"`+sid+`"`)
		b.exec(`INSERT INTO record_slots(id,record_id,song_id,synced_seq,added_by,added_at) VALUES(?,?,?,0,?,?)`, sid, b.out.Record, song, FixtureHuman, created)
	}
	b.exec(`UPDATE records SET order_json=? WHERE id=?`, "["+order[0]+","+order[1]+","+order[2]+"]", b.out.Record)
}

// FixtureE2 is FixtureL3 plus the E2 world above (SQL-only; signature as FixtureL3). It needs schema 15 (migration 0015, the
// engines migration) or later.
func FixtureE2(tb testing.TB, st *store.Store, now time.Time) E2Info {
	tb.Helper()
	b := &e2Builder{tb: tb, st: st, now: now, out: E2Info{L3Info: FixtureL3(tb, st, now)}}
	b.world()
	return b.out
}

// ApplyE2 adds the E2 world to a library that already holds the L3 world (servertest uses it).
func ApplyE2(tb testing.TB, st *store.Store, now time.Time, l3 L3Info) E2Info {
	tb.Helper()
	b := &e2Builder{tb: tb, st: st, now: now, out: E2Info{L3Info: l3}}
	b.world()
	return b.out
}
