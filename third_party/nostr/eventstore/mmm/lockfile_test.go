package mmm

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLockfile(t *testing.T) {
	// create a temporary directory for the test
	tmpDir, err := os.MkdirTemp("", "mmm-lockfile-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// initialize first MMM instance
	mmmm1 := &MultiMmapManager{Dir: tmpDir}
	err = mmmm1.Init()
	require.NoError(t, err)
	defer mmmm1.Close()

	// try to initialize second MMM instance on the same directory
	mmmm2 := &MultiMmapManager{Dir: tmpDir}
	err = mmmm2.Init()
	require.Error(t, err)
	require.Contains(t, err.Error(), "already in use by another instance")

	// close first instance
	mmmm1.Close()

	// now second instance should be able to open
	err = mmmm2.Init()
	require.NoError(t, err)
	mmmm2.Close()
}
