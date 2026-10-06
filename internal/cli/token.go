package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/serve"
)

// runToken makes a token for the server to be given in METAGENTE_TOKEN. Only the
// token goes to the output, so a script can keep it: TOKEN=$(metagente token).
// With --name, the output is a line of a token file: the name, a space, the token.
func runToken(args []string, stdout, stderr io.Writer) int {
	name, named, ok := tokenName(args)
	if !ok {
		fmt.Fprintln(stderr, "`metagente token` prints a token that is 43 characters long; `metagente token --name mac` prints a line for a token file, `mac TOKEN`.")
		return 2
	}
	if named {
		if err := serve.CheckTokenName(name); err != nil {
			fmt.Fprintf(stderr, "Problem: %v.\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "%s %s\n", name, serve.GenerateToken())
		return 0
	}
	fmt.Fprintln(stdout, serve.GenerateToken())
	return 0
}

// tokenName reads `--name NAME` or `--name=NAME`, the only argument the command takes.
func tokenName(args []string) (name string, named, ok bool) {
	switch {
	case len(args) == 0:
		return "", false, true
	case len(args) == 2 && args[0] == "--name":
		return args[1], true, true
	case len(args) == 1 && strings.HasPrefix(args[0], "--name="):
		return strings.TrimPrefix(args[0], "--name="), true, true
	}
	return "", false, false
}
