package cli

import (
	"fmt"
	"io"

	"metagente/internal/serve"
)

// runToken makes a token for the server to be given in METAGENTE_TOKEN. Only the
// token goes to the output, so a script can keep it: TOKEN=$(metagente token).
func runToken(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "`metagente token` takes no arguments. It prints a token that is 43 characters long.")
		return 2
	}
	fmt.Fprintln(stdout, serve.GenerateToken())
	return 0
}
