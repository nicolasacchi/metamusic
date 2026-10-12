package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
)

// entry is one non-directory inbox entry found by the walk.
type entry struct {
	path     string
	size     int64
	mtime    time.Time
	id       inbox.FileID
	hasID    bool
	utf8     bool
	kind     string
	sha      string
	data     []byte // retained content of small parsed files
	failed   bool   // reading failed; an error is recorded
	rd       *resultDir
	artifact string // name under takes/<id>/ when this file is kept with a take
	flac     bool   // named like a FLAC (audio candidate)
	arr      []json.RawMessage
	audio    *audioFile
	bomSeen  bool // the bom warning was already raised for this file
}

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// nameKey is the stable key of a path whose bytes are not UTF-8.
func nameKey(p string) string { return "name:" + hexSHA([]byte(p)) }

// pathRef is how a path appears in reports (never raw non-UTF-8 bytes).
func pathRef(p string) string {
	if utf8.ValidString(p) {
		return p
	}
	return nameKey(p)
}

// ref is the report/mapping reference of the entry: its path, or for
// non-UTF-8 names sha256:<content> when hashed, else name:<hex>.
func (e *entry) ref() string {
	if e.utf8 {
		return e.path
	}
	if e.sha != "" {
		return "sha256:" + e.sha
	}
	return nameKey(e.path)
}

// sourcePath is the recipe_sources.source_path of the entry.
func (e *entry) sourcePath() string { return pathRef(e.path) }

func (e *entry) ignored() bool { return strings.HasPrefix(e.kind, ignPrefix) }

// resultDir is a YuE2 result directory (C16.1).
type resultDir struct {
	dir     string
	flac    *entry
	req     *entry
	result  *entry
	score   *entry
	members []*entry
	parsed  yue2.Parsed
}

func (b *builder) warnCounted(p string, line int, code string) {
	k := p + "\x00" + code
	if i, ok := b.wc[k]; ok {
		b.wcn[k]++
		b.iss.warnings[i].Message = fmt.Sprintf("%d occurrences", b.wcn[k])
		return
	}
	b.wc[k], b.wcn[k] = len(b.iss.warnings), 1
	b.iss.warn(p, line, code, "")
}

// walk lists every non-directory entry in lexical order.
func (b *builder) walk() error {
	fsys := b.im.src.FS()
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if cerr := b.ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if p == "." {
				return fmt.Errorf("importer: scan inbox: %w", err)
			}
			b.iss.fail(pathRef(p), 0, "unreadable", "directory could not be read")
			return nil
		}
		if p == "." || d.IsDir() {
			return nil
		}
		e := &entry{path: p, utf8: utf8.ValidString(p)}
		info, ierr := d.Info()
		if ierr != nil {
			b.iss.fail(pathRef(p), 0, "unreadable", "file could not be inspected")
			return nil //nolint:nilerr // reported as an error issue; the walk continues
		}
		e.size, e.mtime = info.Size(), info.ModTime()
		e.id, e.hasID = inbox.IDOf(info)
		switch t := d.Type(); {
		case t&fs.ModeSymlink != 0:
			e.kind = kIgnSymlink
		case !t.IsRegular():
			e.kind = kIgnNotRegular
		default:
			e.kind = nameKind(p)
		}
		e.flac = e.kind == kPendingFLAC
		if !e.utf8 {
			b.iss.warn(nameKey(p), 0, "non_utf8_name", "")
		}
		b.entries = append(b.entries, e)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(b.entries, func(i, j int) bool { return b.entries[i].path < b.entries[j].path })
	for _, e := range b.entries {
		b.byPath[e.path] = e
	}
	return nil
}

// open opens an entry for reading through the inbox guard.
func (b *builder) open(e *entry) (fs.File, bool) {
	f, err := b.im.src.OpenRegular(e.path, e.id)
	if err != nil {
		e.failed = true
		code, msg := "unreadable", "file could not be read"
		if errors.Is(err, inbox.ErrChanged) {
			code, msg = "changed", "file changed while scanning"
		}
		b.iss.fail(pathRef(e.path), 0, code, msg)
		return nil, false
	}
	return f, true
}

// unsettle marks an entry skipped because it changed while being read.
func (b *builder) unsettle(e *entry) {
	e.kind = kIgnUnsettled
	e.failed = true
}

// readEntry reads a whole small file (cap bytes) and keeps it on the entry.
func (b *builder) readEntry(e *entry, limit int64) []byte {
	if e.data != nil {
		return e.data
	}
	if e.failed {
		return nil
	}
	if e.size > limit {
		e.failed = true
		b.iss.fail(pathRef(e.path), 0, "too_large", fmt.Sprintf("file exceeds the %d byte read cap", limit))
		return nil
	}
	f, ok := b.open(e)
	if !ok {
		return nil
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		e.failed = true
		b.iss.fail(pathRef(e.path), 0, "unreadable", "file could not be read")
		return nil
	}
	if int64(len(data)) != e.size {
		b.unsettle(e)
		return nil
	}
	e.sha = hexSHA(data)
	e.data = data
	return data
}

