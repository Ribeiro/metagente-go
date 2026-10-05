// Package cli is the command line of Metagente. It lives in a package, apart
// from main, so the acceptance suite can run it without building a binary.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"metagente/internal/applog"
	"metagente/internal/config"
	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/runtime"
	"metagente/internal/scaffold"
	"metagente/internal/trust"
)

// Version is the version of this build. `make build` and `make dist` set it from the git tag, with
// -ldflags "-X metagente/internal/cli.Version=..."; a plain `go build` leaves it as it is here.
var Version = "0.0.0-dev"

// Main runs the command line with the real arguments and streams, and returns
// the exit code.
func Main() int { return mainWith(os.Args[1:], os.Stdout, os.Stderr, Run) }

// mainWith is Main with the pieces given, so a test can make a command fail.
func mainWith(args []string, stdout, stderr io.Writer, run func([]string, io.Writer, io.Writer) int) (code int) {
	defer func() {
		// Whatever goes wrong inside, the person reads one plain sentence and
		// never a trace of the implementation. The cause and the stack go to the
		// log, which only the user can read (requirement P2).
		if r := recover(); r != nil {
			fmt.Fprintln(stderr, "Something went wrong inside Metagente. This is not a mistake in your agent.")
			if path, err := applog.Default().Panic("main", r, debug.Stack()); err == nil {
				fmt.Fprintf(stderr, "The details were saved in %s\n", path)
			}
			code = 1
		}
	}()
	return run(args, stdout, stderr)
}

