package subsonic

import (
	clist "container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/listening"
)

// playStateLimit bounds every in-memory per-client map (C109): the oldest entries leave first.
const playStateLimit = 4096

// lru is a bounded map from string keys to any value; the least recently used entry leaves first.
type lru struct {
	mu    sync.Mutex
	limit int
	ll    *clist.List
	idx   map[string]*clist.Element
}

type lruEntry struct {
	key string
	val any
}

func newLRU(limit int) *lru {
	return &lru{limit: limit, ll: clist.New(), idx: map[string]*clist.Element{}}
}

func (l *lru) put(key string, val any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.idx[key]; ok {
		el.Value.(*lruEntry).val = val
		l.ll.MoveToFront(el)
		return
	}
	l.idx[key] = l.ll.PushFront(&lruEntry{key: key, val: val})
	for l.ll.Len() > l.limit {
		old := l.ll.Back()
		l.ll.Remove(old)
		delete(l.idx, old.Value.(*lruEntry).key)
	}
}

func (l *lru) get(key string) (any, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.idx[key]
	if !ok {
		return nil, false
	}
	l.ll.MoveToFront(el)
	return el.Value.(*lruEntry).val, true
}

func (l *lru) remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.idx[key]; ok {
		l.ll.Remove(el)
		delete(l.idx, key)
	}
}

func (l *lru) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ll.Len()
}

// playback is the state of one (client, track) reportPlayback session.
type playback struct {
	session string
	resume  int64     // position of the last `playing` report
	at      time.Time // when that report arrived
	rate    float64   // the playbackRate of that report (1 when absent)
	playing bool
}

// playState holds the per-client maps of the bridge; all of them are lost on restart (D-M4a-12).
type playState struct {
	mu       sync.Mutex // serialises the reportPlayback state machine
	seen     *lru       // client|track -> time.Time of the last accepted reportPlayback
	streamed *lru       // client|track -> "flac" | "mp3", the format last streamed
	sessions *lru       // client|track -> *playback
}

func newPlayState() *playState {
	return &playState{seen: newLRU(playStateLimit), streamed: newLRU(playStateLimit), sessions: newLRU(playStateLimit)}
}

func playKey(client, track string) string { return client + "|" + track }

// seenWithin reports whether a reportPlayback for the pair was accepted less than d before now.
func (p *playState) seenWithin(client, track string, now time.Time, d time.Duration) bool {
	v, ok := p.seen.get(playKey(client, track))
	if !ok {
		return false
	}
	return now.Sub(v.(time.Time)) < d
}

// suppressWindow is how long a reportPlayback session for a (client, track) pair suppresses scrobbles (D-M4a-12).
const suppressWindow = 30 * time.Minute

// eventID is the deterministic UUIDv8 of a listen event (D-M4a-12): the sha256 of its parts, so a retried
// request dedupes through the listen table's primary key.
func eventID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x80 // version 8
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func shortHash(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(h[:8])
}

func (s *server) registerListens() {
	s.add("scrobble", true, (*call).scrobble)
	s.add("reportPlayback", true, (*call).reportPlayback)
}

func (c *call) listener() (listener, error) {
	if c.s.listen == nil {
		return nil, fail(errGeneric, "service unavailable")
	}
	return c.s.listen, nil
}

// listenFormat is the format the bridge last streamed the track in for this client, else the client's default.
// A lossy master is always an MP3.
func (c *call) listenFormat(row songRow) (string, error) {
	if row.Lossy {
		return "mp3", nil
	}
	if v, ok := c.s.play.streamed.get(playKey(c.principal.ID, row.ID)); ok {
		return v.(string), nil
	}
	mp3, err := c.mp3Default()
	if err != nil {
		return "", err
	}
	if mp3 {
		return "mp3", nil
	}
	return "flac", nil
}

