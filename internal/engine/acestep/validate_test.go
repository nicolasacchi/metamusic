package acestep

import (
	"strings"
	"testing"
)

func TestValidate_Table(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", MaxCaptionLen+1)
	caps := fullCaps()
	rows := []struct {
		name string
		raw  []byte
		want string // "" = valid; else the exact "code@field" list
	}{
		{"valid text2music", req(nil), ""},
		{"valid keyscale short form", req(obj{"keyscale": "am"}), ""},
		{"bad id", req(obj{"id": "../x"}), "bad_id@id"},
		{"dot id", req(obj{"id": "."}), "bad_id@id"},
		{"missing id", req(obj{"id": nil}), "missing_id@id"},
		{"missing task", req(obj{"task": nil}), "missing_task@task"},
		{"task cover", req(obj{"task": "cover"}), "ENGINE_CAPABILITY@task"},
		{"task lego", req(obj{"task": "lego"}), "ENGINE_CAPABILITY@task"},
		{"empty caption", req(obj{"caption": "  "}), "bad_caption@caption"},
		{"long caption", req(obj{"caption": long}), "bad_caption@caption"},
		{"lyrics too long", req(obj{"lyrics": strings.Repeat("a", MaxLyricsBytes+1)}), "lyrics_too_long@lyrics"},
		{"instrumental with lyrics", req(obj{"instrumental": true}), "instrumental_lyrics@lyrics"},
		{"vocal without lyrics", req(obj{"lyrics": ""}), "lyrics_required@lyrics"},
		{"instrumental ok", req(obj{"lyrics": "", "instrumental": true}), ""},
		{"bad language", req(obj{"vocal_language": "xx"}), "bad_vocal_language@vocal_language"},
		{"bpm low", req(obj{"bpm": 29}), "bad_bpm@bpm"},
		{"bpm high", req(obj{"bpm": 301}), "bad_bpm@bpm"},
		{"bpm edge", req(obj{"bpm": 300}), ""},
		{"bad keyscale", req(obj{"keyscale": "H minor"}), "bad_keyscale@keyscale"},
		{"bad time signature", req(obj{"timesignature": "5"}), "bad_timesignature@timesignature"},
		{"time signature 6", req(obj{"timesignature": "6"}), ""},
		{"negative seed", req(obj{"seed": -1}), "bad_seed@seed"},
		{"seed too big", req(obj{"seed": 1 << 53}), "bad_seed@seed"},
		{"seed max", req(obj{"seed": MaxSeed}), ""},
		{"turbo wrong steps", req(obj{"inference_steps": 9}), "bad_steps@inference_steps"},
		{"turbo guidance", req(obj{"guidance_milli": 7000}), "guidance_forbidden@guidance_milli"},
		{"duration missing", req(obj{"duration_ds": nil}), "duration_required@duration_ds"},
		{"duration short", req(obj{"duration_ds": 99}), "duration_too_short@duration_ds"},
		{"duration structural ceiling", req(obj{"duration_ds": 6001}), "duration_too_long@duration_ds"},
		{"duration above pool cap", req(obj{"duration_ds": 4801}), "ENGINE_CAPABILITY@duration_ds"},
		{"duration at pool cap", req(obj{"duration_ds": 4800}), ""},
		{"text2music with src", req(obj{"src": src(1000)}), "src_forbidden@src"},
		{"text2music with range", req(obj{"repaint_from_ds": 0, "repaint_to_ds": 100}), "range_forbidden@repaint_from_ds"},
		{"text2music with extend", req(obj{"extend_ds": 50}), "extend_forbidden@extend_ds"},
		{"text2music with tracks", req(obj{"tracks": []string{"bass"}}), "tracks_forbidden@tracks"},
		{"lrc ok", req(obj{"lrc": true}), ""},
		{"lrc instrumental", req(obj{"lrc": true, "lyrics": "", "instrumental": true}), "lrc_requires_lyrics@lrc"},
		{"ref ok", req(obj{"ref": obj{"track_id": "trk-2", "clip_sha256": shaB, "from_ds": 0, "to_ds": 200}}), ""},
		{"ref short window", req(obj{"ref": obj{"track_id": "trk-2", "clip_sha256": shaB, "from_ds": 0, "to_ds": 199}}), "reference_window@ref"},
		{"ref long window", req(obj{"ref": obj{"track_id": "trk-2", "clip_sha256": shaB, "from_ds": 0, "to_ds": 301}}), "reference_window@ref"},
		{"ref bad sha", req(obj{"ref": obj{"track_id": "trk-2", "clip_sha256": "xyz", "from_ds": 0, "to_ds": 300}}), "bad_ref@ref.clip_sha256"},
		{"ref bad track", req(obj{"ref": obj{"track_id": "", "clip_sha256": shaB, "from_ds": 0, "to_ds": 300}}), "bad_ref@ref.track_id"},
		{"repaint ok", req(repaintOver(100, 400)), ""},
		{"repaint no range", req(obj{"task": "repaint", "duration_ds": nil, "thinking": false, "src": src(1000)}), "range_required@repaint_from_ds"},
		{"repaint order", req(repaintOver(400, 100)), "range_order@repaint_from_ds"},
		{"repaint outside", req(repaintOver(800, 1100)), "range_outside_source@repaint_to_ds"},
		{"repaint too short", req(repaintOver(100, 129)), "range_too_short@repaint_to_ds"},
		{"repaint too long", req(repaintOver(0, 901)), "range_too_long@repaint_to_ds"},
		{"repaint 3s edge", req(repaintOver(100, 130)), ""},
		{"repaint 90s edge", req(repaintOver(0, 900)), ""},
		{"repaint thinking", req(obj{"task": "repaint", "duration_ds": nil, "src": src(1000), "repaint_from_ds": 0, "repaint_to_ds": 300}), "thinking_ignored@thinking"},
		{"repaint without src", req(obj{"task": "repaint", "duration_ds": nil, "thinking": false, "repaint_from_ds": 0, "repaint_to_ds": 300}), "src_required@src"},
		{"repaint src sha", req(obj{"task": "repaint", "duration_ds": nil, "thinking": false, "src": obj{"track_id": "t", "master_sha256": "AB", "duration_ds": 1000}, "repaint_from_ds": 0, "repaint_to_ds": 300}), "bad_src@src.master_sha256"},
		{"repaint src track", req(obj{"task": "repaint", "duration_ds": nil, "thinking": false, "src": obj{"track_id": "", "master_sha256": shaA, "duration_ds": 1000}, "repaint_from_ds": 0, "repaint_to_ds": 300}), "bad_src@src.track_id"},
		{"repaint src short", req(obj{"task": "repaint", "duration_ds": nil, "thinking": false, "src": src(99), "repaint_from_ds": 0, "repaint_to_ds": 40}), "src_too_short@src.duration_ds"},
		{"repaint src long", req(obj{"task": "repaint", "duration_ds": nil, "thinking": false, "src": src(6001), "repaint_from_ds": 0, "repaint_to_ds": 300}), "src_too_long@src.duration_ds"},
		{"repaint with duration", req(obj{"task": "repaint", "thinking": false, "src": src(1000), "repaint_from_ds": 0, "repaint_to_ds": 300}), "duration_forbidden@duration_ds"},
		{"extend ok", req(extendOver(305)), ""},
		{"extend missing", req(obj{"task": "extend", "duration_ds": nil, "thinking": false, "src": src(2431)}), "extend_required@extend_ds"},
		{"extend short", req(extendOver(29)), "extend_too_short@extend_ds"},
		{"extend long", req(extendOver(901)), "extend_too_long@extend_ds"},
		{"extend total over 600", req(obj{"task": "extend", "duration_ds": nil, "thinking": false, "src": src(5990), "extend_ds": 100}), "total_over_600@extend_ds"},
		{"extend over pool cap", req(obj{"task": "extend", "duration_ds": nil, "thinking": false, "src": src(4600), "extend_ds": 300}), "ENGINE_CAPABILITY@duration_ds"},
		{"extend with ref", req(obj{"task": "extend", "duration_ds": nil, "thinking": false, "src": src(2431), "extend_ds": 100, "ref": obj{"track_id": "r", "clip_sha256": shaB, "from_ds": 0, "to_ds": 300}}), "ref_forbidden@ref"},
		{"complete ok", req(completeOver("drums", "bass")), ""},
		{"complete base steps ok", req(func() obj {
			o := completeOver("drums")
			o["inference_steps"] = 32
			o["guidance_milli"] = 1000
			return o
		}()), ""},
		{"complete no tracks", req(completeOver()), "tracks_required@tracks"},
		{"complete five tracks", req(completeOver("bass", "brass", "drums", "fx", "guitar")), "tracks_too_many@tracks"},
		{"complete duplicate", req(completeOver("drums", "drums")), "tracks_duplicate@tracks"},
		{"complete unknown", req(completeOver("kazoo")), "unknown_track_name@tracks"},
		{"complete steps low", req(func() obj { o := completeOver("drums"); o["inference_steps"] = 31; return o }()), "bad_steps@inference_steps"},
		{"complete guidance high", req(func() obj { o := completeOver("drums"); o["guidance_milli"] = 15001; return o }()), "bad_guidance@guidance_milli"},
		{"complete thinking", req(func() obj { o := completeOver("drums"); o["thinking"] = true; return o }()), "thinking_ignored@thinking"},
		{"extract ok", req(func() obj { o := completeOver("vocals"); o["task"] = "extract"; return o }()), ""},
		{"extract two", req(func() obj { o := completeOver("vocals", "bass"); o["task"] = "extract"; return o }()), "tracks_too_many@tracks"},
		{"unknown key", req(obj{"surprise": 1}), "unknown_key@"},
		{"wrong type", req(obj{"seed": "7"}), "bad_type@"},
		{"float seed", req(obj{"seed": 1.5}), "bad_type@"},
		{"not object", []byte(`[1]`), "not_object@"},
		{"empty", []byte(``), "not_object@"},
		{"bad utf8", []byte("{\"id\":\"\xff\"}"), "bad_utf8@"},
		{"duplicate key", []byte(`{"id":"a","id":"b"}`), "bad_json@"},
		{"truncated json", []byte(`{"id":`), "bad_json@"},
		{"unknown params version", req(obj{"engine_params_version": "nope"}), "ENGINE_PARAMS_STALE@engine_params_version"},
		{"nested unknown key", req(obj{"src": obj{"x": 1}}), "unknown_key@"},
	}
	if len(rows) < 40 {
		t.Fatalf("table has %d rows, want >= 40", len(rows))
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := Engine{Verified: EmbeddedFeatures()}
			e.Verified.Tasks = []string{"text2music", "repaint", "extend", "complete", "extract"}
			e.Verified.CanaryCommit = "c0ffee"
			e.Verified.Reference, e.Verified.LRC = true, true
			if got := codes(e.Validate(tc.raw)); got != tc.want {
				t.Errorf("Validate = %q, want %q", got, tc.want)
			}
		})
	}
	_ = caps
}

