package main

import (
	"fmt"
	"os"

	"github.com/jbaehova/onthego/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
	builtAt = "unknown"
)

func main() {
	if err := cli.Execute(version, commit, builtAt); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}
