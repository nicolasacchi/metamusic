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
	resume  int64 // position of the last `playing` report
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

// logEvent stores one listen event as the calling device; a rejected event is dropped silently (the client could
// do nothing about it).
func (c *call) logEvent(ev listening.Event) error {
	lst, err := c.listener()
	if err != nil {
		return err
	}
	_, _, _, err = lst.Log(c.ctx, c.principal, "subsonic", []listening.Event{ev})
	return err
}

// scrobble records `start` (submission=false) or `complete` (the default) per id (D-M4a-12).
func (c *call) scrobble() error {
	idsIn := c.all("id")
	if len(idsIn) == 0 {
		return fail(errMissingParam, "required parameter is missing: id")
	}
	times := c.all("time")
	kind := "complete"
	if c.str("submission") == "false" {
		kind = "start"
	}
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
		at := now
		if i < len(times) {
			if ms, err := strconv.ParseInt(times[i], 10, 64); err == nil && ms > 0 {
				at = time.UnixMilli(ms)
			}
		}
		format, err := c.listenFormat(row)
		if err != nil {
			return err
		}
		ev := listening.Event{
			ID: eventID(client, row.ID, kind, strconv.FormatInt(at.UnixMilli(), 10)), TrackID: row.ID,
			SessionID: "ss-" + shortHash(client, at.UTC().Format("2006-01-02")), Kind: kind, Format: &format, ClientAt: at,
		}
		if kind == "start" {
			zero := int64(0)
			ev.FromMS = &zero
		}
		if err := c.logEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

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
		pb.resume, pb.playing = pos, true
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
