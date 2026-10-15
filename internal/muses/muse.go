// Package muses is the Muse model (C89): an inspiration a Song can come from, either a reference (title + optional
// artist, unique by the normalised key) or a brief (short title + free text). Muse text is the privacy-sensitive part
// of the library (D-L1-6, C99): nothing outside this package, board and the migrations may name the muses tables.
package muses

import "github.com/nicolasacchi/metamusic/internal/store"

// Env is the Reconciler environment (C87).
type Env = store.Env

// Kind of a Muse.
type Kind string

// The two Muse kinds.
const (
	KindReference Kind = "reference"
	KindBrief     Kind = "brief"
)

// Sources of a Muse row.
const (
	SourceRecipeRefs = "recipe_refs"
	SourceHuman      = "human"
	SourceAgent      = "agent"
)

// Limits of C89.1.
const (
	MaxTitleRunes  = 200
	MaxArtistRunes = 200
	MaxBriefRunes  = 2000
)

// Muse is one row of muses.
type Muse struct {
	ID, Kind, Title                                    string
	Artist, Brief                                      *string
	Source, CreatedBy, CreatedAt, UpdatedBy, UpdatedAt string
	Revision                                           int
	HiddenAt                                           *string
}

// MuseRef is the only form in which other packages read Muse text (C89 Ref).
type MuseRef struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Title  string  `json:"title"`
	Artist *string `json:"artist"`
}

// NewMuse is the input of Create.
type NewMuse struct {
	Kind          Kind
	Title         string
	Artist, Brief *string
}

// Patch is the input of Update: reference Muses change title/artist, briefs title/brief.
type Patch struct {
	Revision             int
	Title, Artist, Brief *string
	Hidden               *bool
}

// ListFilter selects Muses.
type ListFilter struct {
	Q             string
	Kind          Kind
	IncludeHidden bool
	Limit         int
	Cursor        string
}

// Summary is a list row; the Song counts come from songview.MuseSummaries.
type Summary struct{ Muse }

// Page is one page of Muses.
type Page struct {
	Items      []Summary
	NextCursor *string
	Total      int
}

// AssignResult is the outcome of AssignSongs.
type AssignResult struct{ Assigned, Unchanged int }
