package store

// OwnerRatingsView and CriticRatingsView name the two static views of migration 0017 (C306.3): the Owner's human ratings
// and the agent's. Curation readers (picks, song state, Atlas, next actions, provenance, duel suggestions, stats
// histograms) select from owner_ratings, so a Listener's or Editor's stars can never steer them (D-U1-9, C307.1).
const (
	OwnerRatingsView  = "owner_ratings"
	CriticRatingsView = "critic_ratings"
)

// ViewerRatingJoin is the one SQL join of a viewer-facing display (stars shown on takes, playlists, records, lineage, star
// sort and filter, Subsonic userRating; C307.2): it LEFT JOINs the ratings table to the track column trackCol and pins
// exactly one rater, so at most one row matches and nothing fans out. The caller binds the rater key (auth.DisplayRater)
// as the placeholder, in the position the fragment occupies in the statement. The alias exposes the table's columns.
// The archtest TestNoRawHumanRatingJoin allows this file to name the table and no reader.
func ViewerRatingJoin(alias, trackCol string) string {
	return " LEFT JOIN ratings " + alias + " ON " + alias + ".track_id = " + trackCol + " AND " + alias + ".source = 'human' AND " + alias + ".rater = ?"
}