// logEvent stores one listen event as the calling device. An event whose client time the listening service
// refuses (a clock far off, or a time sent in seconds) is stored again at the server clock; any other rejection
// is dropped (the client could do nothing about it) and only its fixed code is logged at debug level.
func (c *call) logEvent(ev listening.Event) error {
	lst, err := c.listener()
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		accepted, _, rejected, err := lst.Log(c.ctx, c.principal, "subsonic", []listening.Event{ev})
		if err != nil {
			return err
		}
		if len(rejected) > 0 {
			code := rejected[0].Code
			c.s.logger().Debug("subsonic listen event rejected", "code", code)
			if code == "bad_client_at" && attempt == 0 {
				ev.ClientAt = c.s.now()
				continue
			}
			return nil
		}
		if accepted > 0 {
			audit.NoteCreated(c.ctx, ev.ID)
		}
		return nil
	}
}

// startNear reports whether the calling device already has a start event for the track within two minutes of at
// (a scrobble with submission=false followed by the submission itself counts one play).
func (c *call) startNear(trackID string, at time.Time) (bool, error) {
	cat, err := c.cat()
	if err != nil {
		return false, err
	}
	const slack = 2 * time.Minute
	var n int
	err = cat.db.QueryRowContext(c.ctx, `SELECT COUNT(*) FROM listen_events WHERE track_id = ? AND principal_id = ? AND source = 'subsonic'
		AND kind = 'start' AND client_at BETWEEN ? AND ?`, trackID, c.principal.Key(),
		at.Add(-slack).UTC().Format(time.RFC3339), at.Add(slack).UTC().Format(time.RFC3339)).Scan(&n)
	return n > 0, err
}

// scrobble records `start` (submission=false) or `complete` (the default) per id (D-M4a-12).
func (c *call) scrobble() error {
	idsIn := c.all("id")
	if len(idsIn) == 0 {
		return fail(errMissingParam, "required parameter is missing: id")
	}
	times := c.all("time")
	submission := c.str("submission") != "false"
	now := c.s.now()
	client := c.principal.ID
	for i, id := range idsIn {
		row, err := c.songByID("id", id)
		if err != nil {
			return err
		}
		if c.s.play.seenWithin(client, row.ID, now, suppressWindow) {
			continue // a reportPlayback session already counted this play
		}
		at, timed := now, false
		if i < len(times) {
			if ms, err := strconv.ParseInt(times[i], 10, 64); err == nil && ms > 0 {
				at, timed = time.UnixMilli(ms), true
			}
		}
		format, err := c.listenFormat(row)
		if err != nil {
			return err
		}
		session := "ss-" + shortHash(client, at.UTC().Format("2006-01-02"))
		zero := int64(0)
		startAt := at
		if submission && !timed {
			// a submission without a time is sent at the end of the play: the start lies one duration earlier
			startAt = at.Add(-time.Duration(row.DurationS * float64(time.Second)))
		}
		emitStart := true
		if submission {
			// the catalog counts plays from start events, so a submission records the start too, unless a
			// submission=false call already did
			near, err := c.startNear(row.ID, startAt)
			if err != nil {
				return err
			}
			emitStart = !near
		}
		if emitStart {
			ev := listening.Event{
				ID: eventID(client, row.ID, "start", strconv.FormatInt(startAt.UnixMilli(), 10)), TrackID: row.ID,
				SessionID: session, Kind: "start", FromMS: &zero, Format: &format, ClientAt: startAt,
			}
			if err := c.logEvent(ev); err != nil {
				return err
			}
		}
		if submission {
			ev := listening.Event{
				ID: eventID(client, row.ID, "complete", strconv.FormatInt(at.UnixMilli(), 10)), TrackID: row.ID,
				SessionID: session, Kind: "complete", Format: &format, ClientAt: at,
			}
			if err := c.logEvent(ev); err != nil {
				return err
			}
		}
	}
	return nil
}

