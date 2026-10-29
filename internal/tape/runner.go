package tape

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/textfold"
)

// The runner (C128.5): one goroutine, one tape at a time. It claims a queued row, re-reads the snapshot and checks that the
// input key still matches (a published Record can be edited by its human between the export and the run), renders through
// the Pipeline, claims the file under derived/tapes/ and finishes the row. A shutdown mid-render leaves the row running:
// Recover re-queues it once its lease has expired.

// idleEvery is how long an idle runner waits before it looks again (an export wakes it at once).
const idleEvery = 5 * time.Second

// MaxMessage is the length bound of tapes.error_message.
const maxMessage = 500

// Run is the runner loop (serve only): it claims and renders queued tapes one at a time until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if s.o.Store == nil || s.o.Pipeline == nil {
		<-ctx.Done()
		return
	}
	for ctx.Err() == nil {
		ran, err := s.RunOne(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Error("tape runner", "err", err.Error())
		}
		if ran && err == nil {
			continue
		}
		if err := s.Recover(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("tape recover", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(idleEvery):
		}
	}
}

// RunOne claims and processes one queued tape; false means there was none (tests).
func (s *Service) RunOne(ctx context.Context) (bool, error) {
	if s.o.Store == nil || s.o.Pipeline == nil {
		return false, nil
	}
	id, ok, err := s.claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	s.process(ctx, id)
	return true, ctx.Err()
}

func (s *Service) owner() string {
	if s.o.Owner != "" {
		return s.o.Owner
	}
	return "tape-runner"
}

// claim moves the oldest queued tape to running with a lease.
func (s *Service) claim(ctx context.Context) (id string, ok bool, err error) {
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		id, ok = "", false
		switch err := tx.QueryRowContext(ctx, `SELECT id FROM tapes WHERE state = 'queued' ORDER BY created_at, id LIMIT 1`).Scan(&id); {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		now := s.now()
		res, err := tx.ExecContext(ctx, `UPDATE tapes SET state = 'running', owner = ?, lease_expires_at = ?, started_at = ?, progress_json = NULL
			WHERE id = ? AND state = 'queued'`, s.owner(), store.FormatTime(now.Add(LeaseTTL)), store.FormatTime(now), id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		ok = n == 1
		return nil
	})
	return id, ok, err
}

// runFailure is why a run ended: a stable code and a message with ids only.
type runFailure struct{ code, msg string }

func (f *runFailure) Error() string { return f.code + ": " + f.msg }

func (s *Service) process(ctx context.Context, id string) {
	pr, params, fail := s.reprepare(ctx, id)
	if fail == nil {
		fail = s.render(ctx, id, pr, params)
	}
	if fail == nil || ctx.Err() != nil {
		return // finished, or interrupted by shutdown: the lease expires and Recover re-queues the row
	}
	s.markFailed(ctx, id, fail)
}

