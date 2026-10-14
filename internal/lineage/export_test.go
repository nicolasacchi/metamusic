package lineage

// SetPageBytesForTest lowers this service's page cap so a small tree exercises the byte cut.
func (s *Service) SetPageBytesForTest(n int) { s.pageBytes = n }

// PageBytes returns the page cap.
func (s *Service) PageBytes() int { return s.pageBytes }
