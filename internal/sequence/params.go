package sequence

import (
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/sequencer"
)

// Machine codes of the sequencing errors (C138.1, C138.2); every one is a 422 except NOT_FOUND.
const (
	CodeTooSmall         = "SEQUENCE_TOO_SMALL"
	CodeNoFeatures       = "SEQUENCE_NO_FEATURES"
	CodeUnknownStrategy  = "UNKNOWN_STRATEGY"
	CodeEndpointNotInSet = "SEQUENCE_ENDPOINT_NOT_IN_SET"
	CodeEndpointsSame    = "SEQUENCE_ENDPOINTS_CONFLICT"
	CodeTooManySongs     = "TOO_MANY_SONGS"
	CodeUnknownSong      = "UNKNOWN_SONG"
	CodeBadSource        = "SEQUENCE_SOURCE"
)

// chunk keeps an IN list well under SQLite's variable limit.
const chunk = 500

// batchLimit is the most Songs one sequence holds.
const batchLimit = sequencer.MaxSongs

func tooSmall(n int) error {
	return problem.E(422, CodeTooSmall, "a sequence needs at least two songs", map[string]any{"songs": n, "min": 2})
}

func tooMany(n int) error {
	return problem.E(422, CodeTooManySongs, fmt.Sprintf("a sequence holds at most %d songs", batchLimit), map[string]any{"count": n, "limit": batchLimit})
}

func unknownStrategy(s string) error {
	names := make([]string, len(sequencer.Strategies))
	for i, st := range sequencer.Strategies {
		names[i] = string(st)
	}
	return problem.E(422, CodeUnknownStrategy, "unknown strategy", map[string]any{"strategy": s, "allowed": names})
}

func endpointNotInSet(field, song string) error {
	return problem.E(422, CodeEndpointNotInSet, "the endpoint is not one of the songs", map[string]any{"field": field, "song_id": song})
}
