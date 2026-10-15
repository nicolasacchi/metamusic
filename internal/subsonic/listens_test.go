package subsonic

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type evRow struct {
	ID, Session, Kind, Source, PKind, PID string
	From, To                              sql.NullInt64
	Format, Device                        sql.NullString
}

func (r *rig) events(track string) []evRow {
	r.t.Helper()
	rows, err := r.st.R.Query(`SELECT id, session_id, kind, source, principal_kind, principal_id, from_ms, to_ms, format, device_id
		FROM listen_events WHERE track_id = ? ORDER BY received_at, kind DESC, from_ms, id`, track)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []evRow
	for rows.Next() {
		var e evRow
		if err := rows.Scan(&e.ID, &e.Session, &e.Kind, &e.Source, &e.PKind, &e.PID, &e.From, &e.To, &e.Format, &e.Device); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func kinds(evs []evRow) string {
	var out []string
	for _, e := range evs {
		s := e.Kind
		if e.From.Valid && e.To.Valid {
			s += fmt.Sprintf("[%d,%d]", e.From.Int64, e.To.Int64)
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func TestSubsonic_ScrobbleListenEvents(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	ms := strconv.FormatInt(x.clk.now().Add(-time.Minute).UnixMilli(), 10)
	eq(t, "start", x.callCode("scrobble", "id", luna, "time", ms, "submission", "false"), 0)
	evs := x.events(luna)
	eq(t, "one start", kinds(evs), "start")
	e := evs[0]
	eq(t, "source", e.Source, "subsonic")
	eq(t, "principal kind", e.PKind, "device")
	eq(t, "principal id", e.PID, "device:"+x.rw.ID)
	eq(t, "from_ms", e.From.Int64, 0)
	eq(t, "from valid", e.From.Valid, true)
	eq(t, "complete", x.callCode("scrobble", "id", luna, "time", ms), 0)
	eq(t, "start+complete", len(x.events(luna)), 2)
	// the identical calls again record nothing new (deterministic ids)
	eq(t, "retry start", x.callCode("scrobble", "id", luna, "time", ms, "submission", "false"), 0)
	eq(t, "retry complete", x.callCode("scrobble", "id", luna, "time", ms, "submission", "true"), 0)
	eq(t, "still 2", len(x.events(luna)), 2)
	// without a time the clock decides; a retry at the same instant dedupes too
	eq(t, "no time", x.callCode("scrobble", "id", citta), 0)
	eq(t, "no time retry", x.callCode("scrobble", "id", citta), 0)
	eq(t, "citta", kinds(x.events(citta)), "complete")
	// several ids, each with its own time
	t2 := strconv.FormatInt(x.clk.now().Add(-2*time.Minute).UnixMilli(), 10)
	eq(t, "many", x.callCode("scrobble", "id", twinA, "time", ms, "id", twinB, "time", t2), 0)
	eq(t, "twinA", kinds(x.events(twinA)), "complete")
	eq(t, "twinB", kinds(x.events(twinB)), "complete")
	// errors
	eq(t, "unknown", x.callCode("scrobble", "id", "no-such"), 70)
	eq(t, "missing", x.callCode("scrobble"), 10)
	eq(t, "read-only", x.get("scrobble", keyParams(x.roKey, "id", luna), libIP).code(t), 50)
}

var (
	ssRE = regexp.MustCompile(`^ss-[0-9a-f]{16}$`)
	rpRE = regexp.MustCompile(`^rp-[0-9a-f]{16}$`)
)

func TestSubsonic_ListenEventFields(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.writeFixtureFiles(t)
	other := x.newClient(t, "other", true, true)
	// sessions: one per client per UTC day
	x.callCode("scrobble", "id", luna)
	x.callCode("scrobble", "id", citta)
	s1 := x.events(luna)[0].Session
	if !ssRE.MatchString(s1) {
		t.Fatalf("scrobble session %q", s1)
	}
	eq(t, "same day, same client", x.events(citta)[0].Session, s1)
	x.clk.advance(24 * time.Hour)
	x.callCode("scrobble", "id", twinA)
	if s := x.events(twinA)[0].Session; s == s1 || !ssRE.MatchString(s) {
		t.Errorf("next day session %q (day one %q)", s, s1)
	}
	x.get("scrobble", keyParams(other, "id", twinB), libIP)
	if s := x.events(twinB)[0].Session; s == s1 || s == x.events(twinA)[0].Session || !ssRE.MatchString(s) {
		t.Errorf("other client session %q", s)
	}
	// reportPlayback sessions: rp- + 16 hex, 19 characters, inside M2a's 8-64 CHECK
	x.callCode("reportPlayback", "mediaId", short, "state", "starting", "positionMs", "0")
	rp := x.events(short)[0]
	if !rpRE.MatchString(rp.Session) || len(rp.Session) != 19 {
		t.Errorf("reportPlayback session %q", rp.Session)
	}
}

func TestSubsonic_ListenEventFormats(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	x.writeFixtureFiles(t)
	other := x.newClient(t, "other", true, true)
	// the last streamed format wins, else the client default; device_id stays NULL
	statusOK(t, x.raw("GET", "stream", x.rwq("id", luna, "format", "raw"), nil), 200)
	statusOK(t, x.raw("GET", "stream", x.rwq("id", citta, "format", "mp3"), nil), 200)
	statusOK(t, x.raw("GET", "stream", x.rwq("id", short, "format", "mp3"), nil), 200)
	statusOK(t, x.raw("GET", "stream", keyParams(other, "id", short, "format", "flac"), nil), 200)
	x.callCode("scrobble", "id", luna, "submission", "false")
	x.callCode("scrobble", "id", citta, "submission", "false")
	x.callCode("scrobble", "id", twinA, "submission", "false")
	eq(t, "after raw", x.events(luna)[0].Format.String, "flac")
	eq(t, "after mp3", x.events(citta)[0].Format.String, "mp3")
	eq(t, "default raw", x.events(twinA)[0].Format.String, "flac")
	x.get("scrobble", keyParams(other, "id", twinB, "submission", "false"), libIP)
	eq(t, "default mp3", x.events(twinB)[0].Format.String, "mp3")
	// the memory is per client: the same track was streamed as mp3 by one client and as flac by the other
	x.get("scrobble", keyParams(other, "id", short, "submission", "false"), libIP)
	x.callCode("scrobble", "id", short, "submission", "false")
	byPID := map[string]string{}
	for _, e := range x.events(short) {
		byPID[e.PID] = e.Format.String
	}
	eq(t, "rw client", byPID["device:"+x.rw.ID], "mp3")
	eq(t, "other client", len(byPID), 2)
	for pid, f := range byPID {
		if pid != "device:"+x.rw.ID {
			eq(t, "other client format", f, "flac")
		}
	}
	for _, tr := range []string{luna, citta, twinA, twinB, short} {
		for _, e := range x.events(tr) {
			if e.Device.Valid {
				t.Errorf("device_id set: %+v", e)
			}
		}
	}
}

func TestSubsonic_ReportPlaybackSegments(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t) // luna lasts 12 s
	rp := func(track, state, pos string, kv ...string) int {
		return x.callCode("reportPlayback", append([]string{"mediaId", track, "state", state, "positionMs", pos}, kv...)...)
	}
	eq(t, "starting", rp(luna, "starting", "0"), 0)
	eq(t, "playing", rp(luna, "playing", "0"), 0)
	eq(t, "after playing", kinds(x.events(luna)), "start")
	eq(t, "paused", rp(luna, "paused", "6000"), 0)
	eq(t, "start + segment", kinds(x.events(luna)), "start segment[0,6000]")
	// resume, play on, stop near the end: a segment and a completion
	eq(t, "resume", rp(luna, "playing", "6000"), 0)
	eq(t, "stopped", rp(luna, "stopped", "11000"), 0)
	eq(t, "full", kinds(x.events(luna)), "start segment[0,6000] segment[6000,11000] complete")
	// stopped early: segment only
	rp(citta, "starting", "0")
	rp(citta, "playing", "0")
	rp(citta, "stopped", "2000")
	eq(t, "early stop", kinds(x.events(citta)), "start segment[0,2000]")
	// a duplicate report changes nothing
	rp(citta, "stopped", "2000")
	eq(t, "duplicate", kinds(x.events(citta)), "start segment[0,2000]")
	// ignoreScrobble and other media types write nothing
	eq(t, "ignore", rp(twinA, "starting", "0", "ignoreScrobble", "true"), 0)
	rp(twinA, "playing", "0", "ignoreScrobble", "true")
	rp(twinA, "stopped", "7500", "ignoreScrobble", "true")
	eq(t, "no events", len(x.events(twinA)), 0)
	eq(t, "podcast", rp(twinB, "starting", "0", "mediaType", "podcast"), 0)
	eq(t, "no podcast events", len(x.events(twinB)), 0)
	// errors
	eq(t, "unknown track", rp("no-such", "starting", "0"), 70)
	eq(t, "missing id", x.callCode("reportPlayback", "state", "starting"), 10)
	eq(t, "missing state", x.callCode("reportPlayback", "mediaId", luna), 10)
	eq(t, "bad state", rp(luna, "dancing", "0"), 0)
	eq(t, "read-only", x.get("reportPlayback", keyParams(x.roKey, "mediaId", luna, "state", "starting"), libIP).code(t), 50)
	// a playing report with no starting reuses the report time for its session
	rp(short, "playing", "1000")
	rp(short, "paused", "3000")
	eq(t, "playing alone", kinds(x.events(short)), "segment[1000,3000]")
	if s := x.events(short)[0].Session; !rpRE.MatchString(s) {
		t.Errorf("session %q", s)
	}
}

func TestSubsonic_ScrobbleSuppressedAfterReportPlayback(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t) // twinA lasts 8 s
	other := x.newClient(t, "other", false, true)
	x.callCode("reportPlayback", "mediaId", twinA, "state", "starting", "positionMs", "0")
	x.callCode("reportPlayback", "mediaId", twinA, "state", "stopped", "positionMs", "7600")
	eq(t, "report events", kinds(x.events(twinA)), "start complete")
	// within 30 minutes the scrobbles of the same pair are acknowledged but dropped
	x.clk.advance(10 * time.Minute)
	eq(t, "scrobble false", x.callCode("scrobble", "id", twinA, "submission", "false"), 0)
	eq(t, "scrobble true", x.callCode("scrobble", "id", twinA), 0)
	eq(t, "suppressed", kinds(x.events(twinA)), "start complete")
	// another track or another client is unaffected
	eq(t, "other track", x.callCode("scrobble", "id", citta), 0)
	eq(t, "citta recorded", kinds(x.events(citta)), "complete")
	if w := x.get("scrobble", keyParams(other, "id", twinA), libIP); w.code(t) != 0 {
		t.Fatal(w.Body)
	}
	eq(t, "client B recorded", len(x.events(twinA)), 3)
	// the window ends 30 minutes after the report
	x.clk.advance(21 * time.Minute)
	eq(t, "after 31 min", x.callCode("scrobble", "id", twinA), 0)
	eq(t, "recorded again", len(x.events(twinA)), 4)
	// ignoreScrobble also stamps "seen"; another media type does not
	x.callCode("reportPlayback", "mediaId", twinB, "state", "starting", "positionMs", "0", "ignoreScrobble", "true")
	x.callCode("scrobble", "id", twinB)
	eq(t, "ignoreScrobble suppresses scrobbles", len(x.events(twinB)), 0)
	x.callCode("reportPlayback", "mediaId", short, "state", "starting", "positionMs", "0", "mediaType", "podcast")
	x.callCode("scrobble", "id", short)
	eq(t, "podcast does not", kinds(x.events(short)), "complete")
}

func TestSubsonic_PlayStateIsBounded(t *testing.T) {
	t.Parallel()
	p := newPlayState()
	for i := range 3 * playStateLimit {
		k := "c|" + strconv.Itoa(i)
		p.seen.put(k, time.Unix(int64(i), 0))
		p.streamed.put(k, "flac")
		p.sessions.put(k, &playback{})
	}
	for name, n := range map[string]int{"seen": p.seen.len(), "streamed": p.streamed.len(), "sessions": p.sessions.len()} {
		eq(t, name, n, playStateLimit)
	}
	if _, ok := p.streamed.get("c|0"); ok {
		t.Error("the oldest entry survived")
	}
	if _, ok := p.streamed.get("c|" + strconv.Itoa(3*playStateLimit-1)); !ok {
		t.Error("the newest entry is gone")
	}
}
