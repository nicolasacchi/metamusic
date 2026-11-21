package ears

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// ErrAttachMismatch is returned by AttachAnalysis when the received document does not have the digest or size it was reported with.
var ErrAttachMismatch = errors.New("ears: the analysis document differs from its report")

// AttachedAnalysis is what AttachAnalysis stored: the artifact name and its data-relative path. Duplicate is true when the take
// already had an analysis document (a race loser, C340.5): the new one is kept as analysis-<n>.json and must not be analysed.
type AttachedAnalysis struct {
	Name, Path string
	SHA256     string
	Bytes      int64
	Duplicate  bool
}

// AttachAnalysis attaches a received analysis document to an EXISTING take (C340.5, D-G1-7), inside the caller's transaction: it reads the
// received file (srcPath, relative to the data root), checks its digest and size against the report and parses it strictly
// (tierb.Parse; a failure wraps tierb.ErrContract), then writes takes/<track>/analysis.json at 0444 (the directory is created for an
// import that had none) and inserts the artifacts row. A take that already has a document keeps it: the new one is stored as
// analysis-<n>.json and reported as a duplicate. It writes nothing else: never the take's audio, tags or recipe, never an analyses row
// (AnalyzeTierB does that, after the commit).
//
// The file is written before the row; when the transaction later fails the file is an orphan that no row names, and the next attach
// takes its name over.
func (s *Service) AttachAnalysis(ctx context.Context, tx *sql.Tx, trackID, srcPath, wantSHA string, wantBytes int64) (AttachedAnalysis, error) {
	if err := s.wired(); err != nil {
		return AttachedAnalysis{}, err
	}
	doc, err := s.readReceived(srcPath)
	if err != nil {
		return AttachedAnalysis{}, err
	}
	sum := sha256.Sum256(doc)
	sha := hex.EncodeToString(sum[:])
	if sha != wantSHA || int64(len(doc)) != wantBytes {
		return AttachedAnalysis{}, ErrAttachMismatch
	}
	if _, err := tierb.Parse(doc); err != nil {
		return AttachedAnalysis{}, fmt.Errorf("ears: analysis document: %w", err)
	}
	var one int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, trackID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return AttachedAnalysis{}, library.ErrNoSuchTrack
	case err != nil:
		return AttachedAnalysis{}, fmt.Errorf("ears: attach analysis: %w", err)
	}
	have := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM artifacts WHERE track_id = ? AND name LIKE 'analysis%.json'`, trackID)
	if err != nil {
		return AttachedAnalysis{}, fmt.Errorf("ears: attach analysis: %w", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return AttachedAnalysis{}, err
		}
		have[n] = true
	}
	if err := rows.Close(); err != nil {
		return AttachedAnalysis{}, err
	}
	name := "analysis.json"
	for n := 2; have[name]; n++ {
		name = fmt.Sprintf("analysis-%d.json", n)
	}
	a := AttachedAnalysis{Name: name, Path: path.Join("takes", trackID, name), SHA256: sha, Bytes: int64(len(doc)), Duplicate: len(have) > 0}
	if err := s.writeFrozen(a.Path, doc); err != nil {
		return AttachedAnalysis{}, err
	}
	if err := s.o.Library.AttachArtifact(ctx, tx, trackID, library.NewArtifact{Name: name, SHA256: sha, Path: a.Path, Bytes: a.Bytes}); err != nil {
		_ = s.o.Data.Remove(a.Path)
		return AttachedAnalysis{}, err
	}
	return a, nil
}

func (s *Service) readReceived(srcPath string) ([]byte, error) {
	f, err := s.o.Data.Open(srcPath)
	if err != nil {
		return nil, fmt.Errorf("ears: received analysis document: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, tierb.MaxDocBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ears: received analysis document: %w", err)
	}
	if len(b) > tierb.MaxDocBytes {
		return nil, fmt.Errorf("%w: over %d bytes", tierb.ErrContract, tierb.MaxDocBytes)
	}
	return b, nil
}

// writeFrozen creates the file exclusively with mode 0444. A file already there has no row (the caller chose a free name), so it is the
// orphan of an earlier attempt that did not commit: it is replaced.
func (s *Service) writeFrozen(rel string, doc []byte) error {
	if err := s.o.Data.MkdirAll(path.Dir(rel), 0o755); err != nil {
		return fmt.Errorf("ears: attach analysis: %w", err)
	}
	f, err := s.o.Data.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o444)
	if errors.Is(err, fs.ErrExist) {
		if err := s.o.Data.Remove(rel); err != nil {
			return fmt.Errorf("ears: attach analysis: %w", err)
		}
		f, err = s.o.Data.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o444)
	}
	if err != nil {
		return fmt.Errorf("ears: attach analysis: %w", err)
	}
	_, err = f.Write(doc)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = s.o.Data.Remove(rel)
		return fmt.Errorf("ears: attach analysis: %w", err)
	}
	return nil
}
