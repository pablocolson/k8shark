// Command k8shark is the single binary that powers every component: the CLI
// control-plane (tap/clean/console), the hub server and the node worker. Which
// role runs is selected by the sub-command, so the same image ships everywhere.
package main

import (
	"os"

	"github.com/pablocolson/k8shark/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