// Run runs one command and returns the exit code: 0 for success, 1 for a
// problem in the agent or the files, 2 for a mistake in how the command was
// written.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "new":
		return runNew(args[1:], stdout, stderr)
	case "run":
		return runRun(args[1:], stdout, stderr)
	case "trust":
		return runTrust(args[1:], stdout, stderr)
	case "token":
		return runToken(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "--version", "-v", "version":
		fmt.Fprintf(stdout, "metagente %s\n", Version)
		return 0
	case "--help", "-h", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "Problem: I do not know the command `%s`.\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Build AI agents in minutes.

Usage:
  metagente check [--strict] FILE.ag   look for problems without running
  metagente new NAME                   create a starter agent and a metagente.toml
  metagente run FILE.ag [MESSAGE] [key=value ...] [--agent NAME] [--config FILE]
                                       run an agent
  metagente trust FILE.ag [--yes]      approve the programs and addresses an agent uses
  metagente trust --list               show what is approved
  metagente trust --revoke             remove the approvals of this project
  metagente token                      make a token to keep and give to the server (METAGENTE_TOKEN)
  metagente serve FILE.ag ... [--port N] [--agent NAME] [--public-card] [--mcp] [--quiet] [--config FILE]
                                       serve agents over A2A on this computer; every request needs a token.
                                       --mcp serves them as MCP tools too, at /mcp, behind the same token
  metagente serve ... --public --tls-cert FILE --tls-key FILE --host NAME
                                       open to the network: TLS of its own, the names it answers to,
                                       and the token in METAGENTE_TOKEN (or --token-file FILE)
  metagente serve ... --behind-proxy --host NAME --public-url https://NAME
                                       behind a proxy on this computer, which does the TLS
  metagente serve FILE.ag ... --stdio
                                       the agents as MCP tools on standard input and output (no port, no token)
  metagente --version
`)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// printError shows an error the way the person should read it.
func printError(stderr io.Writer, err error) {
	if d, ok := diag.From(err); ok {
		fmt.Fprint(stderr, d.Render())
		return
	}
	fmt.Fprintf(stderr, "Problem: %v\n", err)
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	c, err := parseCheckArgs(args)
	if err != nil {
		printBadUsage(stderr, err)
		return 2
	}
	agents, err := readAgents(c.file)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	problems, warnings := checkWithLinks(agents)
	if c.strict {
		problems = append(problems, warnings...)
		warnings = nil
		diag.SortByPlace(problems)
	}
	return reportCheck(c.file, agents, problems, warnings, stdout, stderr)
}

type checkArgs struct {
	file   string
	strict bool
}

func parseCheckArgs(args []string) (*checkArgs, error) {
	c := &checkArgs{}
	for _, arg := range args {
		switch {
		case arg == "--strict":
			c.strict = true
		case len(arg) > 1 && arg[0] == '-':
			return nil, badUsage(fmt.Sprintf("`check` does not take the option `%s`.", arg), "the only option is --strict.")
		case c.file == "":
			c.file = arg
		default:
			return nil, badUsage("`check` takes one file.", "write it like: metagente check hello.ag")
		}
	}
	if c.file == "" {
		return nil, badUsage("`check` needs the name of a file.", "write it like: metagente check hello.ag")
	}
	return c, nil
}

// readAgents opens a file and reads the agents in it.
func readAgents(file string) ([]*lang.AgentDef, error) {
	text, err := os.ReadFile(file)
	if err != nil {
		why := err.Error()
		if os.IsNotExist(err) {
			why = "the file does not exist"
		}
		return nil, diag.Newf("I could not open %s: %s", file, why).
			Fix("check the file name and the folder you are in")
	}
	return lang.ParseFile(filepath.Base(file), file, string(text))
}

// checkWithLinks checks the agents and, besides, their links: the agents they name must exist and
// must accept what is sent to them. A configuration that cannot be read is not a reason to stop
// here; `run` reports it.
func checkWithLinks(agents []*lang.AgentDef) (problems, warnings []*diag.Diagnostic) {
	result := lang.Check(agents)
	problems, warnings = result.Problems, result.Warnings
	if start, err := os.Getwd(); err == nil {
		if cfg, err := config.Load(start, ""); err == nil {
			linker := runtime.NewLinker()
			linker.Register(agents)
			problems = append(problems, runtime.CheckLinks(linker, cfg.Root, agents)...)
			warnings = append(warnings, runtime.LinkWarnings(cfg.Root, agents)...)
			diag.SortByPlace(problems)
			diag.SortByPlace(warnings)
		}
	}
	return problems, warnings
}

// reportCheck writes the warnings and the problems, or the line that says there are none.
func reportCheck(file string, agents []*lang.AgentDef, problems, warnings []*diag.Diagnostic, stdout, stderr io.Writer) int {
	for _, w := range warnings {
		fmt.Fprintln(stderr, w.Render())
	}
	if len(problems) == 0 {
		n := len(agents)
		fmt.Fprintf(stdout, "No problems found in %s (%d %s).\n", file, n, plural(n, "agent"))
		return 0
	}
	for _, p := range problems {
		fmt.Fprintln(stderr, p.Render())
	}
	fmt.Fprintf(stderr, "Found %d %s.\n", len(problems), plural(len(problems), "problem"))
	return 1
}

func runNew(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Problem: `new` needs one name.")
		fmt.Fprintln(stderr, "Fix: write it like: metagente new hello")
		return 2
	}
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	lines, err := scaffold.Create(dir, args[0])
	if err != nil {
		printError(stderr, err)
		return 1
	}
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
	}
	return 0
}

func runRun(args []string, stdout, stderr io.Writer) int {
	r, err := parseRunArgs(args)
	if err != nil {
		printBadUsage(stderr, err)
		return 2
	}
	cfg, err := currentConfig(r.configPath)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	for _, note := range cfg.Warnings {
		fmt.Fprintf(stderr, "Note: %s\n", note)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rt := runtime.New(cfg)
	defer rt.Close() // ends the programs the run started
	opts := r.options()
	opts.Confirm = func(missing []trust.Item) bool { return askToApprove(stderr, cfg.Root, missing) }
	result, err := runtime.RunFile(ctx, rt, opts)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	if text, ok := runtime.ShowValue(result); ok {
		fmt.Fprintln(stdout, text)
	}
	return 0
}

type runArgs struct {
	positional []string
	agent      string
	configPath string
}

func parseRunArgs(args []string) (*runArgs, error) {
	r := &runArgs{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--agent" || arg == "--config":
			if i+1 >= len(args) {
				return nil, badUsage(fmt.Sprintf("`%s` needs a value.", arg), fmt.Sprintf("write it like: %s VALUE", arg))
			}
			i++
			if arg == "--agent" {
				r.agent = args[i]
			} else {
				r.configPath = args[i]
			}
		case strings.HasPrefix(arg, "--agent="):
			r.agent = strings.TrimPrefix(arg, "--agent=")
		case strings.HasPrefix(arg, "--config="):
			r.configPath = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "--"):
			return nil, badUsage(fmt.Sprintf("`run` does not take the option `%s`.", arg), "the options are --agent NAME and --config FILE.")
		default:
			r.positional = append(r.positional, arg)
		}
	}
	if len(r.positional) == 0 {
		return nil, badUsage("`run` needs the name of a file.", "write it like: metagente run hello.ag greet name=World")
	}
	return r, nil
}

// options are the file, the agent, the message and the values that the command line named.
func (r *runArgs) options() runtime.Options {
	opts := runtime.Options{File: r.positional[0], Agent: r.agent}
	if len(r.positional) > 1 {
		opts.Message = r.positional[1]
		opts.Params = r.positional[2:]
	}
	return opts
}
