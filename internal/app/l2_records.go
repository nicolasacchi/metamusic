package app

import (
	"encoding/json"

	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/records"
)

// buildL2Records wires the Records service and its Syncer (lanes R-a and R-b own this file; both constructors are final).
// The Syncer is built here and started by serve only: rigs call Rig.Settle, mcp-stdio and the CLI never start it.
func buildL2Records(in l2Inputs, s *l2Services, o L2Options) {
	s.Records = records.New(in.st, in.now, in.newID)
	s.Syncer = records.NewSyncer(s.Records, o.Log)
	// records imports no other L2 package (C118), so Verify R7 gets the Crate filter vocabulary from here.
	records.SetCrateFilterCheck(func(subject string, filter json.RawMessage) error {
		_, err := crates.ParseFilter(crates.Subject(subject), filter)
		return err
	})
}
