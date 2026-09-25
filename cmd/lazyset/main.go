package main

import (
	"os"
	"runtime/debug"

	"github.com/daviddwlee84/lazyset/internal/cli"
)

var version = "dev"

func main() {
	if version == "dev" {
		if b, ok := debug.ReadBuildInfo(); ok && b.Main.Version != "" && b.Main.Version != "(devel)" {
			version = b.Main.Version
		}
	}
	os.Exit(cli.Execute(version))
}
