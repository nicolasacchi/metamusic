package librarytest

import (
	"context"
	"database/sql"
)

// hasRater reports whether the ratings table has the rater column (schema 17 and later, U1-R1). The fixtures also run on
// older schemas for the upgrade tests, so they write the rater only where it exists.
func hasRater(q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) bool {
	var n int
	if err := q.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pragma_table_info('ratings') WHERE name = 'rater'`).Scan(&n); err != nil {
		return false
	}
	return n == 1
}

// raterSQL returns the extra column list and values (to append after the last column and the last value of a ratings
// INSERT) that attribute a fixture rating: 'owner' for the human source, 'critic' for the agent one.
func raterSQL(has bool, source string) (col, val string) {
	switch {
	case !has:
		return "", ""
	case source == "agent":
		return ",rater", ",'critic'"
	}
	return ",rater", ",'owner'"
}

// raterConflict is the upsert target of a ratings row on the given schema.
func raterConflict(has bool) string {
	if has {
		return "track_id, source, rater"
	}
	return "track_id, source"
}
