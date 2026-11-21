package diction

// Wire types of the Diction Lab (C344, C345). Names carry the Diction or Lexicon prefix because every schema lives in one
// OpenAPI namespace.

// Thresholds are the CER cut-offs of the line classes: a line is clear up to Clear, mixed up to Mixed, else unclear. Only the
// calibration PR changes them (C344.2).
type Thresholds struct {
	Clear float64 `json:"clear"`
	Mixed float64 `json:"mixed"`
}

// DefaultThresholds are the uncalibrated cut-offs.
var DefaultThresholds = Thresholds{Clear: 0.15, Mixed: 0.35}

// Line bases and line classes.
const (
	BasisASR          = "asr"
	BasisProportional = "proportional"
	BasisNone         = "none"

	ClassClear   = "clear"
	ClassMixed   = "mixed"
	ClassUnclear = "unclear"
	ClassUnknown = "unknown"

	SourceHuman  = "human"
	SourceCritic = "critic"
	SourceASR    = "asr"

	MarkClear   = "clear"
	MarkUnclear = "unclear"
	MarkNone    = "none"
)

// DictionWord is one word of a lyric line with what the recogniser made of it.
type DictionWord struct {
	I      int      `json:"i" doc:"Index of the word in the line"`
	Text   string   `json:"text" doc:"The word as written in the lyrics"`
	StartS *float64 `json:"start_s" doc:"Null when the word has no measured time"`
	EndS   *float64 `json:"end_s"`
	Timing *string  `json:"timing" enum:"heard,interpolated" doc:"heard: from the recogniser; interpolated: between two heard neighbours; null: not timed"`
	Match  string   `json:"match" enum:"exact,close,miss,dropped"`
	Heard  string   `json:"heard" doc:"What was heard instead (folded); empty when exact or dropped"`
}

// DictionMark is the marks of one line, one per source; null when the source never marked it.
type DictionMark struct {
	Human  *string `json:"human" enum:"clear,unclear,none"`
	Critic *string `json:"critic" enum:"clear,unclear,none"`
}

// DictionLine is one lyric line of the karaoke view.
type DictionLine struct {
	SectionIdx  int           `json:"section_idx"`
	LineIdx     int           `json:"line_idx"`
	Text        string        `json:"text"`
	StartS      *float64      `json:"start_s"`
	EndS        *float64      `json:"end_s"`
	Timing      *string       `json:"timing" enum:"heard,estimated" doc:"heard: from recognised words; estimated: the section's time split equally among its lines; null: not timed"`
	CER         *float64      `json:"cer" doc:"Character error rate of the line against the recogniser; null without a recogniser"`
	Class       string        `json:"class" enum:"clear,mixed,unclear,unknown"`
	ClassSource string        `json:"class_source" enum:"human,asr"`
	Mark        DictionMark   `json:"mark"`
	Words       []DictionWord `json:"words" nullable:"false" doc:"Empty for an estimated line"`
}

// HeardCount is one thing the recogniser heard instead of a word, with how often.
type HeardCount struct {
	Text string `json:"text"`
	N    int    `json:"n"`
}

// LexiconHit is a mangled word of this take that the personal lexicon knows.
type LexiconHit struct {
	Word         string       `json:"word"`
	TypicalHeard []HeardCount `json:"typical_heard" nullable:"false"`
	Attempts     int          `json:"attempts"`
	Mangled      int          `json:"mangled"`
}

// DictionLines is the get_track_diction answer (C345.1): one page of lines with the summary of the take.
type DictionLines struct {
	TrackID     string        `json:"track_id"`
	Basis       string        `json:"basis" enum:"asr,proportional"`
	Lang        string        `json:"lang"`
	Thresholds  Thresholds    `json:"thresholds"`
	Lines       []DictionLine `json:"lines" nullable:"false"`
	LexiconHits []LexiconHit  `json:"lexicon_hits" nullable:"false" doc:"At most 10, most mangled first; on the first page only"`
	Truncated   bool          `json:"truncated" doc:"True when next_cursor continues the list"`
	NextCursor  *string       `json:"next_cursor"`
}

// LexiconExample is one occurrence of a mangled word.
type LexiconExample struct {
	TrackID  string  `json:"track_id"`
	AtSecond float64 `json:"at_second"`
	Heard    string  `json:"heard"`
}

// LexiconEntry is one word of the personal lexicon of mangled words (computed, never stored).
type LexiconEntry struct {
	Lang         string           `json:"lang"`
	Word         string           `json:"word" doc:"Folded: lower case, no accents"`
	Attempts     int              `json:"attempts" doc:"Times the word was sung in a take with a listening check"`
	Mangled      int              `json:"mangled" doc:"Times it was heard as another word"`
	Dropped      int              `json:"dropped" doc:"Times it was not heard"`
	Rate         float64          `json:"rate" doc:"mangled / attempts, three decimals"`
	TypicalHeard []HeardCount     `json:"typical_heard" nullable:"false" doc:"At most 3"`
	Tracks       int              `json:"tracks"`
	LastSeenAt   string           `json:"last_seen_at"`
	Status       string           `json:"status" enum:"active,ignored"`
	Note         *string          `json:"note"`
	Risk         bool             `json:"risk" doc:"attempts >= 3, rate >= 0.5 and status active: warned about in lint"`
	Examples     []LexiconExample `json:"examples" nullable:"false" doc:"At most 3"`
}

// Calibration is the progress of the calibration set: human marks on lines that have a recogniser CER.
type Calibration struct {
	LinesMarked  int    `json:"lines_marked"`
	TracksMarked int    `json:"tracks_marked"`
	Clear        int    `json:"clear"`
	Unclear      int    `json:"unclear"`
	Lang         string `json:"lang" doc:"The language asked for, or all"`
}

// LexiconPage is the get_diction_lexicon answer.
type LexiconPage struct {
	Entries     []LexiconEntry `json:"entries" nullable:"false"`
	Total       int            `json:"total" doc:"Entries matching the filters"`
	Calibration Calibration    `json:"calibration"`
	NextCursor  *string        `json:"next_cursor"`
}

// MarkRow is one stored mark of a take.
type MarkRow struct {
	SectionIdx int    `json:"section_idx"`
	LineIdx    int    `json:"line_idx"`
	Source     string `json:"source" enum:"human,critic"`
	Mark       string `json:"mark" enum:"clear,unclear,none"`
	UpdatedAt  string `json:"updated_at"`
}

// TrackMarks is the set_diction_marks answer: every mark of the take.
type TrackMarks struct {
	TrackID string    `json:"track_id"`
	Marks   []MarkRow `json:"marks" nullable:"false"`
}

// MarkIn is one requested mark.
type MarkIn struct {
	SectionIdx int    `json:"section_idx" minimum:"0"`
	LineIdx    int    `json:"line_idx" minimum:"0"`
	Mark       string `json:"mark" enum:"clear,unclear,none" doc:"none withdraws the mark"`
}