// reprepare re-reads the snapshot of a claimed tape and checks that it still hashes to the stored key.
func (s *Service) reprepare(ctx context.Context, id string) (prepared, paramsDoc, *runFailure) {
	var key, paramsJSON, recordID, planJSON string
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT key, params_json, record_id, plan_json FROM tapes WHERE id = ?`, id).Scan(&key, &paramsJSON, &recordID, &planJSON); err != nil {
		return prepared{}, paramsDoc{}, &runFailure{ErrSourceChanged, "the tape row could not be read"}
	}
	var params paramsDoc
	var old planDoc
	if json.Unmarshal([]byte(paramsJSON), &params) != nil || json.Unmarshal([]byte(planJSON), &old) != nil {
		return prepared{}, paramsDoc{}, &runFailure{ErrSourceChanged, "the tape row is malformed"}
	}
	if s.o.Source == nil {
		return prepared{}, paramsDoc{}, &runFailure{ErrSourceChanged, "no snapshot source"}
	}
	snap, err := s.o.Source.Snapshot(ctx, recordID, SnapOptions{IncludeLiner: params.IncludeLiner})
	if err != nil {
		return prepared{}, paramsDoc{}, &runFailure{ErrSourceChanged, "the record could not be snapshotted again"}
	}
	if snap.Status != "published" {
		return prepared{}, paramsDoc{}, &runFailure{ErrSourceChanged, "the record is no longer published"}
	}
	if len(snap.Items) == 0 {
		return prepared{}, paramsDoc{}, &runFailure{ErrSourceChanged, "the record has no playable take any more"}
	}
	pr, err := s.prepare(ctx, snap, params, old.AcceptedWarnings)
	if err != nil {
		return prepared{}, paramsDoc{}, &runFailure{ErrSourceChanged, "the tape inputs could not be rebuilt"}
	}
	if pr.key != key {
		return prepared{}, paramsDoc{}, &runFailure{ErrSourceChanged, "the record or its takes changed since the export"}
	}
	return pr, params, nil
}

// render runs the Pipeline for a claimed tape and finishes the row. It returns nil when the tape is ready or the context
// ended; otherwise the failure to record.
func (s *Service) render(ctx context.Context, id string, pr prepared, params paramsDoc) *runFailure {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(s.renewEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-rctx.Done():
				return
			case <-t.C:
				if !s.renew(rctx, id) {
					s.log.Warn("tape lease lost", "tape_id", id)
					cancel()
					return
				}
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	var mu sync.Mutex
	var last time.Time
	progress := func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		now := s.now()
		if !last.IsZero() && now.Sub(last) < s.progressEvery {
			return
		}
		last = now
		b, _ := json.Marshal(progressDoc(p))
		_, _ = s.o.Store.W.ExecContext(rctx, `UPDATE tapes SET progress_json = ? WHERE id = ? AND state = 'running' AND owner = ?`, string(b), id, s.owner())
	}
	job := Job{Snapshot: pr.snap, Items: pr.items, TargetLUFS: pr.target, Chapters: pr.chapters, Tags: pr.tags, CoverKey: pr.coverKey}
	res, err := s.o.Pipeline.Render(rctx, job, progress)
	if err != nil {
		if res.Cleanup != nil {
			res.Cleanup()
		}
		if ctx.Err() != nil {
			return nil
		}
		code, msg := s.failureOf(err)
		return &runFailure{code, msg}
	}
	if res.Cleanup != nil {
		defer res.Cleanup()
	}
	if ctx.Err() != nil {
		return nil
	}
	if res.TotalSamples <= 0 || res.Bytes <= 0 || len(res.Chapters) != len(pr.items) || len(res.SHA256) != 64 {
		return &runFailure{ErrVerifyFailed, "the rendered tape is incomplete"}
	}
	rel, err := s.claimFile(res)
	if err != nil {
		code, msg := s.failureOf(err)
		return &runFailure{code, msg}
	}
	fin := context.WithoutCancel(ctx)
	if err := s.markReady(fin, id, pr, res, rel); err != nil {
		var f *runFailure
		if errors.As(err, &f) {
			return f
		}
		s.log.Error("tape finish", "tape_id", id, "err", err.Error())
		return &runFailure{ErrEncodeFailed, "the tape could not be recorded"}
	}
	return nil
}

// renew extends the lease of a running tape; false means the tape is no longer ours.
func (s *Service) renew(ctx context.Context, id string) bool {
	res, err := s.o.Store.W.ExecContext(ctx, `UPDATE tapes SET lease_expires_at = ? WHERE id = ? AND state = 'running' AND owner = ?`,
		store.FormatTime(s.now().Add(LeaseTTL)), id, s.owner())
	if err != nil {
		return ctx.Err() != nil // a failed write is retried at the next tick
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// failureOf maps an error of the Pipeline (or of claiming its file) to a stored code and message: a *RenderError or a
// *runFailure carries its code, ENOSPC is
// TAPE_DISK_FULL and anything else is TAPE_ENCODE_FAILED. The data root's path is masked in the message.
func (s *Service) failureOf(err error) (code, msg string) {
	msg = err.Error()
	var re *RenderError
	var rf *runFailure
	switch {
	case errors.As(err, &rf):
		code, msg = rf.code, rf.msg
	case errors.As(err, &re) && re.Code != "":
		code = re.Code
	case errors.Is(err, syscall.ENOSPC):
		code = ErrDiskFull
	default:
		code = ErrEncodeFailed
	}
	if s.o.Data != nil {
		if root := s.o.Data.Name(); root != "" {
			msg = strings.ReplaceAll(msg, root, "<data>")
		}
	}
	if r := []rune(msg); len(r) > maxMessage {
		msg = string(r[:maxMessage])
	}
	return code, msg
}

func (s *Service) markFailed(ctx context.Context, id string, f *runFailure) {
	msg := f.msg
	if r := []rune(msg); len(r) > maxMessage {
		msg = string(r[:maxMessage])
	}
	err := s.writeTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE tapes SET state = 'failed', error_code = ?, error_message = ?, finished_at = ?, owner = NULL,
			lease_expires_at = NULL, progress_json = NULL WHERE id = ? AND state = 'running' AND owner = ?`, f.code, msg, store.FormatTime(s.now()), id, s.owner())
		return err
	})
	if err != nil {
		s.log.Error("tape mark failed", "tape_id", id, "err", err.Error())
	}
}