// hashEntry streams an entry through sha256 (artifacts, never loaded).
func (b *builder) hashEntry(e *entry) bool {
	if e.sha != "" {
		return true
	}
	if e.failed {
		return false
	}
	f, ok := b.open(e)
	if !ok {
		return false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		e.failed = true
		b.iss.fail(pathRef(e.path), 0, "unreadable", "file could not be read")
		return false
	}
	if n != e.size {
		b.unsettle(e)
		return false
	}
	e.sha = hex.EncodeToString(h.Sum(nil))
	return true
}

// detectResultDirs finds YuE2 result directories (exactly one FLAC next to a
// request.json that parses as a recipe) and assigns their members' kinds.
func (b *builder) detectResultDirs() {
	byDir := map[string][]*entry{}
	for _, e := range b.entries {
		switch e.kind {
		case kIgnTemp, kIgnArchive, kIgnSymlink, kIgnNotRegular:
			continue
		}
		d := path.Dir(e.path)
		byDir[d] = append(byDir[d], e)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		members := byDir[d]
		var flacs []*entry
		var req *entry
		for _, m := range members {
			if m.kind == kPendingFLAC {
				flacs = append(flacs, m)
			}
			if path.Base(m.path) == "request.json" {
				req = m
			}
		}
		if len(flacs) != 1 || req == nil {
			continue
		}
		data := b.readEntry(req, capJSON)
		if data == nil {
			continue
		}
		parsed, err := yue2.ParseRequest(data, yue2.ParseOptions{})
		if err != nil {
			continue
		}
		rd := &resultDir{dir: d, flac: flacs[0], req: req, parsed: parsed, members: members}
		b.rdirs = append(b.rdirs, rd)
		b.assignMembers(rd)
	}
	// result_subdir: subdirectories of a result dir (their files follow the general rules).
	for _, rd := range b.rdirs {
		seen := map[string]bool{}
		prefix := rd.dir + "/"
		if rd.dir == "." {
			prefix = ""
		}
		for _, e := range b.entries {
			if !strings.HasPrefix(e.path, prefix) {
				continue
			}
			rest := e.path[len(prefix):]
			if i := strings.IndexByte(rest, '/'); i > 0 && !seen[rest[:i]] {
				seen[rest[:i]] = true
				b.iss.warn(pathRef(prefix+rest[:i]), 0, "result_subdir", "")
			}
		}
	}
}

func (b *builder) assignMembers(rd *resultDir) {
	var abcs []*entry
	for _, m := range rd.members {
		base := path.Base(m.path)
		m.rd = rd
		switch {
		case m == rd.flac: // the audio is copied by content; its name never matters
			m.kind = kPendingFLAC
		case !safeNameRe.MatchString(base):
			m.kind = kIgnUnsafeName
			m.rd = nil
		case base == "request.json":
			m.kind, m.artifact = kRequestJSON, base
		case base == "result.json":
			m.kind, m.artifact = kYue2Result, base
			rd.result = m
		case base == "latent.npy":
			m.kind = kIgnLatent
			m.rd = nil
		case strings.EqualFold(path.Ext(base), ".abc"):
			abcs = append(abcs, m)
		case strings.EqualFold(path.Ext(base), ".mp3"):
			m.kind = kIgnMP3Sidecar
			m.rd = nil
		default:
			m.kind, m.artifact = kArtifact, base
		}
	}
	// The score is the file named score.abc, else the only .abc of the dir.
	var score *entry
	for _, m := range abcs {
		if path.Base(m.path) == "score.abc" {
			score = m
		}
	}
	if score == nil && len(abcs) == 1 {
		score = abcs[0]
	}
	for _, m := range abcs {
		if m == score {
			m.kind, m.artifact = kScoreABC, "score.abc"
			rd.score = m
		} else {
			m.kind, m.artifact = kArtifact, path.Base(m.path)
		}
	}
}

// markUnsettled applies the MinAge guard; a young member holds back the
// result dir's audio too.
func (b *builder) markUnsettled() {
	if b.im.o.MinAge <= 0 {
		return
	}
	cut := b.now.Add(-b.im.o.MinAge)
	young := func(e *entry) bool { return e.mtime.After(cut) }
	for _, rd := range b.rdirs {
		for _, m := range rd.members {
			if m != rd.flac && m.rd == rd && young(m) {
				rd.flac.kind = kIgnUnsettled
			}
		}
	}
	for _, e := range b.entries {
		if e.ignored() {
			continue
		}
		if young(e) {
			e.kind = kIgnUnsettled
		}
	}
}

// bomWarn raises the path-level bom warning once per file whose content
// starts with a UTF-8 BOM (C16.1).
func (b *builder) bomWarn(e *entry, data []byte) {
	if e == nil || e.bomSeen || !bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return
	}
	e.bomSeen = true
	b.iss.warn(e.ref(), 0, "bom", "")
}

