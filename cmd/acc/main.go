package main

import (
	"fmt"
	"os"

	"github.com/kunalvirwal/apple-container-compose/internal/cli"
)

func main() {
	rootCmd := cli.NewRootCommand()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(rootCmd.ErrOrStderr(), "error:", err)
		os.Exit(1)
	}
}
