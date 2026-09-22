//go:build linux

package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOSLaunchRejectsNonAllowlistedBinaryBeforeOpeningLog(t *testing.T) {
	for _, binary := range []string{"firecracker", "/bin/sh", "/usr/bin/../bin/firecracker"} {
		path := filepath.Join(t.TempDir(), "console")
		_, err := (linuxProcessManager{}).Start(context.Background(), StartVMMRequest{Binary: binary, ConsoleLogPath: path})
		if err == nil {
			t.Fatalf("OS launch accepted %q", binary)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("rejected launch opened log")
		}
	}
}