// classifyFiles reads the content of candidate files and settles their kinds.
func (b *builder) classifyFiles() {
	for _, e := range b.entries {
		if b.ctx.Err() != nil {
			return
		}
		switch e.kind {
		case kPendingJSON:
			if e.rd != nil {
				continue
			}
			data := b.readEntry(e, capJSON)
			if data == nil {
				continue
			}
			b.bomWarn(e, data)
			jc := classifyJSON(data, storableStem(e.path))
			e.kind = jc.kind
			if jc.warning != "" {
				b.iss.warn(e.ref(), 0, jc.warning, "")
			}
			switch jc.kind {
			case kRecipesJSONArray:
				e.arr = jc.elements
			case kCollectionJSON:
				b.collFiles = append(b.collFiles, &collFile{e: e, name: jc.coll.name, ids: jc.coll.ids})
			}
		case kPendingCSV:
			data := b.readEntry(e, capCSV)
			if data == nil {
				continue
			}
			b.bomWarn(e, data)
			rows, head, isRef, err := classifyCSV(data)
			if !isRef {
				e.kind = kIgnUnknown
				continue
			}
			e.kind = kReferenceIndex
			if err != nil {
				b.iss.fail(e.ref(), 0, "bad_csv", "reference index has a malformed row")
			}
			b.refFiles = append(b.refFiles, &refFile{e: e, rows: rows, header: head})
		case kRecipesJSONL:
			b.readEntry(e, capJSONL)
		case kRequestJSON, kYue2Result:
			if e.rd != nil {
				b.readEntry(e, capJSON)
			}
		case kArtifact:
			if e.rd != nil {
				b.hashEntry(e)
			}
		case kScoreABC:
			if e.rd != nil {
				if e.size <= capABC {
					b.readEntry(e, capABC)
				} else {
					b.hashEntry(e)
				}
			}
		}
	}
}

// ---- audio ---------------------------------------------------------------

// audioFile is one FLAC candidate.
type audioFile struct {
	e       *entry
	info    flacmeta.Info
	frames  string
	audioID string
	md5     string
	tags    map[string][]string
	rd      *resultDir
	stem    string
	status  string // "" (ok) | "invalid" | "unsettled"
}

func (b *builder) readAudio(e *entry) {
	af := &audioFile{e: e, rd: e.rd, stem: stemOf(e.path)}
	e.audio = af
	b.audioFiles = append(b.audioFiles, af)
	if e.kind == kIgnUnsettled {
		af.status = "unsettled"
		return
	}
	f, ok := b.open(e)
	if !ok {
		af.status = "invalid"
		return
	}
	defer func() { _ = f.Close() }()
	ra, okRA := f.(io.ReaderAt)
	sk, okSK := f.(io.Seeker)
	if !okRA || !okSK {
		e.failed = true
		af.status = "invalid"
		b.iss.fail(e.ref(), 0, "unreadable", "file is not seekable")
		return
	}
	var magic [4]byte
	if n, _ := ra.ReadAt(magic[:], 0); n < 4 || string(magic[:]) != "fLaC" {
		e.kind, af.status = kNotFLAC, "invalid"
		b.iss.fail(e.ref(), 0, "not_flac", "file does not start with the fLaC marker")
		return
	}
	if _, err := sk.Seek(0, io.SeekStart); err != nil {
		af.status = "invalid"
		b.iss.fail(e.ref(), 0, "unreadable", "file could not be read")
		return
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		af.status = "invalid"
		e.failed = true
		b.iss.fail(e.ref(), 0, "unreadable", "file could not be read")
		return
	}
	if n != e.size {
		b.unsettle(e)
		af.status = "unsettled"
		return
	}
	e.sha = hex.EncodeToString(h.Sum(nil))
	info, err := flacmeta.Read(ra, e.size)
	if err != nil {
		af.status = "invalid"
		b.iss.fail(e.ref(), 0, "invalid_flac", "FLAC metadata is not readable")
		return
	}
	frames, err := flacmeta.FramesSHA256(ra, e.size, info.FramesOffset)
	if err != nil || info.StreamInfo.TotalSamples == 0 {
		af.status = "invalid"
		b.iss.fail(e.ref(), 0, "invalid_flac", "FLAC audio frames are not readable")
		return
	}
	e.kind = kAudioFLAC
	af.info, af.frames = info, frames
	af.audioID = flacmeta.AudioID(frames, info.StreamInfo.TotalSamples)
	af.md5 = hex.EncodeToString(info.StreamInfo.MD5[:])
	af.tags = map[string][]string{}
	for _, c := range info.Comments {
		if k, v, ok := strings.Cut(c, "="); ok {
			k = strings.ToUpper(k)
			af.tags[k] = append(af.tags[k], v)
		}
	}
}

// tag returns the first non-empty value of a tag.
func (a *audioFile) tag(key string) string {
	for _, v := range a.tags[key] {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
