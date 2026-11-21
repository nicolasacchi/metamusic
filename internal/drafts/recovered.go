package drafts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// OriginRecovered is the origin of a draft made by Recover (C343.3).
const OriginRecovered = "recovered"

// RecoveredDraft is the input of CreateRecovered: a lyric rebuilt from the stored listening check of an imported reference, the
// style, and the draft_recoveries fields. It is plain data so that internal/ears and internal/drafts need not import each other.
type RecoveredDraft struct {
	ReferenceTrackID string
	ReferenceTitle   string // seeds the request id slug and, without Title, the label
	Title            string // the caller's label, cut to the label limit; empty uses ReferenceTitle
	Style            string
	StyleSource      string // caller | placeholder
	Lyrics           string
	Cot              string // melody | full | off; empty is melody
	Rationale        string // 1..1000 characters or empty
	AnalysisSHA256   string
	Language         string // two or three letters, or empty
	Words            int
	Confidence       float64
}

// recoveredSeed is the FNV-1a hash of the draft id, mod 2^31 (C343.3): the same draft id always names the same seed.
func recoveredSeed(draftID string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(draftID))
	return int64(h.Sum32() % (1 << 31))
}

// recoveredRequestID is "rec-<slug>-<6 hex>": the slug of the reference's title, the hex from the draft id.
func recoveredRequestID(title, draftID string) string {
	slug := yue2.Slug(title)
	if slug == "" {
		slug = "reference"
	}
	sum := sha256.Sum256([]byte(draftID))
	return "rec-" + slug + "-" + hex.EncodeToString(sum[:3])
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// CreateRecovered stores a new draft recovered from a reference (C343.3): origin recovered, no parent (a reference can never be a
// mother), the request {id, style, lyrics, cot, seed}, and the draft_recoveries row in the same transaction. Each call makes a new
// draft. The usual lint warnings decorate the answer. It spends nothing: a render of the draft goes through the ordinary estimate.
func (s *Service) CreateRecovered(ctx context.Context, p auth.Principal, in RecoveredDraft) (library.DraftDetail, error) {
	if in.StyleSource != "caller" && in.StyleSource != "placeholder" {
		return library.DraftDetail{}, invalid("style", "style_source must be caller or placeholder")
	}
	if in.Cot == "" {
		in.Cot = "melody"
	}
	id := s.newID()
	raw, err := json.Marshal(map[string]any{"id": recoveredRequestID(in.ReferenceTitle, id), "style": in.Style, "lyrics": in.Lyrics, "cot": in.Cot,
		"seed": recoveredSeed(id)})
	if err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: recovered request: %w", err)
	}
	parsed, warns, err := parseOne(raw)
	if err != nil {
		return library.DraftDetail{}, err
	}
	label := in.Title
	if label == "" {
		label = in.ReferenceTitle
	}
	label = cutRunes(label, maxLabelRunes)
	at := s.stamp()
	tx, err := s.st.W.BeginTx(ctx, nil)
	if err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertDraft(ctx, tx, id, OriginRecovered, "", label, parsed, p.Key(), at); err != nil {
		return library.DraftDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO draft_recoveries(draft_id, track_id, analysis_sha256, language, words, confidence, style_source, rationale, created_by, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, id, in.ReferenceTrackID, in.AnalysisSHA256, nullable(in.Language), in.Words, in.Confidence, in.StyleSource,
		nullable(in.Rationale), p.Key(), at); err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: recovery row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return library.DraftDetail{}, fmt.Errorf("drafts: commit: %w", err)
	}
	return s.result(ctx, p, id, warns)
}
