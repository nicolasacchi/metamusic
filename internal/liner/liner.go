package liner

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// Build renders in. It is pure: the same input gives the same output, and nothing reads the clock.
//
// The audiences differ by construction (C124.1, D-L2-16, D-L2-27): for AudienceExport Build never reads CostMicroUSD,
// SessionName or Muse (the last unless MuseNames), whatever the caller supplied, and slots without a playable take are not
// part of the notes. AudienceApp prints cost, session name and Muse title/artist. Any other audience is treated as export,
// the audience that leaks nothing.
func Build(in Input) Liner {
	app := in.Audience == AudienceApp
	museOK := app || in.MuseNames
	l := Liner{Title: oneLine(in.Title), subtitle: oneLine(in.Subtitle), date: oneLine(in.Date), note: strings.TrimSpace(in.Note)}

	var playable []Track
	empty := 0
	for _, t := range in.Tracks {
		if t.DurationS > 0 {
			playable = append(playable, t)
		} else {
			empty++
		}
	}
	songs := len(playable)
	if app {
		songs = len(in.Tracks)
	}

	var total float64
	engines, angles := map[string]bool{}, map[string]bool{}
	famN := map[string]int{}
	var lossyN, resampled, upmixed int
	var cost int64
	for _, t := range playable {
		total += t.DurationS
		if e := oneLine(t.Engine); e != "" {
			engines[e] = true
		}
		if a := oneLine(t.Angle); a != "" {
			angles[a] = true
		}
		for _, f := range uniq(t.Families) {
			famN[f]++
		}
		if t.Lossy {
			lossyN++
		}
		if t.Resampled {
			resampled++
		}
		if t.Upmixed {
			upmixed++
		}
		if app && t.CostMicroUSD != nil {
			cost += *t.CostMicroUSD
		}
	}
	l.Summary = Summary{
		Songs: songs, Playable: len(playable), Duration: clock(total),
		Engines: keys(engines), Angles: keys(angles), Families: topFamilies(famN, 3),
		ContainsLossy: lossyN > 0, LossyN: lossyN,
	}

	if lossyN > 0 {
		l.Notices = append(l.Notices, "Contains lossy sources ("+strconv.Itoa(lossyN)+" of "+strconv.Itoa(len(playable))+" tracks).")
	}
	if resampled > 0 {
		l.Notices = append(l.Notices, plural(resampled, "track")+" resampled to 48 kHz")
	}
	if upmixed > 0 {
		l.Notices = append(l.Notices, plural(upmixed, "track")+" mono, duplicated to stereo")
	}
	if app && empty > 0 {
		if empty == 1 {
			l.Notices = append(l.Notices, "1 slot has no playable take yet")
		} else {
			l.Notices = append(l.Notices, strconv.Itoa(empty)+" slots have no playable take yet")
		}
	}

	for _, t := range in.Tracks {
		switch {
		case t.DurationS > 0:
			l.Lines = append(l.Lines, Line{Position: t.Position, Text: lineText(t, app, museOK)})
		case app:
			l.Lines = append(l.Lines, Line{Position: t.Position, Text: oneLine(sidePrefix(t) + "(no playable take yet)")})
		}
	}
	// The cost total is a footer (Position 0, not a track), so Description never carries it.
	if app && hasCost(playable) {
		l.Lines = append(l.Lines, Line{Position: 0, Text: "Total cost: " + usd(cost)})
	}
	return l
}

func lineText(t Track, app, museOK bool) string {
	var parts []string
	add := func(s string) {
		if s = oneLine(s); s != "" {
			parts = append(parts, s)
		}
	}
	add(t.Angle)
	add(t.Engine)
	add(clock(t.DurationS))
	if t.BPM != nil {
		bpm := strconv.Itoa(*t.BPM) + " BPM"
		switch t.BPMBasis {
		case "declared", "plan":
			bpm += " (" + t.BPMBasis + ")"
		}
		add(bpm)
	}
	add(strings.Join(uniq(t.Families), ", "))
	why := oneLine(t.Why)
	if t.Mode == "pinned" && !strings.HasPrefix(why, "pinned") {
		why = strings.TrimSuffix("pinned · "+why, " · ")
	}
	add(why)
	if app {
		name := oneLine(t.SessionName)
		if name == "" {
			name = oneLine(t.SessionLabel)
		}
		if name != "" {
			add("session " + name)
		}
		if t.CostMicroUSD != nil {
			add(usd(*t.CostMicroUSD))
		}
	} else {
		add(t.SessionLabel)
	}
	if museOK && t.Muse != nil && oneLine(t.Muse.Title) != "" {
		m := "inspired by " + oneLine(t.Muse.Title)
		if t.Muse.Artist != nil && oneLine(*t.Muse.Artist) != "" {
			m += " — " + oneLine(*t.Muse.Artist)
		}
		add(m)
	}
	text := sidePrefix(t) + oneLine(t.Title)
	if len(parts) > 0 {
		text += " — " + strings.Join(parts, " · ")
	}
	return text
}

func sidePrefix(t Track) string {
	if s := oneLine(t.Side); s != "" {
		return "[" + s + "] "
	}
	return ""
}

func hasCost(ts []Track) bool {
	for _, t := range ts {
		if t.CostMicroUSD != nil {
			return true
		}
	}
	return false
}

// usd formats micro-USD as dollars and cents (half up) with integer arithmetic only.
func usd(micro int64) string {
	sign := ""
	if micro < 0 {
		sign, micro = "-", -micro
	}
	cents := (micro + 5000) / 10000
	c := strconv.FormatInt(cents%100, 10)
	if len(c) == 1 {
		c = "0" + c
	}
	return sign + "$" + strconv.FormatInt(cents/100, 10) + "." + c
}

// clock formats seconds as m:ss, or h:mm:ss from one hour; fractions round half up.
func clock(s float64) string {
	n := int64(math.Floor(max(s, 0) + 0.5))
	h, m, sec := n/3600, n/60%60, n%60
	two := func(v int64) string {
		if v < 10 {
			return "0" + strconv.FormatInt(v, 10)
		}
		return strconv.FormatInt(v, 10)
	}
	if h > 0 {
		return strconv.FormatInt(h, 10) + ":" + two(m) + ":" + two(sec)
	}
	return strconv.FormatInt(m, 10) + ":" + two(sec)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// oneLine collapses every run of whitespace (newlines included) so operator text cannot start a new Markdown block.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = oneLine(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// topFamilies returns the n most frequent families (ties by name), sorted by name.
func topFamilies(count map[string]int, n int) []string {
	all := make([]string, 0, len(count))
	for f := range count {
		all = append(all, f)
	}
	sort.Slice(all, func(i, j int) bool {
		if count[all[i]] != count[all[j]] {
			return count[all[i]] > count[all[j]]
		}
		return all[i] < all[j]
	})
	if len(all) > n {
		all = all[:n]
	}
	sort.Strings(all)
	return all
}
