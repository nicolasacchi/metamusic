package nextactions

import (
	"context"
	"fmt"
	"strings"
)

// names resolves the display names of rule texts for one call. Song and Record titles are the operator's own and are read in
// one statement per kind; a Muse is named by the first eight characters of its id until the operator approves Muse titles in
// item texts (D-L3-16, not approved): the id prefix is all this package reveals, so no Muse text is read at all.
type names struct {
	songs, records map[string]string
}

const museIDPrefix = 8

func prefix(id string) string {
	if len(id) > museIDPrefix {
		return id[:museIDPrefix]
	}
	return id
}

// Muse returns the noun phrase for a Muse as the text templates place it ("Muse 019a1b2c").
func (n *names) Muse(id string) string { return "Muse " + prefix(id) }

// Song returns the Song title, or "a Song" for an id that no longer resolves.
func (n *names) Song(id string) string {
	if t, ok := n.songs[id]; ok {
		return t
	}
	return "a Song"
}

// Record returns the Record title, or "a Record" for an id that no longer resolves.
func (n *names) Record(id string) string {
	if t, ok := n.records[id]; ok {
		return t
	}
	return "a Record"
}

// load fills dst with the titles of ids from table ("songs" or "records") in one statement. An empty id list costs nothing.
func (n *names) load(ctx context.Context, q Reader, table string, ids []string, dst map[string]string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT id, title FROM `+table+` WHERE id IN (SELECT value FROM json_each(?))`, jsonIDs(ids))
	if err != nil {
		return fmt.Errorf("nextactions: %s titles: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return err
		}
		dst[id] = strings.TrimSpace(title)
	}
	return rows.Err()
}
