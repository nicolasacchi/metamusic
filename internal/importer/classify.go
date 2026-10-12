package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Classification kinds (C16.1).
const (
	kRecipesJSONL     = "recipes_jsonl"
	kRecipesJSONArray = "recipes_json_array"
	kRequestJSON      = "request_json"
	kCollectionJSON   = "collection_json"
	kReferenceIndex   = "reference_index"
	kScoreABC         = "score_abc"
	kABCSource        = "abc_source"
	kAudioFLAC        = "audio_flac"
	kNotFLAC          = "not_flac"
	kYue2Result       = "yue2_result"
	kArtifact         = "artifact"

	ignPrefix            = "ignored:"
	kIgnTemp             = "ignored:temp"
	kIgnArchive          = "ignored:archive"
	kIgnUnsettled        = "ignored:unsettled"
	kIgnLatent           = "ignored:latent"
	kIgnUnsafeName       = "ignored:unsafe_name"
	kIgnSymlink          = "ignored:symlink"
	kIgnNotRegular       = "ignored:not_regular"
	kIgnPlaylist         = "ignored:reference_playlist"
	kIgnJSON             = "ignored:json"
	kIgnMP3Sidecar       = "ignored:mp3_sidecar"
	kIgnOrphanMP3        = "ignored:orphan_mp3"
	kIgnUnknown          = "ignored:unknown"
	kPendingJSON         = "pending:json"
	kPendingCSV          = "pending:csv"
	kPendingABC          = "pending:abc"
	kPendingFLAC         = "pending:flac"
	kPendingMP3          = "pending:mp3"
	capJSONL       int64 = 64 << 20
	capJSONLLine         = 1 << 20
	capJSON        int64 = 4 << 20
	capCSV         int64 = 16 << 20
	capABC         int64 = 1 << 20
)

var archiveSuffixes = []string{".zip", ".tar", ".tgz", ".tar.gz", ".tar.xz", ".txz", ".tar.bz2", ".tbz2", ".tar.zst", ".7z", ".rar"}
var tempSuffixes = []string{".part", ".partial", ".tmp", ".crdownload", "~"}

var safeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// nameKind classifies a path by name alone (temp, archive, extension).
func nameKind(p string) string {
	lower := strings.ToLower(p)
	for _, comp := range strings.Split(p, "/") {
		if strings.HasPrefix(comp, ".") || comp == "__MACOSX" {
			return kIgnTemp
		}
	}
	base := path.Base(lower)
	if base == "thumbs.db" {
		return kIgnTemp
	}
	for _, s := range tempSuffixes {
		if strings.HasSuffix(lower, s) {
			return kIgnTemp
		}
	}
	for _, s := range archiveSuffixes {
		if strings.HasSuffix(lower, s) {
			return kIgnArchive
		}
	}
	switch path.Ext(lower) {
	case ".jsonl":
		return kRecipesJSONL
	case ".json":
		return kPendingJSON
	case ".csv":
		return kPendingCSV
	case ".abc":
		return kPendingABC
	case ".flac":
		return kPendingFLAC
	case ".mp3":
		return kPendingMP3
	}
	return kIgnUnknown
}

// stemOf is the file name without its extension.
func stemOf(p string) string {
	b := path.Base(p)
	return strings.TrimSuffix(b, path.Ext(b))
}

// storableStem is stemOf for values that get stored: a name that is not valid
// UTF-8 is never stored (C16.1), a neutral hash key stands in for it.
func storableStem(p string) string {
	st := stemOf(p)
	if utf8.ValidString(st) {
		return st
	}
	h := sha256.Sum256([]byte(st))
	return "name:" + hex.EncodeToString(h[:6])
}

// jsonClass is what a *.json file turned out to be.
type jsonClass struct {
	kind     string
	elements []json.RawMessage // recipes_json_array
	coll     *collFile
	warning  string
}

// looksLikeRequest: an object with lyrics and style/tags.
func looksLikeRequest(raw []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, l := m["lyrics"]
	_, s := m["style"]
	_, t := m["tags"]
	return l && (s || t)
}

// classifyJSON decides what a *.json file is (C16.1 table). stem names an
// unnamed collection.
func classifyJSON(data []byte, stem string) jsonClass {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	t := bytes.TrimSpace(data)
	if len(t) == 0 || !json.Valid(t) {
		return jsonClass{kind: kIgnJSON, warning: "invalid_json"}
	}
	switch t[0] {
	case '[':
		var arr []json.RawMessage
		if json.Unmarshal(t, &arr) != nil || len(arr) == 0 {
			return jsonClass{kind: kIgnJSON}
		}
		for _, el := range arr {
			if !looksLikeRequest(el) {
				return jsonClass{kind: kIgnJSON, warning: "json_array_not_requests"}
			}
		}
		return jsonClass{kind: kRecipesJSONArray, elements: arr}
	case '{':
		var m map[string]json.RawMessage
		if json.Unmarshal(t, &m) != nil {
			return jsonClass{kind: kIgnJSON}
		}
		if looksLikeRequest(t) {
			return jsonClass{kind: kRequestJSON}
		}
		if songs, ok := m["songs"]; ok {
			// Only name and variant_ids are ever read (C16.9).
			var items []struct {
				VariantIDs []string `json:"variant_ids"`
			}
			if json.Unmarshal(songs, &items) == nil {
				var ids []string
				found := false
				for _, it := range items {
					if it.VariantIDs != nil {
						found = true
						ids = append(ids, it.VariantIDs...)
					}
				}
				if found {
					name := stem
					var n string
					if raw, ok := m["name"]; ok && json.Unmarshal(raw, &n) == nil && strings.TrimSpace(n) != "" {
						name = n
					}
					return jsonClass{kind: kCollectionJSON, coll: &collFile{name: name, ids: ids}}
				}
			}
		}
		_, p := m["playlist"]
		_, tr := m["tracks"]
		if p && tr {
			return jsonClass{kind: kIgnPlaylist}
		}
	}
	return jsonClass{kind: kIgnJSON}
}

// refRow is one reference-index row; only variant_id, seed, variant and
// variant_type are interpreted, everything else is opaque (C16.9).
type refRow struct {
	line        int
	variantID   string
	seed        string
	hasSeed     bool
	variantType string
	extra       map[string]string
}

// classifyCSV returns the reference rows when data is a reference index.
func classifyCSV(data []byte) (rows []refRow, header []string, isRef bool, err error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	head, err := r.Read()
	if err != nil {
		return nil, nil, false, nil //nolint:nilerr // an empty or unreadable header is simply not an index
	}
	idx := map[string]int{}
	for i, h := range head {
		idx[strings.TrimSpace(h)] = i
	}
	vi, ok := idx["variant_id"]
	if !ok {
		return nil, nil, false, nil
	}
	for {
		rec, rerr := r.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rows, head, true, rerr
		}
		line, _ := r.FieldPos(0)
		row := refRow{line: line, extra: map[string]string{}}
		for i, h := range head {
			h = strings.TrimSpace(h)
			v := ""
			if i < len(rec) {
				v = rec[i]
			}
			switch h {
			case "variant_id":
				row.variantID = strings.TrimSpace(v)
			case "seed":
				row.seed, row.hasSeed = strings.TrimSpace(v), true
			case "variant_type":
				row.variantType = strings.TrimSpace(v)
				row.extra[h] = v
			default:
				row.extra[h] = v
			}
		}
		_ = vi
		if row.variantID == "" {
			continue
		}
		rows = append(rows, row)
	}
	return rows, head, true, nil
}
