package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Ribeiro/metagente-go/internal/config"
)

// usageError is a command line that makes no sense: what is wrong with it, and how to write it.
type usageError struct{ problem, fix string }

func (e *usageError) Error() string { return e.problem }

func badUsage(problem, fix string) error { return &usageError{problem: problem, fix: fix} }

// printBadUsage writes a problem with the command line the way every other problem is written.
func printBadUsage(stderr io.Writer, err error) {
	var u *usageError
	if errors.As(err, &u) {
		fmt.Fprintf(stderr, "Problem: %s\nFix: %s\n", u.problem, u.fix)
		return
	}
	fmt.Fprintf(stderr, "Problem: %v\n", err)
}

// currentConfig reads the configuration that applies to the folder where the command is run.
func currentConfig(explicit string) (*config.Config, error) {
	start, err := os.Getwd()
	if err != nil {
		start = "."
	}
	return config.Load(start, explicit)
}
