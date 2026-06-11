package main

import (
	"context"
	"io"
	"os"

	"github.com/openagentsinc/bahia/internal/soulfactory/openclawcontrol"
)

func main() {
	os.Exit(run(context.Background(), os.Stdin, os.Stdout, os.Getenv))
}

func run(ctx context.Context, stdin io.Reader, stdout io.Writer, getenv func(string) string) int {
	return openclawcontrol.RunCLI(ctx, stdin, stdout, getenv)
}
