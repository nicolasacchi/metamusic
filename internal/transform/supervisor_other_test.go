//go:build !linux

package transform_test

import "testing"

func describeProcess() string { return "--process--" }

func checkProcess(t *testing.T, proc string) { t.Helper() }
