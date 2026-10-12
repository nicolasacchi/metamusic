package importer

import (
	"os"
	"strings"
	"syscall"
	"testing"
)

const yue2ReqJSON = "{\n  \"style\": \"pop\",\n  \"lyrics\": \"[Verse 1]\\nla\",\n  \"cot\": \"full\",\n  \"seed\": 5,\n  \"abc\": null,\n  \"cfg_scale\": null,\n  \"id\": \"cls-rd\"\n}\n"

const smallReq = `{"id":"r1","style":"s","lyrics":"[Verse]\nl","seed":1}`

type clsFile struct {
	path string
	mode string // "" text, "flac:<frames>", "fifo", "link:<target>"
	body string
	want string
}

func TestClassify_Table(t *testing.T) {
	e := newEnv(t)
	files := []clsFile{
		// temp
		{".x.flac.Ab12Cd", "flac:t1", "", kIgnTemp},
		{"a.flac.part", "", "x", kIgnTemp},
		{"b.flac.partial", "", "x", kIgnTemp},
		{"c.tmp", "", "x", kIgnTemp},
		{"d.crdownload", "", "x", kIgnTemp},
		{"e.flac~", "", "x", kIgnTemp},
		{"__MACOSX/x.flac", "flac:t2", "", kIgnTemp},
		{".DS_Store", "", "x", kIgnTemp},
		{"Thumbs.db", "", "x", kIgnTemp},
		{"sub/.git/config", "", "x", kIgnTemp},
		// archives
		{"x.zip", "", "PK", kIgnArchive},
		{"x.tar", "", "x", kIgnArchive},
		{"x.tgz", "", "x", kIgnArchive},
		{"x.tar.gz", "", "x", kIgnArchive},
		{"x.tar.xz", "", "x", kIgnArchive},
		{"x.txz", "", "x", kIgnArchive},
		{"x.tar.bz2", "", "x", kIgnArchive},
		{"x.tbz2", "", "x", kIgnArchive},
		{"x.tar.zst", "", "x", kIgnArchive},
		{"x.7z", "", "x", kIgnArchive},
		{"x.rar", "", "x", kIgnArchive},
		{"UP.ZIP", "", "x", kIgnArchive},
		// FLAC
		{"nomagic.flac", "", "this is not flac", kNotFLAC},
		{"good.flac", "flac:g1", "", kAudioFLAC},
		{"X.FLAC", "flac:g2", "", kAudioFLAC},
		// recipes
		{"y.Jsonl", "", req("cls-j", 1) + "\n", kRecipesJSONL},
		{"rec.jsonl", "", `{"id":"abc-1","style":"s","lyrics":"[Verse]\nl","seed":3,"cot":"melody","abc_path":"abc/song.abc"}` + "\n", kRecipesJSONL},
		{"abc/song.abc", "", "X:1\nK:C\nCDEF|]\n", kABCSource},
		{"scores/lonely.abc", "", "X:1\nK:C\nCDEF|]\n", kScoreABC},
		{"arr.json", "", "[" + smallReq + "," + strings.Replace(smallReq, `"r1"`, `"r2"`, 1) + "]", kRecipesJSONArray},
		{"mixed.json", "", "[" + smallReq + ", 5]", kIgnJSON},
		{"req.json", "", smallReq, kRequestJSON},
		{"coll.json", "", `{"name":"c","songs":[{"title":"T","variant_ids":["r1"]}]}`, kCollectionJSON},
		{"playlist.json", "", `{"playlist":"p","owner":"o","url":"u","tracks":[{"title":"t"}]}`, kIgnPlaylist},
		{"other.json", "", `{"pass":true,"files":1}`, kIgnJSON},
		{"invalid.json", "", `{oops`, kIgnJSON},
		{"empty-array.json", "", `[]`, kIgnJSON},
		{"idx.csv", "", "variant_id,seed,source_title\nr1,1,T\n", kReferenceIndex},
		{"other.csv", "", "a,b\n1,2\n", kIgnUnknown},
		{"notes.txt", "", "hello", kIgnUnknown},
		{"README", "", "hello", kIgnUnknown},
		{"latent.npy", "", "x", kIgnUnknown},
		// result dir
		{"rd1/audio.flac", "flac:rd1", "", kAudioFLAC},
		{"rd1/request.json", "", yue2ReqJSON, kRequestJSON},
		{"rd1/result.json", "", `{"truncated":{"abc":false,"semantic":false}}`, kYue2Result},
		{"rd1/score.abc", "", "X:1\nK:C\nCDEF|]\n", kScoreABC},
		{"rd1/config.json", "", `{"a":1}`, kArtifact},
		{"rd1/semantic.npy", "", "npy", kArtifact},
		{"rd1/latent.npy", "", "npy", kIgnLatent},
		{"rd1/bad name.json", "", "{}", kIgnUnsafeName},
		{"rd1/hq.mp3", "", "mp3", kIgnMP3Sidecar},
		{"rd1/sub/x.flac", "flac:rd1sub", "", kAudioFLAC},
		{"rd1/sub/notes.txt", "", "x", kIgnUnknown},
		// mp3 sidecars and orphans
		{"audio/take.flac", "flac:take", "", kAudioFLAC},
		{"mp3/take.mp3", "", "mp3", kIgnMP3Sidecar},
		{"mp3/orphan.mp3", "", "mp3", kIgnOrphanMP3},
		{"a/two.flac", "flac:two-a", "", kAudioFLAC},
		{"b/two.flac", "flac:two-b", "", kAudioFLAC},
		{"m/two.mp3", "", "mp3", kIgnOrphanMP3},
		// non-regular
		{"link.flac", "link:good.flac", "", kIgnSymlink},
		{"pipe.flac", "fifo", "", kIgnNotRegular},
	}
	if len(files) < 40 {
		t.Fatalf("table has %d rows", len(files))
	}
	for _, f := range files {
		switch {
		case strings.HasPrefix(f.mode, "flac:"):
			e.flac(f.path, strings.TrimPrefix(f.mode, "flac:"))
		case f.mode == "fifo":
			if err := syscall.Mkfifo(e.abs(f.path), 0o644); err != nil {
				t.Skip(err)
			}
		case strings.HasPrefix(f.mode, "link:"):
			if err := os.Symlink(strings.TrimPrefix(f.mode, "link:"), e.abs(f.path)); err != nil {
				t.Fatal(err)
			}
		default:
			e.put(f.path, f.body)
		}
	}
	_, inv := e.inventory(func(o *Options) { o.MinAge = 2 * 60 * 1e9 })
	got := map[string]string{}
	for _, en := range inv.b.entries {
		got[en.path] = en.kind
	}
	for _, f := range files {
		if got[f.path] != f.want {
			t.Errorf("%-22s kind = %q, want %q", f.path, got[f.path], f.want)
		}
	}
	if !hasCode(inv.Report.Warnings, "json_array_not_requests") || !hasCode(inv.Report.Warnings, "invalid_json") ||
		!hasCode(inv.Report.Warnings, "result_subdir") || !hasCode(inv.Report.Warnings, "orphan_mp3") {
		t.Errorf("warnings = %v", issueCodes(inv.Report.Warnings))
	}
	if !hasCode(inv.Report.Errors, "not_flac") {
		t.Errorf("errors = %v", issueCodes(inv.Report.Errors))
	}
	if sc := audioByPath(&inv.Report, "audio/take.flac"); sc == nil || len(sc.Sidecars) != 1 || sc.Sidecars[0] != "mp3/take.mp3" {
		t.Errorf("sidecars = %+v", sc)
	}
}
