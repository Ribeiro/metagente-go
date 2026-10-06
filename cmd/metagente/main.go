// Command metagente is the Metagente interpreter.
package main

import (
	"os"

	"github.com/Ribeiro/metagente-go/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
