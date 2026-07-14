// Command pathshim records external command invocations via PATH shims and
// replays them offline in tests.
//
// A single binary serves two roles, dispatched on argv[0]: invoked as
// "pathshim" it is the CLI; invoked through a shim symlink (e.g. "git"
// inside a session's shim directory) it intercepts that call.
package main

import (
	"os"
	"path/filepath"

	"github.com/JaydenCJ/pathshim/internal/cli"
	"github.com/JaydenCJ/pathshim/internal/shim"
)

func main() {
	if name := filepath.Base(os.Args[0]); name != "pathshim" {
		os.Exit(shim.Run(name, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
