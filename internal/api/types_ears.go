package api

import (
	"github.com/nicolasacchi/metamusic/internal/diction"
	"github.com/nicolasacchi/metamusic/internal/ears"
)

// Types of the M5b Ears operations (contracts C330-C349). Domain shapes live in internal/ears with their schemas; this file
// holds the envelopes.

type tierBOut struct{ Body ears.TierB }

// Diction Lab (C345): the domain shapes live in internal/diction.

type trackDictionIn struct {
	ID     string `path:"id" doc:"Track id"`
	Cursor string `query:"cursor" doc:"Opaque cursor of the previous page's next_cursor"`
}
type dictionLinesOut struct{ Body diction.DictionLines }

type setDictionMarksIn struct {
	ID   string `path:"id" doc:"Track id"`
	Body struct {
		Marks []diction.MarkIn `json:"marks" minItems:"1" maxItems:"200" doc:"Marks on lyric lines; a person's marks override the recogniser, an agent's are kept apart"`
	}
}
type trackMarksOut struct{ Body diction.TrackMarks }

type dictionLexiconIn struct {
	Lang   string         `query:"lang" doc:"Only this language (ISO 639-1, und when unknown)"`
	Risk   optParam[bool] `query:"risk" doc:"true: only the risky words; false: only the others"`
	Q      string         `query:"q" doc:"Substring of the folded word"`
	Limit  int            `query:"limit" minimum:"0" maximum:"200" doc:"Default 50"`
	Cursor string         `query:"cursor"`
}
type lexiconPageOut struct{ Body diction.LexiconPage }

type updateLexiconEntryIn struct {
	Lang string `path:"lang" doc:"ISO 639-1 language, und when unknown"`
	Word string `path:"word" doc:"The word; folded (lower case, no accents) before use"`
	Body struct {
		Status *string `json:"status,omitempty" enum:"active,ignored" doc:"ignored takes the word out of the lint warning"`
		Note   *string `json:"note,omitempty" maxLength:"500" doc:"An empty note removes it"`
	}
}
type lexiconEntryOut struct{ Body diction.LexiconEntry }
