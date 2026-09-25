package main

import (
	"os"

	"lazyset/internal/cli"
)

var version = "dev"

func main() { os.Exit(cli.Execute(version)) }
