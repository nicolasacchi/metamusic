// Package acceptance holds the cross-cutting M2a acceptance tests: they drive
// whole servers (REST, MCP, stdio, CLI) built by servertest over the shared
// synthetic archive and check properties no single package owns (the agent can
// do everything the UI can, rating isolation, no secret ever leaks). The
// package has no non-test code.
package acceptance
