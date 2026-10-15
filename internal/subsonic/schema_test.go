package subsonic_test

import (
	"encoding/json"
	"flag"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

// fixtureRig is a rig over the M4a fixture library (five tracks, deterministic ids and timestamps) with files, plus a
// read-write client and the resolved placeholders.
func fixtureRig(t *testing.T) (*servertest.Rig, *subsonictest.Client, subsonictest.PlaceholderSet) {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, M4a: true, Files: true})
	c := r.Subsonic(t, subsonictest.ClientOpts{Name: "Schema Client", Username: "schemaclient", Write: true})
	c.Clock = r.Clock
	ps := subsonictest.NewPlaceholders(t, r.Store)
	ps["user"] = c.Username
	ps["data_dir"] = r.DataDir
	return r, c, ps
}

// endpointCall is one request of the per-endpoint sweep. Binary endpoints (stream, download, getCoverArt) have no
// envelope and are covered by the client fixtures instead.
type endpointCall struct {
	name   string // endpoint (and golden file) name
	params url.Values
	apiKey bool
	bind   func(resp *subsonictest.Response, ps subsonictest.PlaceholderSet) // stores ids for later calls
}

func qv(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v
}

// sweep lists every implemented envelope endpoint in an order that works on the fixture library: reads first, then
// the writes (the playlist is created, read, updated and deleted).
func sweep(ps subsonictest.PlaceholderSet) []endpointCall {
	song, album := ps["song:1"], ps["album:1"]
	return []endpointCall{
		{name: "ping"},
		{name: "getLicense"},
		{name: "getOpenSubsonicExtensions"},
		{name: "tokenInfo", apiKey: true},
		{name: "getUser", params: qv("username", ps["user"])},
		{name: "getMusicFolders"},
		{name: "getIndexes"},
		{name: "getMusicDirectory", params: qv("id", album)},
		{name: "getArtists"},
		{name: "getArtist", params: qv("id", "ar-metamusic")},
		{name: "getAlbum", params: qv("id", album)},
		{name: "getSong", params: qv("id", song)},
		{name: "getGenres"},
		{name: "getAlbumList", params: qv("type", "newest", "size", "50")},
		{name: "getAlbumList2", params: qv("type", "newest", "size", "50")},
		{name: "getRandomSongs", params: qv("size", "5", "genre", "dream pop")},
		{name: "getStarred"},
		{name: "getStarred2"},
		{name: "getSongsByGenre", params: qv("genre", "rock")},
		{name: "getSimilarSongs", params: qv("id", song)},
		{name: "getSimilarSongs2", params: qv("id", song)},
		{name: "getTopSongs", params: qv("artist", "metamusic")},
		{name: "getNowPlaying"},
		{name: "getScanStatus"},
		{name: "getArtistInfo", params: qv("id", "ar-metamusic")},
		{name: "getArtistInfo2", params: qv("id", "ar-metamusic")},
		{name: "getAlbumInfo", params: qv("id", album)},
		{name: "getAlbumInfo2", params: qv("id", album)},
		{name: "getLyrics", params: qv("artist", "metamusic", "title", "none")},
		{name: "getLyricsBySongId", params: qv("id", song)},
		{name: "getBookmarks"},
		{name: "getPlayQueue"},
		{name: "search2", params: qv("query", "")},
		{name: "search3", params: qv("query", "")},
		{name: "getPlaylists"},
		{name: "createPlaylist", params: qv("name", "Sweep list", "songId", song), bind: func(r *subsonictest.Response, ps subsonictest.PlaceholderSet) {
			if pl, ok := r.Doc["playlist"].(map[string]any); ok {
				ps["playlist"], _ = pl["id"].(string)
			}
		}},
		{name: "getPlaylist", params: qv("id", "{{playlist}}")},
		{name: "updatePlaylist", params: qv("playlistId", "{{playlist}}", "name", "Sweep list renamed")},
		{name: "deletePlaylist", params: qv("id", "{{playlist}}")},
		{name: "createBookmark", params: qv("id", song, "position", "42000", "comment", "sweep bookmark")},
		{name: "deleteBookmark", params: qv("id", song)},
		{name: "savePlayQueue", params: qv("id", song, "current", song, "position", "1000")},
		{name: "star", params: qv("id", song)},
		{name: "unstar", params: qv("id", song)},
		{name: "setRating", params: qv("id", song, "rating", "4")},
		{name: "scrobble", params: qv("id", song, "submission", "false")},
		{name: "reportPlayback", params: qv("mediaId", song, "mediaType", "song", "state", "starting", "positionMs", "0")},
	}
}

// runSweep calls every endpoint on c in order, expanding {{x}} in parameter values from ps, and passes each reply to
// visit. The reply must be a protocol success.
func runSweep(t *testing.T, c *subsonictest.Client, ps subsonictest.PlaceholderSet, o subsonictest.CallOpts, visit func(name string, r *subsonictest.Response)) {
	t.Helper()
	for _, ec := range sweep(ps) {
		params := url.Values{}
		for k, vs := range ec.params {
			for _, v := range vs {
				if strings.HasPrefix(v, "{{") {
					v = ps[strings.Trim(v, "{}")]
				}
				params.Add(k, v)
			}
		}
		co := o
		co.APIKey = ec.apiKey
		resp := c.Call(t, ec.name, params, co)
		if resp.HTTP != 200 || resp.Status != "ok" {
			t.Fatalf("%s: http %d status %q code %d: %.300s", ec.name, resp.HTTP, resp.Status, resp.Code, resp.Body)
		}
		if ec.bind != nil {
			ec.bind(resp, ps)
		}
		visit(ec.name, resp)
	}
}

