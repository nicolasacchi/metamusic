package judgments

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// OrphanRater is a rater key with human rating rows that no configured user answers to: the user was removed from the
// configuration after rating (D-U1-4). The rows stay inert and readable by the Owner; they never stop startup (C301.10).
type OrphanRater struct {
	Name string
	Rows int
}

// OrphanRaters lists the human raters other than the Owner whose name is not accepted by known, sorted by name. known is
// the configured user map's membership test (names are stored lowercase, like the configuration's keys).
func OrphanRaters(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, known func(string) bool) ([]OrphanRater, error) {
	rows, err := q.QueryContext(ctx, `SELECT rater, COUNT(*) FROM ratings WHERE source = 'human' AND rater <> 'owner' GROUP BY rater ORDER BY rater`)
	if err != nil {
		return nil, fmt.Errorf("judgments: orphan raters: %w", err)
	}
	defer rows.Close()
	var out []OrphanRater
	for rows.Next() {
		var o OrphanRater
		if err := rows.Scan(&o.Name, &o.Rows); err != nil {
			return nil, err
		}
		if !known(o.Name) {
			out = append(out, o)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b OrphanRater) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
