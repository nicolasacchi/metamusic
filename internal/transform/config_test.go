package transform_test

import "github.com/nicolasacchi/metamusic/internal/config"

func testConfig() config.Config {
	return config.Config{TransformEnabled: true, TransformQueueMax: 6, TransformMaxCPUS: 600, TransformMaxOutS: 900, TransformMaxMemMB: 1024,
		TransformAgentDailyCPUS: 1800, TransformHumanDailyCPUS: 14400}
}
