package tape

import "time"

// SetTimings shortens the lease renewal and progress throttle of a Service for tests.
func SetTimings(s *Service, renew, progress time.Duration) {
	s.renewEvery, s.progressEvery = renew, progress
}