// Filename is the download name of a tape: <record title slug>-<YYYYMMDD>-tape[-lossy-sources].flac, at most 120 bytes.
func Filename(recordTitle string, day time.Time, lossy bool) string {
	suffix := "-" + day.UTC().Format("20060102") + "-tape"
	if lossy {
		suffix += "-lossy-sources"
	}
	suffix += ".flac"
	slug := textfold.Slug(recordTitle, 120-len(suffix))
	if slug == "" {
		slug = "record"
	}
	return slug + suffix
}

func (s *Service) markReady(ctx context.Context, id string, pr prepared, res Result, rel string) error {
	doc := pr.doc
	doc.Chapters = make([]chapterDoc, len(res.Chapters))
	for i, c := range res.Chapters {
		doc.Chapters[i] = chapterDoc(c)
	}
	planJSON, _ := json.Marshal(doc)
	now := s.now()
	name := Filename(pr.snap.Title, now, pr.lossy > 0)
	var lost bool
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE tapes SET state = 'ready', sha256 = ?, bytes = ?, path = ?, filename = ?, total_samples = ?, plan_json = ?,
			progress_json = NULL, owner = NULL, lease_expires_at = NULL, finished_at = ? WHERE id = ? AND state = 'running' AND owner = ?`,
			res.SHA256, res.Bytes, rel, name, res.TotalSamples, string(planJSON), store.FormatTime(now), id, s.owner())
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		lost = n != 1
		return nil
	})
	if err != nil {
		return err
	}
	if lost {
		s.removeIfUnreferenced(ctx, rel)
		return &runFailure{ErrInterrupted, "the tape lease was lost"}
	}
	return nil
}

// claimFile moves a rendered file into derived/tapes/<aa>/<sha256>.flac, mode 0444. The name is the content hash, so an
// existing file is identical bytes already. A file inside the data root is linked, any other is copied and re-hashed.
func (s *Service) claimFile(res Result) (string, error) {
	data := s.o.Data
	if data == nil {
		return "", errors.New("tape: no data root")
	}
	if _, err := hex.DecodeString(res.SHA256); err != nil || len(res.SHA256) != 64 {
		return "", &runFailure{ErrVerifyFailed, "the rendered tape has no valid hash"}
	}
	rel := path.Join("derived", "tapes", res.SHA256[:2], res.SHA256+".flac")
	if err := data.MkdirAll(path.Dir(rel), 0o755); err != nil {
		return "", fmt.Errorf("tape: derived dir: %w", err)
	}
	if _, err := data.Stat(rel); err == nil {
		return rel, nil
	}
	if src, ok := s.insideData(res.Path); ok {
		err := data.Link(src, rel)
		if err == nil || errors.Is(err, fs.ErrExist) {
			if err := data.Chmod(rel, 0o444); err != nil {
				return "", fmt.Errorf("tape: chmod: %w", err)
			}
			return rel, nil
		}
		// a link can fail across mounts: copy instead
	}
	tmp := path.Join(path.Dir(rel), "."+res.SHA256+".part")
	f, err := data.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("tape: create file: %w", err)
	}
	in, err := os.Open(res.Path)
	if err != nil {
		_ = f.Close()
		_ = data.Remove(tmp)
		return "", fmt.Errorf("tape: open rendered file: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), in)
	_ = in.Close()
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = data.Remove(tmp)
		return "", fmt.Errorf("tape: copy: %w", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != res.SHA256 || n != res.Bytes {
		_ = data.Remove(tmp)
		return "", &runFailure{ErrVerifyFailed, "the rendered file does not match its hash"}
	}
	if err := data.Chmod(tmp, 0o444); err != nil {
		_ = data.Remove(tmp)
		return "", fmt.Errorf("tape: chmod: %w", err)
	}
	if err := data.Rename(tmp, rel); err != nil {
		_ = data.Remove(tmp)
		return "", fmt.Errorf("tape: claim file: %w", err)
	}
	return rel, nil
}

// insideData returns the data-root-relative form of an absolute path under the root.
func (s *Service) insideData(abs string) (string, bool) {
	root := s.o.Data.Name()
	if root == "" || !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", false
	}
	return strings.TrimPrefix(abs, root+string(os.PathSeparator)), true
}

// removeIfUnreferenced deletes a tape file no ready row names (a lost race, an expired tape).
func (s *Service) removeIfUnreferenced(ctx context.Context, rel string) {
	if s.o.Data == nil || !strings.HasPrefix(rel, "derived/tapes/") {
		return
	}
	var n int
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM tapes WHERE path = ? AND state = 'ready'`, rel).Scan(&n); err != nil || n > 0 {
		return
	}
	if err := s.o.Data.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("tape: remove file", "err", err.Error())
	}
}