func updateGoldens() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

const specPath = "../../third_party/opensubsonic/openapi.json"

// openapiSchemas converts the OpenAPI 3.0 document of the vendored description for jsonschema-go: every component
// schema becomes a $defs entry, references are rewritten and `nullable: true` becomes a type union with null.
type openapiDoc struct {
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas   map[string]any `json:"schemas"`
		Responses map[string]struct {
			Content map[string]struct {
				Schema map[string]any `json:"schema"`
			} `json:"content"`
		} `json:"responses"`
	} `json:"components"`
}

func convert(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if k == "$ref" {
				if s, ok := val.(string); ok {
					out[k] = strings.Replace(s, "#/components/schemas/", "#/$defs/", 1)
					continue
				}
			}
			if k == "nullable" || k == "example" || k == "externalDocs" || k == "xml" {
				continue
			}
			out[k] = convert(val)
		}
		if n, _ := x["nullable"].(bool); n {
			if t, ok := out["type"].(string); ok {
				out["type"] = []any{t, "null"}
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = convert(val)
		}
		return out
	}
	return v
}

// responseSchemaRef returns the $ref of the 200 JSON response of /rest/<endpoint> (GET), "" when it has none.
func (d openapiDoc) responseSchemaRef(t *testing.T, endpoint string) string {
	t.Helper()
	op, ok := d.Paths["/rest/"+endpoint]["get"]
	if !ok {
		return ""
	}
	var o struct {
		Responses map[string]struct {
			Ref     string `json:"$ref"`
			Content map[string]struct {
				Schema map[string]any `json:"schema"`
			} `json:"content"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(op, &o); err != nil {
		t.Fatal(err)
	}
	r := o.Responses["200"]
	if r.Ref != "" {
		name := strings.TrimPrefix(r.Ref, "#/components/responses/")
		if s := d.Components.Responses[name].Content["application/json"].Schema["$ref"]; s != nil {
			return s.(string)
		}
		return ""
	}
	if s := r.Content["application/json"].Schema["$ref"]; s != nil {
		return s.(string)
	}
	return ""
}

// TestSubsonic_JSONMatchesSchema: every implemented envelope endpoint is called on the fixture library with f=json
// and the whole answer is validated against the vendored OpenSubsonic description.
func TestSubsonic_JSONMatchesSchema(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc openapiDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	defs := convert(doc.Components.Schemas).(map[string]any)
	_, c, ps := fixtureRig(t)
	runSweep(t, c, ps, subsonictest.CallOpts{JSON: true}, func(name string, r *subsonictest.Response) {
		ref := doc.responseSchemaRef(t, name)
		if ref == "" {
			t.Errorf("%s: the vendored description has no JSON response schema", name)
			return
		}
		root := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema",
			"$ref": strings.Replace(ref, "#/components/schemas/", "#/$defs/", 1), "$defs": reachable(defs, strings.TrimPrefix(ref, "#/components/schemas/"))}
		rb, _ := json.Marshal(root)
		var sch jsonschema.Schema
		if err := json.Unmarshal(rb, &sch); err != nil {
			t.Fatalf("%s: converting the schema: %v", name, err)
		}
		rs, err := sch.Resolve(nil)
		if err != nil {
			t.Fatalf("%s: resolving the schema: %v", name, err)
		}
		var env any
		if err := json.Unmarshal(r.Body, &env); err != nil {
			t.Fatalf("%s: not JSON: %v", name, err)
		}
		if err := rs.Validate(env); err != nil {
			t.Errorf("%s does not match %s: %v\n%.600s", name, ref, err, r.Body)
		}
		// negative control: the validator must notice a payload element that was dropped or corrupted
		if m, ok := env.(map[string]any); ok && name == "getAlbum" {
			resp := m["subsonic-response"].(map[string]any)
			delete(resp, "album")
			if rs.Validate(env) == nil {
				t.Errorf("%s: the schema accepted an answer without its album element", name)
			}
			resp["album"] = map[string]any{"id": 7}
			if rs.Validate(env) == nil {
				t.Errorf("%s: the schema accepted a malformed album", name)
			}
		}
	})
}

// reachable returns the $defs entries that the schema named root references, transitively (resolving only those keeps
// the sweep fast under -race).
func reachable(defs map[string]any, root string) map[string]any {
	out := map[string]any{}
	var visit func(name string)
	var walk func(v any)
	visit = func(name string) {
		if _, done := out[name]; done {
			return
		}
		d, ok := defs[name]
		if !ok {
			return
		}
		out[name] = d
		walk(d)
	}
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if s, ok := val.(string); ok && k == "$ref" {
					visit(strings.TrimPrefix(s, "#/$defs/"))
					continue
				}
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	visit(root)
	return out
}
