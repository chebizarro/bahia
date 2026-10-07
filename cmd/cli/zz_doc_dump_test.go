package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/spf13/cobra"
)

func TestZZDumpCommands(t *testing.T) {
	if os.Getenv("DUMP_CLI_COMMANDS") == "" {
		t.Skip("set DUMP_CLI_COMMANDS")
	}
	root := newRootCommand()
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if len(c.Commands()) == 0 && c.Runnable() {
			fmt.Println(c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}
