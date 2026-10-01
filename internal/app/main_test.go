package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// testLocalStoreDir holds the local Nostr event store of every App the tests
// build, so the default relative path never lands in the source tree.
var testLocalStoreDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bahia-app-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testLocalStoreDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func testLocalStorePath() string {
	return filepath.Join(testLocalStoreDir, "daemon.bolt")
}
