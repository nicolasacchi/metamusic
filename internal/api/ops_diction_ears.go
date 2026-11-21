package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/diction"
)

// registerDiction registers the Italian Diction Lab operations (M5b C345.1-.3). All are generic over MCP (D-EARS-17). The marks and
// the lexicon overrides are the Owner's own judgement (D-EARS-14, D-EARS-18): scope write and OwnerOnly.
func registerDiction(a huma.API, d Deps) {
	Register(a, huma.Operation{
		OperationID: "get_track_diction", Method: "GET", Path: "/api/tracks/{id}/diction",
		Summary: "Get the karaoke lines of a take",
		Description: "The lyric lines of a take with word-level intelligibility: per line the time, the recogniser's character error rate, a class " +
			"(clear, mixed, unclear) and the words with what was heard; a human mark replaces the class. basis asr comes from a listening check " +
			"(word times are measured); basis proportional splits a timed section equally among its lines (times are estimates, words untimed). " +
			"Pages of at most 60 000 bytes: follow next_cursor. 409 ANALYSIS_UNAVAILABLE when the take has neither.",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/tracks/:id"},
		func(ctx context.Context, in *trackDictionIn) (*dictionLinesOut, error) {
			if d.Diction == nil {
				return nil, unavailable("diction")
			}
			if err := d.visible(ctx, in.ID); err != nil {
				return nil, err
			}
			v, err := d.Diction.Lines(ctx, in.ID, in.Cursor)
			if err != nil {
				return nil, m2bErr(err, "EARS-D1")
			}
			return &dictionLinesOut{Body: v}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "set_diction_marks", Method: "PUT", Path: "/api/tracks/{id}/diction/marks",
		Summary: "Mark lyric lines as clear or unclear",
		Description: "Says what a listener heard on up to 200 lines: clear, unclear, or none to withdraw a mark. The Owner's marks override the " +
			"recogniser and feed the lexicon and the calibration set; an agent's marks are stored apart and affect neither. Answers every mark of the take.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/tracks/:id", OwnerOnly: true},
		func(ctx context.Context, in *setDictionMarksIn) (*trackMarksOut, error) {
			if d.Diction == nil {
				return nil, unavailable("diction")
			}
			if err := d.visible(ctx, in.ID); err != nil {
				return nil, err
			}
			v, err := d.Diction.SetMarks(ctx, auth.FromContext(ctx), in.ID, in.Body.Marks)
			if err != nil {
				return nil, m2bErr(err, "EARS-D1")
			}
			return &trackMarksOut{Body: v}, nil
		})

	Register(a, huma.Operation{
		OperationID: "get_diction_lexicon", Method: "GET", Path: "/api/diction/lexicon",
		Summary: "List the personal lexicon of mangled words",
		Description: "Words the recogniser keeps mishearing in sung Italian, from every take with a listening check: attempts, how often mangled or " +
			"dropped, what was heard instead, and whether the word is a risk (3 or more attempts, at least half mangled, not ignored). " +
			"Also the progress of the calibration set (human marks on lines with a recogniser result).",
	}, Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "generic", UI: "/diction"},
		func(ctx context.Context, in *dictionLexiconIn) (*lexiconPageOut, error) {
			if d.Diction == nil {
				return nil, unavailable("diction")
			}
			v, err := d.Diction.Lexicon(ctx, diction.LexiconQuery{Lang: in.Lang, Risk: in.Risk.ptr(), Q: in.Q, Limit: in.Limit, Cursor: in.Cursor})
			if err != nil {
				return nil, m2bErr(err, "EARS-D1")
			}
			return &lexiconPageOut{Body: v}, nil
		})

	Register(a, withBody(huma.Operation{
		OperationID: "update_lexicon_entry", Method: "PATCH", Path: "/api/diction/lexicon/{lang}/{word}",
		Summary: "Ignore or restore a lexicon word, or annotate it",
		Description: "Sets the status (active or ignored) and/or the note of a word. An ignored word is no longer warned about. The word must be in " +
			"the lexicon or be at least three letters. Answers the entry.",
	}, bodySmall), Meta{Scope: auth.ScopeWrite, Cost: "free", MCP: "generic", UI: "/diction", OwnerOnly: true},
		func(ctx context.Context, in *updateLexiconEntryIn) (*lexiconEntryOut, error) {
			if d.Diction == nil {
				return nil, unavailable("diction")
			}
			v, err := d.Diction.UpdateEntry(ctx, auth.FromContext(ctx), in.Lang, in.Word, diction.EntryPatch{Status: in.Body.Status, Note: in.Body.Note})
			if err != nil {
				return nil, m2bErr(err, "EARS-D1")
			}
			return &lexiconEntryOut{Body: v}, nil
		})
}
