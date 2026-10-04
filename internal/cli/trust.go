package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"metagente/internal/config"
	"metagente/internal/runtime"
	"metagente/internal/trust"
)

// These are variables so the tests can pretend a person is at the keyboard.
var (
	stdin      io.Reader = os.Stdin
	isTerminal           = func() bool {
		info, err := os.Stdin.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0
	}
)

// askToApprove shows what is not approved yet and asks the person. It says no
// when nobody can be asked.
func askToApprove(stderr io.Writer, root string, missing []trust.Item) bool {
	if !isTerminal() {
		return false
	}
	fmt.Fprintln(stderr, "This agent wants to:")
	for _, line := range trust.Explain(missing) {
		fmt.Fprintln(stderr, line)
	}
	return confirm(stderr, fmt.Sprintf("Approve this for the project at %s?", root))
}

func confirm(stderr io.Writer, question string) bool {
	fmt.Fprintf(stderr, "%s [y/N] ", question)
	answer, _ := bufio.NewReader(stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func runTrust(args []string, stdout, stderr io.Writer) int {
	t, err := parseTrustArgs(args)
	if err != nil {
		printBadUsage(stderr, err)
		return 2
	}
	cfg, err := currentConfig(t.configPath)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	rt := runtime.New(cfg)
	defer rt.Close()

	switch {
	case t.list:
		return trustList(rt, stdout, stderr)
	case t.revoke:
		return trustRevoke(rt, cfg, stdout, stderr)
	}
	return trustFile(rt, cfg, t, stdout, stderr)
}

type trustArgs struct {
	file       string
	configPath string
	yes        bool
	list       bool
	revoke     bool
}

// modes is how many of the three things that `trust` can do were asked for; it has to be one.
func (t *trustArgs) modes() int {
	modes := 0
	for _, on := range []bool{t.list, t.revoke, t.file != ""} {
		if on {
			modes++
		}
	}
	return modes
}

func parseTrustArgs(args []string) (*trustArgs, error) {
	t := &trustArgs{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--yes":
			t.yes = true
		case arg == "--list":
			t.list = true
		case arg == "--revoke":
			t.revoke = true
		case arg == "--config":
			if i+1 >= len(args) {
				return nil, badUsage("`--config` needs a value.", "write it like: --config FILE")
			}
			i++
			t.configPath = args[i]
		case strings.HasPrefix(arg, "--config="):
			t.configPath = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "--"):
			return nil, badUsage(fmt.Sprintf("`trust` does not take the option `%s`.", arg),
				"the options are --yes, --list, --revoke and --config FILE.")
		case t.file == "":
			t.file = arg
		default:
			return nil, badUsage("`trust` takes one file.", "write it like: metagente trust hello.ag")
		}
	}
	if t.modes() != 1 {
		return nil, badUsage("`trust` needs a file, or --list, or --revoke.", "write it like: metagente trust hello.ag")
	}
	return t, nil
}

// trustRevoke takes back everything that was approved for the project.
func trustRevoke(rt *runtime.Runtime, cfg *config.Config, stdout, stderr io.Writer) int {
	removed, err := rt.Trust.Revoke(cfg.Root)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	if removed == 0 {
		fmt.Fprintf(stdout, "Nothing was approved for %s.\n", cfg.Root)
		return 0
	}
	fmt.Fprintf(stdout, "Removed %d %s for %s.\n", removed, plural(removed, "approval"), cfg.Root)
	return 0
}

// trustFile shows what a file needs, and approves the new items once the person says so.
func trustFile(rt *runtime.Runtime, cfg *config.Config, t *trustArgs, stdout, stderr io.Writer) int {
	agents, err := runtime.LoadAgents(t.file)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	rt.Linker.Register(agents)
	needs := rt.Needs(agents)
	if len(needs) == 0 {
		fmt.Fprintf(stdout, "Nothing in %s needs approval: it starts no programs and reaches no addresses.\n", t.file)
		return 0
	}
	missing, err := rt.Trust.Missing(cfg.Root, needs)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	showNeeds(stdout, t.file, needs, missing)
	if len(missing) == 0 {
		fmt.Fprintf(stdout, "Everything is already approved for %s.\n", cfg.Root)
		return 0
	}
	if !t.yes && !askForApproval(stdout, stderr, cfg.Root) {
		return 1
	}
	if err := rt.Trust.Approve(cfg.Root, needs); err != nil {
		printError(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Approved %d %s for %s.\n", len(missing), plural(len(missing), "item"), cfg.Root)
	return 0
}

// showNeeds lists what the file wants to do, and marks what is new.
func showNeeds(stdout io.Writer, file string, needs, missing []trust.Item) {
	isNew := map[string]bool{}
	for _, item := range missing {
		isNew[item.Key()] = true
	}
	fmt.Fprintf(stdout, "%s, and the agents it links to, want to:\n", filepath.Base(file))
	for _, item := range needs {
		status := "approved"
		if isNew[item.Key()] {
			status = "NEW"
		}
		fmt.Fprintf(stdout, "  [%s] %s\n", status, item.Describe())
	}
}

// askForApproval asks the person, if there is one to ask. Nothing is approved on the strength of a
// terminal that is not there.
func askForApproval(stdout, stderr io.Writer, root string) bool {
	if !isTerminal() {
		fmt.Fprintln(stderr, "Problem: I cannot ask you here, and nothing was approved.")
		fmt.Fprintln(stderr, "Fix: run the command in a terminal, or add --yes once you have read the list above.")
		return false
	}
	if !confirm(stderr, fmt.Sprintf("Approve the NEW items for the project at %s?", root)) {
		fmt.Fprintln(stdout, "Nothing was approved.")
		return false
	}
	return true
}

func trustList(rt *runtime.Runtime, stdout, stderr io.Writer) int {
	approved, err := rt.Trust.List()
	if err != nil {
		printError(stderr, err)
		return 1
	}
	if len(approved) == 0 {
		fmt.Fprintln(stdout, "Nothing is approved yet.")
		return 0
	}
	for _, project := range approved {
		fmt.Fprintf(stdout, "%s (approved %s, set %s)\n", project.Project, project.At, trust.Short(project.SHA256))
		for _, item := range project.Items {
			fmt.Fprintf(stdout, "  %s\n", item.Describe())
		}
	}
	return 0
}
