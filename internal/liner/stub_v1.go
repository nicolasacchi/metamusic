package liner

// STUB(L2-V1): the frozen signatures of the pure renderer (C124.1). Lane V1 deletes this file and adds liner.go and
// render.go with their goldens; the signatures do not change.

// Build renders in. It is pure: the same input gives the same output.
func Build(in Input) Liner { return Liner{Title: in.Title} }

// Markdown renders the notes as Markdown of at most 8 KB; a track list that does not fit is cut at a whole line with a notice.
func (l Liner) Markdown() string { return "" }

// Description is the tape DESCRIPTION: the summary and the notices, at most 1 000 characters.
func (l Liner) Description() string { return "" }
