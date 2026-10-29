package recordview_test

import "github.com/nicolasacchi/metamusic/internal/records"

func recordsNew(title string, songs ...string) records.NewRecord {
	return records.NewRecord{Title: title, SongIDs: songs}
}
