package atlas

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// layoutCacheTTL bounds how long a laid-out scope stays in memory; it is never a freshness rule, because a changed scope has
// a different token and misses the cache (C141.3).
const layoutCacheTTL = 60 * time.Second

// token is sha256(x, y, color_by, the filter, dim, taxonomy version, the ordered row tuples). rows must be in id order. The
// tuple of a row holds everything that decides membership, order, coordinates, flags, title or colour: a change to any of
// them is a different map, a change to a Song outside the scope is not.
func token(q Query, taxVersion int, rows []row, colour func(row) string) (string, error) {
	f := q.Filter
	f.Limit, f.Cursor = 0, "" // paging is not part of the map
	fj, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	// Every field is length-prefixed, so no two field sequences hash alike; a row is one write.
	var buf []byte
	str := func(s string) {
		buf = binary.AppendUvarint(buf, uint64(len(s)))
		buf = append(buf, s...)
	}
	num := func(n int) { buf = binary.AppendVarint(buf, int64(n)) }
	str(Def)
	str(q.X)
	str(q.Y)
	str(q.ColorBy)
	str(string(fj))
	if q.Dim {
		str("dim")
	} else {
		str("flat")
	}
	num(taxVersion)
	h.Write(buf) //nolint:errcheck // a hash.Hash never fails
	for _, r := range rows {
		buf = buf[:0]
		str(r.id)
		str(r.f.digest.String)
		num(r.h)
		num(r.st)
		num(r.pk)
		num(r.rec)
		str(r.muse)
		str(colour(r))
		str(r.title)
		h.Write(buf) //nolint:errcheck // as above
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func badAtlasCursor() error {
	return problem.E(http.StatusUnprocessableEntity, "INVALID_CURSOR", "the cursor is not one this operation issued", nil)
}

func stale() error {
	return problem.E(http.StatusConflict, "ATLAS_STALE", "the map changed since the first page; start again without a cursor", nil)
}

func encodeCursor(token, lastID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(token + ":" + lastID))
}

func decodeCursor(c string) (token, lastID string, err error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", "", badAtlasCursor()
	}
	token, lastID, ok := strings.Cut(string(b), ":")
	if _, herr := hex.DecodeString(token); !ok || len(token) != 64 || herr != nil || lastID == "" {
		return "", "", badAtlasCursor()
	}
	return token, lastID, nil
}

// laid is a cached layout: the positions of the plotted points in id order.
type laid struct {
	xy []XY
	at time.Time
}

// layoutCache keeps the last LayoutCacheEntries layouts keyed by layout_token.
type layoutCache struct {
	mu      sync.Mutex
	entries map[string]laid
}

func (c *layoutCache) get(token string, now time.Time) ([]XY, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[token]
	if !ok || now.Sub(e.at) > layoutCacheTTL {
		delete(c.entries, token)
		return nil, false
	}
	return e.xy, true
}

func (c *layoutCache) put(token string, xy []XY, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]laid{}
	}
	c.entries[token] = laid{xy: xy, at: now}
	for len(c.entries) > LayoutCacheEntries {
		keys := make([]string, 0, len(c.entries))
		for k := range c.entries {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return c.entries[a].at.Compare(c.entries[b].at) })
		delete(c.entries, keys[0])
	}
}
