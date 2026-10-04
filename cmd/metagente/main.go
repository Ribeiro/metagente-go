// Command metagente is the Metagente interpreter.
package main

import (
	"os"

	"metagente/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
