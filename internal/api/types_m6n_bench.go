package api

import "github.com/nicolasacchi/metamusic/internal/criticbench"

// M6N-B1 (C387): the Critic Bench. The body is criticbench.Bench itself.

type getCriticBenchIn struct {
	Critic         string `query:"critic" maxLength:"200" doc:"One critic id (see the ids in the answer): only that critic is returned, with the newest ten pairs it called the other way"`
	IncludeUnknown bool   `query:"include_unknown" default:"true" doc:"false drops the critics that say nothing yet (state unknown or insufficient)"`
}

type criticBenchOut struct{ Body criticbench.Bench }
