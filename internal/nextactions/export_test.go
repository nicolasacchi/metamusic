package nextactions

import "github.com/nicolasacchi/metamusic/internal/auth"

// SetRules replaces the registry for one serial test and returns the restore function. Tests that call it must not run in
// parallel with any other test of the package (parallel tests only resume after every serial test has finished).
func SetRules(rs []Rule) (restore func()) {
	old := Rules
	Rules = rs
	return func() { Rules = old }
}

// RuleSets returns the rules of lanes NA1, NA2 and NA3 as the registry concatenates them.
func RuleSets() [][]Rule { return [][]Rule{rulesA, rulesB, rulesC} }

// SetStatementHook makes the service call f with every SQL statement the engine and the rules issue (not LiveIDs, which is the
// muses package's one statement).
func (s *Service) SetStatementHook(f func(query string)) { s.hook = f }

// ForTakeCodes exposes the set of codes ForTake evaluates.
func ForTakeCodes() map[string]bool { return forTakeCodes }

// Viewer returns a principal with the scopes, for table tests.
func Viewer(kind auth.Kind, scopes ...auth.Scope) auth.Principal {
	return auth.Principal{Kind: kind, ID: "t-" + string(kind), Name: "t", Scopes: scopes, Via: "test"}
}