func TestCheck_Capabilities(t *testing.T) {
	t.Parallel()
	def := Engine{Verified: EmbeddedFeatures()}
	for name, tc := range map[string]struct {
		raw  []byte
		want string
	}{
		"repaint off":   {req(repaintOver(100, 400)), "ENGINE_CAPABILITY@task"},
		"extend off":    {req(extendOver(100)), "ENGINE_CAPABILITY@task"},
		"complete off":  {req(completeOver("bass")), "ENGINE_CAPABILITY@task"},
		"extract off":   {req(func() obj { o := completeOver("bass"); o["task"] = "extract"; return o }()), "ENGINE_CAPABILITY@task"},
		"reference off": {req(obj{"ref": obj{"track_id": "r", "clip_sha256": shaB, "from_ds": 0, "to_ds": 300}}), "ENGINE_CAPABILITY@ref"},
		"lrc off":       {req(obj{"lrc": true}), "ENGINE_CAPABILITY@lrc"},
	} {
		if got := codes(def.Validate(tc.raw)); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	// the capability message names the capability
	is := def.Validate(req(repaintOver(100, 400)))
	if is[0].Message != "capability=repaint" {
		t.Errorf("message %q", is[0].Message)
	}
	// verified bounds only tighten
	f := EmbeddedFeatures()
	f.Tasks, f.CanaryCommit = []string{"text2music", "repaint", "extend"}, "c"
	f.Repaint, f.Extend = Bounds{MinS: 5, MaxS: 20}, Bounds{MinS: 5, MaxS: 20}
	e := Engine{Verified: f}
	for name, tc := range map[string]struct {
		raw  []byte
		want string
	}{
		"repaint under min": {req(repaintOver(0, 49)), "range_too_short@repaint_to_ds"},
		"repaint over max":  {req(repaintOver(0, 201)), "range_too_long@repaint_to_ds"},
		"repaint at max":    {req(repaintOver(0, 200)), ""},
		"extend under min":  {req(extendOver(49)), "extend_too_short@extend_ds"},
		"extend over max":   {req(extendOver(201)), "extend_too_long@extend_ds"},
	} {
		if got := codes(e.Validate(tc.raw)); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	// reference window above the verified maximum
	f.Reference, f.ReferenceMaxS = true, 20
	e = Engine{Verified: f}
	if got := codes(e.Validate(req(obj{"ref": obj{"track_id": "r", "clip_sha256": shaB, "from_ds": 0, "to_ds": 250}}))); got != "reference_window@ref" {
		t.Errorf("ref window: %q", got)
	}
}

func TestCheck_ZeroCapsAndStaleVersion(t *testing.T) {
	t.Parallel()
	r, _ := mustParse(t, req(nil))
	if is := Check(r, fullCaps()); len(is) != 0 {
		t.Errorf("valid request: %v", is)
	}
	r.ParamsVersion = "old"
	if got := codes(Check(r, fullCaps())); got != "ENGINE_PARAMS_STALE@engine_params_version" {
		t.Errorf("stale: %q", got)
	}
	// a cap-less Caps never checks duration or bounds, but still refuses disabled capabilities
	bpm := 100
	r2, _ := mustParse(t, req(nil))
	r2.BPM, r2.KeyScale, r2.TimeSignature = &bpm, "A Minor", "4"
	c := fullCaps()
	c.Tempo, c.Key = false, false
	if got := codes(Check(r2, c)); got != "ENGINE_CAPABILITY@bpm,ENGINE_CAPABILITY@bpm,ENGINE_CAPABILITY@keyscale" && got != "ENGINE_CAPABILITY@bpm,ENGINE_CAPABILITY@keyscale" {
		t.Errorf("tempo/key off: %q", got)
	}
}

func TestMaxSecondsMessage(t *testing.T) {
	t.Parallel()
	is := Engine{}.Validate(req(obj{"duration_ds": 4801}))
	if len(is) != 1 || is[0].Message != "capability=max_seconds" {
		t.Errorf("issues: %v", is)
	}
}
