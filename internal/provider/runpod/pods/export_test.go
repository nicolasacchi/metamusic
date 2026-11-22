package pods

import "context"

// CanaryVerifiedForTest exposes the canary gate to the external test package.
func (l *Lifecycle) CanaryVerifiedForTest(ctx context.Context) (bool, error) {
	return l.canaryVerified(ctx)
}