// Recover re-queues running tapes whose lease expired (startup, and when the runner is idle). The first and second expiry
// raise tapes.attempts; the third fails the tape with TAPE_INTERRUPTED. Progress writes never touch the counter.
func (s *Service) Recover(ctx context.Context) error {
	if s.o.Store == nil {
		return nil
	}
	type cand struct {
		id, lease string
		attempts  int
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT id, attempts, lease_expires_at FROM tapes WHERE state = 'running'`)
	if err != nil {
		return fmt.Errorf("tape: recover: %w", err)
	}
	var cs []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.attempts, &c.lease); err != nil {
			_ = rows.Close()
			return err
		}
		cs = append(cs, c)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := s.now()
	requeued := false
	for _, c := range cs {
		exp, err := store.ParseInstant(c.lease)
		if err != nil || exp.After(now) {
			continue
		}
		err = s.writeTx(ctx, func(tx *sql.Tx) error {
			if c.attempts >= 2 {
				_, err := tx.ExecContext(ctx, `UPDATE tapes SET state = 'failed', error_code = ?, error_message = ?, finished_at = ?, owner = NULL,
					lease_expires_at = NULL, progress_json = NULL WHERE id = ? AND state = 'running' AND lease_expires_at = ?`,
					ErrInterrupted, "the tape was interrupted three times", store.FormatTime(now), c.id, c.lease)
				return err
			}
			r, err := tx.ExecContext(ctx, `UPDATE tapes SET state = 'queued', attempts = attempts + 1, owner = NULL, lease_expires_at = NULL,
				started_at = NULL, progress_json = NULL WHERE id = ? AND state = 'running' AND lease_expires_at = ?`, c.id, c.lease)
			if err == nil {
				n, _ := r.RowsAffected()
				requeued = requeued || n == 1
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("tape: recover %s: %w", c.id, err)
		}
	}
	if requeued {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return nil
}
