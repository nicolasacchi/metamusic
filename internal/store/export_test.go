package store

import "database/sql"

// MigrateTo is the test hook for partial migration (upgrade tests).
var MigrateTo = migrateTo

// OpenRaw opens a bare single-connection writer pool without migrating.
func OpenRaw(path string) (*sql.DB, error) {
	w, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	return w, nil
}
