package store

// SongStateSQL is the one SQL fragment behind Song state (C90.4, D-L1-18); it is shared by songs.List, board and
// songview and never stored. It selects one row per Song with the columns
//
//	song_id, takes, judged (0/1), picked (0/1), queued_jobs, open_proposals, archived (0/1)
//
// and is used as a derived table: "FROM (" + SongStateSQL + ") st". The state is the first of picked, judged,
// rendered (takes > 0), else draft; see SongStateCase.
const SongStateSQL = `SELECT s.id AS song_id,
  (SELECT COUNT(*) FROM take_songs ts WHERE ts.song_id = s.id) AS takes,
  (EXISTS (SELECT 1 FROM take_songs ts JOIN ratings r ON r.track_id = ts.track_id
            WHERE ts.song_id = s.id AND r.source = 'human' AND r.stars IS NOT NULL)
   OR EXISTS (SELECT 1 FROM duel_verdicts v JOIN duels d ON d.id = v.duel_id
               WHERE v.source = 'human'
                 AND (d.slot_a_track_id IN (SELECT ts.track_id FROM take_songs ts WHERE ts.song_id = s.id)
                   OR d.slot_b_track_id IN (SELECT ts.track_id FROM take_songs ts WHERE ts.song_id = s.id)))
   OR EXISTS (SELECT 1 FROM take_songs ts JOIN tracks t ON t.id = ts.track_id
               WHERE ts.song_id = s.id AND t.status <> 'draft')) AS judged,
  COALESCE((SELECT p.source IN ('pinned','ladder') FROM song_picks p WHERE p.song_id = s.id), 0) AS picked,
  (SELECT COUNT(*) FROM jobs j
    WHERE j.state IN ('queued','submitting','starting','generating','transferring','ingesting')
      AND ((s.draft_id IS NOT NULL AND j.draft_id = s.draft_id)
        OR j.source_track_id IN (SELECT ts.track_id FROM take_songs ts WHERE ts.song_id = s.id))) AS queued_jobs,
  (SELECT COUNT(*) FROM pick_proposals pp WHERE pp.song_id = s.id AND pp.state = 'open') AS open_proposals,
  (NOT EXISTS (SELECT 1 FROM take_songs ts WHERE ts.song_id = s.id)
   AND COALESCE((SELECT d.status = 'archived' FROM recipe_drafts d WHERE d.id = s.draft_id), 0)) AS archived
FROM songs s`

// SongStateCase is the SQL expression that turns the columns of SongStateSQL (alias st) into the Song state string.
const SongStateCase = `CASE WHEN st.picked = 1 THEN 'picked' WHEN st.judged = 1 THEN 'judged' WHEN st.takes > 0 THEN 'rendered' ELSE 'draft' END`
