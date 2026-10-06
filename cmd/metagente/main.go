// Command metagente is the Metagente interpreter.
package main

import (
	"os"

	"github.com/Ribeiro/metagente-go/internal/cli"
	"github.com/Ribeiro/metagente-go/internal/mcp"
)

func main() {
	// On Windows, what Metagente starts ends with it (E4). If the system refuses, nothing is
	// worse than before: the programs are still ended one by one when the runtime closes.
	_ = mcp.EndChildrenWithProcess()
	os.Exit(cli.Main())
}
