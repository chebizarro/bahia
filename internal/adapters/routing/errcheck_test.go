package routing

import "testing"

func checkTestError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("test operation failed: %v", err)
	}
}
