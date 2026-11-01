package muses

import "context"

// LiveIDs returns the ids of the non-hidden Muses in id order: one statement, no Muse text. The next-action engine passes
// them to its batch statements as a json_each parameter, so no rule names the muses table (L3 C133, C139.1).
func (s *Service) LiveIDs(ctx context.Context) ([]string, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT id FROM muses WHERE hidden_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
