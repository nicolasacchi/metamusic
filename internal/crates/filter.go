package crates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

func unprocessable(code, msg string, details map[string]any) error {
	return problem.E(http.StatusUnprocessableEntity, code, msg, details)
}

// fieldErr is one entry of INVALID_FILTER's details.fields.
type fieldErr struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ParseFilter validates a saved filter of the subject against the vocabulary (C125.2): unknown key (including the
// non-saved parameters and crate_id), wrong type, out-of-range value, empty array, or more than MaxFilterBytes are refused
// with 422 FILTER_TOO_LARGE, UNKNOWN_FILTER_KEY (details.key) or INVALID_FILTER (details.fields).
func ParseFilter(subject Subject, raw json.RawMessage) (Filter, error) {
	if subject != SubjectSong && subject != SubjectTake {
		return Filter{}, unprocessable("VALIDATION_FAILED", "subject must be song or take", map[string]any{"field": "subject"})
	}
	if len(raw) > MaxFilterBytes {
		return Filter{}, unprocessable("FILTER_TOO_LARGE", fmt.Sprintf("a saved filter is at most %d bytes", MaxFilterBytes), map[string]any{"max_bytes": MaxFilterBytes})
	}
	var obj map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&obj); err != nil || obj == nil || dec.More() {
		return Filter{}, unprocessable("INVALID_FILTER", "a filter is a JSON object", map[string]any{"fields": []fieldErr{{"filter", "must be a JSON object"}}})
	}
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	slices.Sort(names)
	var errs []fieldErr
	vals := map[string]any{}
	for _, name := range names {
		key, ok := Lookup(string(subject), name)
		if !ok {
			return Filter{}, unprocessable("UNKNOWN_FILTER_KEY", "not a key of this subject's filter vocabulary", map[string]any{"key": name})
		}
		v, msg := decodeValue(key, obj[name])
		if msg != "" {
			errs = append(errs, fieldErr{name, msg})
			continue
		}
		vals[name] = v
	}
	if len(errs) > 0 {
		return Filter{}, unprocessable("INVALID_FILTER", "the filter has invalid values", map[string]any{"fields": errs})
	}
	f := Filter{subject: subject}
	var err error
	if subject == SubjectSong {
		f.songs, err = songFilter(vals)
		if err == nil {
			err = f.songs.ValidateL2()
		}
	} else {
		f.tracks, err = trackParams(vals)
	}
	if err != nil {
		return Filter{}, asInvalid(err)
	}
	return f, nil
}

// asInvalid turns a validation error of a mapped value into INVALID_FILTER.
func asInvalid(err error) error {
	var pe *problem.Error
	var ve *library.ValidationError
	if errors.As(err, &pe) {
		field, _ := pe.Details["field"].(string)
		return unprocessable("INVALID_FILTER", "the filter has invalid values", map[string]any{"fields": []fieldErr{{field, pe.Message}}})
	}
	if errors.As(err, &ve) {
		return unprocessable("INVALID_FILTER", "the filter has invalid values", map[string]any{"fields": []fieldErr{{ve.Field, ve.Msg}}})
	}
	return err
}

// decodeValue type- and range-checks one value against its key and returns it as bool, string, []string, int or float64.
func decodeValue(k Key, raw json.RawMessage) (any, string) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nil, "null is not a value; leave the key out"
	}
	switch k.Kind {
	case KindBool:
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return nil, "want true or false"
		}
		return b, ""
	case KindString, KindInstant:
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, "want a string"
		}
		if strings.TrimSpace(s) == "" || len(s) > 200 {
			return nil, "want 1 to 200 characters"
		}
		if k.Kind == KindInstant && !validInstant(s) {
			return nil, "want an RFC 3339 date or date-time"
		}
		return s, ""
	case KindInt, KindNumber:
		var f float64
		if json.Unmarshal(raw, &f) != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, "want a number"
		}
		if k.Kind == KindInt && f != math.Trunc(f) {
			return nil, "want an integer"
		}
		if k.Ranged && (f < k.Min || f > k.Max) {
			return nil, fmt.Sprintf("want a value from %g to %g", k.Min, k.Max)
		}
		if k.Kind == KindInt {
			return int(f), ""
		}
		return f, ""
	case KindStrings:
		var ss []string
		if json.Unmarshal(raw, &ss) != nil {
			return nil, "want an array of strings"
		}
		if len(ss) == 0 {
			return nil, "an empty array is refused; leave the key out"
		}
		if k.MaxItems > 0 && len(ss) > k.MaxItems {
			return nil, fmt.Sprintf("at most %d items", k.MaxItems)
		}
		for _, s := range ss {
			if strings.TrimSpace(s) == "" || len(s) > 200 {
				return nil, "items are 1 to 200 characters"
			}
			if len(k.Enum) > 0 && !slices.Contains(k.Enum, s) {
				return nil, fmt.Sprintf("%q is not one of %s", s, strings.Join(k.Enum, ", "))
			}
		}
		return ss, ""
	}
	return nil, "unsupported key kind"
}

