package export

import (
	"encoding/json"
	"sort"
	"strings"
)

// StyleStats reports what DetectStyle saw.
type StyleStats struct {
	Lines, Scanned         int // input lines, lines the scanner understood
	EscapeVotes, RawVotes  int // ensure_ascii votes (lines with only \u>=0x80 escapes / only raw bytes)
	CommaItem, CompactItem int
	SpaceKey, CompactKey   int
	AllSorted              bool
	DistinctKeyOrders      int
}

// DefaultStyle is used when no data decides: sorted keys, Python separators, ensure_ascii=false.
func DefaultStyle() Style { return Style{ItemSep: ", ", KeySep: ": ", EnsureASCII: false} }

// DetectStyle infers the writing style of an existing JSONL corpus. Each attribute takes the strict
// majority; ties and no data fall back to DefaultStyle.
func DetectStyle(raws [][]byte) (Style, StyleStats) {
	var st StyleStats
	st.Lines = len(raws)
	st.AllSorted = true
	orders := map[string]int{}
	orderKeys := map[string][]string{}
	for _, raw := range raws {
		sc := scanLine(raw)
		if !sc.ok {
			continue
		}
		st.Scanned++
		key := strings.Join(sc.keys, "\x00")
		orders[key]++
		orderKeys[key] = sc.keys
		if !sort.StringsAreSorted(sc.keys) {
			st.AllSorted = false
		}
		for _, s := range sc.itemSeps {
			switch s {
			case ", ":
				st.CommaItem++
			case ",":
				st.CompactItem++
			}
		}
		for _, s := range sc.keySeps {
			switch s {
			case ": ":
				st.SpaceKey++
			case ":":
				st.CompactKey++
			}
		}
		switch {
		case sc.escape && !sc.raw:
			st.EscapeVotes++
		case sc.raw && !sc.escape:
			st.RawVotes++
		}
	}
	st.DistinctKeyOrders = len(orders)
	s := DefaultStyle()
	if st.CompactItem > st.CommaItem {
		s.ItemSep = ","
	}
	if st.CompactKey > st.SpaceKey {
		s.KeySep = ":"
	}
	s.EnsureASCII = st.EscapeVotes > st.RawVotes
	if !st.AllSorted {
		best, bestN, tie := "", 0, false
		for k, n := range orders {
			switch {
			case n > bestN:
				best, bestN, tie = k, n, false
			case n == bestN:
				tie = true
			}
		}
		if !tie {
			if ko := orderKeys[best]; !sort.StringsAreSorted(ko) {
				s.KeyOrder = ko
			}
		}
	}
	return s, st
}

type lineScan struct {
	ok          bool
	keys        []string
	itemSeps    []string
	keySeps     []string
	escape, raw bool
}

func isWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// scanLine reads the top-level key sequence, separators and ensure_ascii evidence of one JSON object.
func scanLine(raw []byte) lineScan {
	var sc lineScan
	for _, c := range raw {
		if c >= 0x80 {
			sc.raw = true
			break
		}
	}
	i, n := 0, len(raw)
	skipWS := func() {
		for i < n && isWS(raw[i]) {
			i++
		}
	}
	skipWS()
	if i >= n || raw[i] != '{' {
		return sc
	}
	i++
	// strEnd returns the index just past the string starting at j (raw[j] == '"'), noting \u escapes.
	strEnd := func(j int) int {
		j++
		for j < n {
			switch raw[j] {
			case '\\':
				if j+1 < n && raw[j+1] == 'u' && j+6 <= n {
					var v int
					for _, h := range raw[j+2 : j+6] {
						v <<= 4
						switch {
						case h >= '0' && h <= '9':
							v |= int(h - '0')
						case h >= 'a' && h <= 'f':
							v |= int(h-'a') + 10
						case h >= 'A' && h <= 'F':
							v |= int(h-'A') + 10
						}
					}
					if v >= 0x80 {
						sc.escape = true
					}
					j += 6
					continue
				}
				j += 2
			case '"':
				return j + 1
			default:
				j++
			}
		}
		return -1
	}
	skipValue := func() bool {
		if i >= n {
			return false
		}
		switch raw[i] {
		case '"':
			e := strEnd(i)
			if e < 0 {
				return false
			}
			i = e
		case '{', '[':
			depth := 0
			for i < n {
				switch raw[i] {
				case '"':
					e := strEnd(i)
					if e < 0 {
						return false
					}
					i = e
					continue
				case '{', '[':
					depth++
				case '}', ']':
					depth--
				}
				i++
				if depth == 0 {
					return true
				}
			}
			return false
		default:
			for i < n && !isWS(raw[i]) && raw[i] != ',' && raw[i] != '}' {
				i++
			}
		}
		return true
	}
	skipWS()
	if i < n && raw[i] == '}' {
		return sc // an empty object carries no evidence
	}
	for {
		if i >= n || raw[i] != '"' {
			return lineScan{}
		}
		ke := strEnd(i)
		if ke < 0 {
			return lineScan{}
		}
		var key string
		if json.Unmarshal(raw[i:ke], &key) != nil {
			return lineScan{}
		}
		sc.keys = append(sc.keys, key)
		i = ke
		ks := i
		skipWS()
		if i >= n || raw[i] != ':' {
			return lineScan{}
		}
		i++
		skipWS()
		sc.keySeps = append(sc.keySeps, string(raw[ks:i]))
		if !skipValue() {
			return lineScan{}
		}
		skipWS()
		if i >= n {
			return lineScan{}
		}
		switch raw[i] {
		case '}':
			sc.ok = true
			return sc
		case ',':
			sepStart := i
			i++
			skipWS()
			sc.itemSeps = append(sc.itemSeps, string(raw[sepStart:i]))
		default:
			return lineScan{}
		}
	}
}
