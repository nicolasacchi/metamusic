package backup

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/schedule"
)

const (
	nameLayout    = "20060102T150405Z"
	partialMaxAge = 24 * time.Hour
	keepDays      = 14
	keepWeeks     = 8
)

// snapshotNameRE matches "<yyyymmddThhmmssZ>" with an optional "-N" (N >= 2).
var snapshotNameRE = regexp.MustCompile(`^(\d{8}T\d{6}Z)(?:-([2-9]|[1-9]\d+))?$`)

// parseName parses a snapshot directory name into its instant and suffix
// number (1 when there is no suffix).
func parseName(name string) (t time.Time, n int, ok bool) {
	m := snapshotNameRE.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, 0, false
	}
	t, err := time.Parse(nameLayout, m[1])
	if err != nil {
		return time.Time{}, 0, false
	}
	n = 1
	if m[2] != "" {
		n, _ = strconv.Atoi(m[2])
	}
	return t, n, true
}

// parsePartial parses ".<snapshot name>.partial".
func parsePartial(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".partial") {
		return time.Time{}, false
	}
	t, _, ok := parseName(strings.TrimSuffix(name[1:], ".partial"))
	return t, ok
}

// SnapshotName returns "<yyyymmddThhmmssZ>", then "-2", "-3", ... while taken.
func SnapshotName(t time.Time, exists func(name string) bool) string {
	base := t.UTC().Format(nameLayout)
	if !exists(base) {
		return base
	}
	for n := 2; ; n++ {
		if c := base + "-" + strconv.Itoa(n); !exists(c) {
			return c
		}
	}
}

type snap struct {
	name string
	t    time.Time
	n    int
}

func (a snap) before(b snap) bool {
	if !a.t.Equal(b.t) {
		return a.t.Before(b.t)
	}
	return a.n < b.n
}

// Retain splits snapshot names into those to keep and to drop. It keeps the
// newest snapshot of each of the last 14 calendar days and of each of the last
// 8 ISO weeks (in loc), and always the newest one. A ".<name>.partial" is
// dropped once its name is older than 24 h and kept before; names that do not
// parse are in neither list. Both lists are sorted.
func Retain(names []string, now time.Time, loc *time.Location) (keep, drop []string) {
	var snaps []snap
	for _, nm := range names {
		if t, n, ok := parseName(nm); ok {
			snaps = append(snaps, snap{nm, t, n})
		} else if t, ok := parsePartial(nm); ok {
			if now.Sub(t) > partialMaxAge {
				drop = append(drop, nm)
			} else {
				keep = append(keep, nm)
			}
		}
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].before(snaps[j]) })
	kept := map[string]bool{}
	if len(snaps) > 0 {
		kept[snaps[len(snaps)-1].name] = true
	}
	local := now.In(loc)
	dayCut := time.Date(local.Year(), local.Month(), local.Day()-(keepDays-1), 0, 0, 0, 0, loc)
	weekCut := weekStart(local, loc).AddDate(0, 0, -7*(keepWeeks-1))
	days, weeks := map[time.Time]string{}, map[time.Time]string{}
	for _, s := range snaps { // ascending, so the last write per bucket is the newest
		l := s.t.In(loc)
		days[time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)] = s.name
		weeks[weekStart(l, loc)] = s.name
	}
	for d, nm := range days {
		if !d.Before(dayCut) {
			kept[nm] = true
		}
	}
	for w, nm := range weeks {
		if !w.Before(weekCut) {
			kept[nm] = true
		}
	}
	for _, s := range snaps {
		if kept[s.name] {
			keep = append(keep, s.name)
		} else {
			drop = append(drop, s.name)
		}
	}
	sort.Strings(keep)
	sort.Strings(drop)
	return keep, drop
}

// weekStart is midnight of the Monday of t's ISO week in loc.
func weekStart(t time.Time, loc *time.Location) time.Time {
	off := (int(t.Weekday()) + 6) % 7
	return time.Date(t.Year(), t.Month(), t.Day()-off, 0, 0, 0, 0, loc)
}

// NextRun returns the first instant after now whose local wall clock is at ("HH:MM"); it delegates to schedule.NextRun, where the
// DST rules live. A bad or "off" at gives the zero time.
func NextRun(now time.Time, at string, loc *time.Location) time.Time {
	return schedule.NextRun(now, at, loc)
}

// Scheduler runs Run daily at At in Loc.
type Scheduler struct {
	At  string
	Loc *time.Location
	Run func(context.Context)
}

// Loop sleeps until the next run, runs, and repeats until ctx ends. With At
// "off" (or unparsable) it only waits for ctx.
func (s *Scheduler) Loop(ctx context.Context) {
	loc := s.Loc
	if loc == nil {
		loc = time.Local
	}
	for {
		next := NextRun(time.Now(), s.At, loc)
		if next.IsZero() {
			<-ctx.Done()
			return
		}
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		s.Run(ctx)
	}
}

// retentionLoc is the time zone calendar days and ISO weeks are counted in.
func retentionLoc() *time.Location { return time.Local }