func validInstant(s string) bool {
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func ptr[T any](v T) *T { return &v }

// songFilter maps validated song values onto the filter of list_songs.
func songFilter(v map[string]any) (songs.ListFilter, error) {
	var f songs.ListFilter
	for name, x := range v {
		switch name {
		case "muse_id":
			f.MuseID = x.(string)
		case "no_muse":
			f.NoMuse = x.(bool)
		case "angle":
			f.Angle = x.(string)
		case "state":
			for _, s := range x.([]string) {
				f.States = append(f.States, songs.State(s))
			}
		case "queued":
			f.Queued = ptr(x.(bool))
		case "session_id":
			f.SessionID = x.(string)
		case "sound":
			m, err := songs.ParseSoundFilters(x.([]string))
			if err != nil {
				return f, asSound(err)
			}
			f.Sounds = m
		case "tempo_mismatch":
			f.TempoMismatch = ptr(x.(bool))
		case "has_pick":
			f.HasPick = ptr(x.(bool))
		case "has_open_proposals":
			f.HasOpenProposals = ptr(x.(bool))
		case "q":
			f.Q = x.(string)
		case "pick_decided":
			f.PickDecided = ptr(x.(bool))
		case "unheard":
			f.Unheard = ptr(x.(bool))
		case "min_duration_s":
			f.MinDurationS = ptr(x.(float64))
		case "max_duration_s":
			f.MaxDurationS = ptr(x.(float64))
		case "min_stars":
			f.MinStars = ptr(x.(int))
		case "unrated":
			f.Unrated = ptr(x.(bool))
		case "tag":
			f.Tags = x.([]string)
		case "lossy":
			f.Lossy = ptr(x.(bool))
		case "recipe_known":
			f.RecipeKnown = ptr(x.(bool))
		case "in_record":
			f.InRecord = ptr(x.(bool))
		}
	}
	return f, nil
}

func asSound(err error) error {
	return problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "sound selections are facet:value with a known facet", map[string]any{"field": "sound"})
}

// trackParams maps validated take values onto the parameters of search_tracks.
func trackParams(v map[string]any) (library.SearchParams, error) {
	var p library.SearchParams
	for name, x := range v {
		switch name {
		case "q":
			p.Q = x.(string)
		case "status":
			p.Status = x.([]string)
		case "style":
			p.Style = x.([]string)
		case "min_duration_s":
			p.MinDurationS = ptr(x.(float64))
		case "max_duration_s":
			p.MaxDurationS = ptr(x.(float64))
		case "recorded_from":
			p.RecordedFrom = x.(string)
		case "recorded_to":
			p.RecordedTo = x.(string)
		case "has_score":
			p.HasScore = ptr(x.(bool))
		case "truncated":
			p.Truncated = ptr(x.(bool))
		case "min_stars":
			p.MinStars = ptr(x.(int))
		case "unrated":
			p.Unrated = x.(bool)
		case "tag":
			p.Tags = x.([]string)
		case "has_open_comments":
			p.HasOpenComments = ptr(x.(bool))
		case "recipe_known":
			p.RecipeKnown = ptr(x.(bool))
		case "lossy":
			p.Lossy = ptr(x.(bool))
		case "is_pick":
			p.IsPick = ptr(x.(bool))
		case "session_id":
			p.SessionID = x.(string)
		case "unheard":
			p.Unheard = ptr(x.(bool))
		case "in_record":
			p.InRecord = ptr(x.(bool))
		case "sound":
			if _, err := songs.ParseSoundFilters(x.([]string)); err != nil {
				return p, asSound(err)
			}
			p.Sounds = x.([]string)
		}
	}
	if p.MinStars != nil && p.Unrated {
		return p, problem.E(http.StatusUnprocessableEntity, "VALIDATION_FAILED", "cannot be combined with min_stars", map[string]any{"field": "unrated"})
	}
	return p, nil
}

// Songs maps a parsed Song filter onto the filter struct of list_songs.
func (f Filter) Songs() songs.ListFilter { return f.songs }

// Tracks maps a parsed Take filter onto the search parameters of search_tracks.
func (f Filter) Tracks() library.SearchParams { return f.tracks }
