package songpr

// Options builds the Song PR service (C253). M5-F2 froze the type so the API and the app wiring compile; lane M5-S1
// fills it (store, library, drafts lint, clock, ids) and replaces this file.
type Options struct{}

// Service stores and reads Song PRs. Until lane M5-S1 lands it holds nothing and every operation answers 501.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service { return &Service{o: o} }