// seekSlackMS is how far a `playing` position may run ahead of wall-clock playback before it counts as a seek.
const seekSlackMS = 2000

var playbackStates = map[string]bool{"starting": true, "playing": true, "paused": true, "stopped": true}

// reportPlayback maps the OpenSubsonic playbackReport extension onto listen events (D-M4a-12): `starting` is a
// `start`, a `paused` or `stopped` after `playing` is a `segment`, a `stopped` at 90 % or more also a `complete`.
func (c *call) reportPlayback() error {
	if c.str("mediaId") == "" {
		return fail(errMissingParam, "required parameter is missing: mediaId")
	}
	state := c.str("state")
	if state == "" {
		return fail(errMissingParam, "required parameter is missing: state")
	}
	if !playbackStates[state] {
		return fail(errGeneric, "unsupported playback state")
	}
	if mt := c.str("mediaType"); mt != "" && mt != "song" {
		return nil // podcasts and the like are not tracked
	}
	pos := int64(0)
	if v := c.str("positionMs"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return fail(errGeneric, "invalid position")
		}
		pos = n
	}
	row, err := c.songByID("mediaId", c.str("mediaId"))
	if err != nil {
		return err
	}
	dur := int64(row.DurationS*1000 + 0.5)
	pos = min(pos, dur)
	client, now := c.principal.ID, c.s.now()
	key := playKey(client, row.ID)
	rate := 1.0
	if v, err := strconv.ParseFloat(c.str("playbackRate"), 64); err == nil && v > 0 && v <= 8 {
		rate = v
	}
	ps := c.s.play
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.seen.put(key, now) // every accepted report, ignoreScrobble included, keeps scrobbles for this pair away
	if c.str("ignoreScrobble") == "true" {
		return nil
	}
	format, err := c.listenFormat(row)
	if err != nil {
		return err
	}
	var pb *playback
	if v, ok := ps.sessions.get(key); ok {
		pb = v.(*playback)
	}
	newSession := func() *playback {
		return &playback{session: "rp-" + shortHash(client, row.ID, now.UTC().Format(time.RFC3339Nano))}
	}
	emit := func(kind string, from, to *int64) error {
		return c.logEvent(listening.Event{
			ID:      eventID(client, row.ID, kind, pb.session, fmt.Sprintf("%d-%d", deref(from), deref(to))),
			TrackID: row.ID, SessionID: pb.session, Kind: kind, FromMS: from, ToMS: to, Format: &format, ClientAt: now,
		})
	}
	switch state {
	case "starting":
		pb = newSession()
		pb.resume = pos
		ps.sessions.put(key, pb)
		return emit("start", &pos, nil)
	case "playing":
		if pb == nil {
			pb = newSession()
		}
		if pb.playing {
			// a repeated report (heartbeat, or a position after a seek) closes the running segment: at the
			// reported position when it is what playback explains, else at the position playback reached
			expected := pb.resume + int64(float64(now.Sub(pb.at).Milliseconds())*pb.rate)
			end := pos
			if pos < pb.resume || pos > expected+seekSlackMS {
				end = min(max(expected, pb.resume), dur)
			}
			if end > pb.resume {
				from := pb.resume
				if err := emit("segment", &from, &end); err != nil {
					return err
				}
			}
		}
		pb.resume, pb.playing, pb.at, pb.rate = pos, true, now, rate
		ps.sessions.put(key, pb)
		return nil
	}
	// paused or stopped
	if pb == nil {
		pb = newSession()
	}
	if pb.playing && pos >= pb.resume {
		from := pb.resume
		if err := emit("segment", &from, &pos); err != nil {
			return err
		}
	}
	pb.playing = false
	if state == "paused" {
		ps.sessions.put(key, pb)
		return nil
	}
	ps.sessions.remove(key)
	if dur > 0 && pos*10 >= dur*9 {
		return emit("complete", nil, nil)
	}
	return nil
}

func deref(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}
