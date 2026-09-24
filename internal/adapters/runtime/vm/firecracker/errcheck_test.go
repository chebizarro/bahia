package firecracker

import (
	"errors"
	"net"
	"testing"
)

func checkTestClose(t *testing.T, err error) {
	t.Helper()
	if err != nil && !errors.Is(err, net.ErrClosed) {
		t.Errorf("test close failed: %v", err)
	}
}
