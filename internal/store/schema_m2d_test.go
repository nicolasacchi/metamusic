package store

import (
	"context"
	"testing"
)

func TestMigration0006_ListenDetail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := open(t)
	var n int
	if err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('listen_events') WHERE name='detail'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("listen_events.detail missing: %d %v", n, err)
	}
	if v, err := s.UserVersion(ctx); err != nil || v < 6 {
		t.Fatalf("version %d %v", v, err)
	}
}
