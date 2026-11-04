package attention

// Options builds the attention service (store, listening, library timing, clock); lane M5-A1 fills it.
type Options struct{}

// Service reads heat strips and hotspots of a take (C256). M5-F2 froze the type; lane M5-A1 replaces this file.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service { return &Service{o: o} }
