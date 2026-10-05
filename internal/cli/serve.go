package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"metagente/internal/config"
	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/runtime"
	"metagente/internal/serve"
)

// serveEnv is what `serve` needs from the process around it, so a test can give it
// something else.
type serveEnv struct {
	getenv func(string) string
	// terminal is true when a person is reading what the server writes.
	terminal bool
	// stdin is where an MCP client talks to `serve --stdio`.
	stdin io.Reader
	// ready, when set, is told where the server listens and which token opens it.
	ready func(address, token string)
}

func runServe(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := serveEnv{getenv: os.Getenv, terminal: isTerminal() && fileIsTerminal(os.Stderr), stdin: os.Stdin}
	return serveCommand(ctx, args, stdout, stderr, env)
}

func fileIsTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// serveCommand serves agents until the context ends. It returns the exit code: 0
// for a server that was stopped, 2 for a request that makes no sense, 1 for the
// rest.
func serveCommand(ctx context.Context, args []string, stdout, stderr io.Writer, env serveEnv) int {
	flags, err := parseServeArgs(args)
	if err != nil {
		usageProblem(stderr, err)
		return 2
	}
	cfg, err := loadServeConfig(flags.configPath, stderr)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	if flags.stdio {
		return serveStdio(ctx, flags, cfg, stdout, stderr, env)
	}
	plan, err := serve.Resolve(flags.options, serve.Defaults{
		Bind: cfg.Serve.Bind, Port: cfg.Serve.A2APort, Hosts: cfg.Serve.AllowedHosts, PublicURL: cfg.Serve.PublicURL,
	})
	if err != nil {
		printError(stderr, err)
		return 2
	}
	token, generated, err := serve.ResolveToken(env.getenv, env.terminal, plan.Loopback)
	if err != nil {
		printError(stderr, err)
		return 2
	}
	rt := runtime.New(cfg)
	defer rt.Close() // ends the programs the server started
	agents, err := loadServed(rt, flags.files, flags.agents)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	return runServer(ctx, serving{rt: rt, plan: plan, agents: agents, token: token, generated: generated, quiet: flags.quiet, mcp: flags.mcp}, stderr, env)
}

func usageProblem(stderr io.Writer, err error) {
	fmt.Fprintf(stderr, "Problem: %v.\n", err)
	fmt.Fprintln(stderr, "Fix: the options of `serve` are listed by `metagente --help`.")
}

// ---------- the flags ----------

type serveArgs struct {
	options    serve.Options
	files      []string
	agents     []string
	configPath string
	quiet      bool
	stdio      bool
	mcp        bool
}

// listFlag is an option that can be given many times.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func parseServeArgs(args []string) (*serveArgs, error) {
	a := &serveArgs{options: serve.Options{Port: -1}}
	var hosts, agents listFlag
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&a.options.Port, "port", -1, "")
	fs.StringVar(&a.options.Bind, "bind", "", "")
	fs.BoolVar(&a.options.Public, "public", false, "")
	fs.BoolVar(&a.options.BehindProxy, "behind-proxy", false, "")
	fs.BoolVar(&a.options.PublicCard, "public-card", false, "")
	fs.StringVar(&a.options.TLSCert, "tls-cert", "", "")
	fs.StringVar(&a.options.TLSKey, "tls-key", "", "")
	fs.StringVar(&a.options.PublicURL, "public-url", "", "")
	fs.StringVar(&a.configPath, "config", "", "")
	fs.BoolVar(&a.quiet, "quiet", false, "")
	fs.BoolVar(&a.stdio, "stdio", false, "")
	fs.BoolVar(&a.mcp, "mcp", false, "")
	fs.Var(&hosts, "host", "")
	fs.Var(&agents, "agent", "")

	// The standard library stops at the first word that is not an option; the files
	// and the options may come in any order here.
	for rest := args; len(rest) > 0; {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) > 0 {
			a.files = append(a.files, rest[0])
			rest = rest[1:]
		}
	}
	if len(a.files) == 0 {
		return nil, errors.New("`serve` needs the name of at least one file")
	}
	a.options.Hosts, a.agents = hosts, agents
	return a, nil
}

func loadServeConfig(path string, stderr io.Writer) (*config.Config, error) {
	start, err := os.Getwd()
	if err != nil {
		start = "."
	}
	cfg, err := config.Load(start, path)
	if err != nil {
		return nil, err
	}
	for _, note := range cfg.Warnings {
		fmt.Fprintf(stderr, "Note: %s\n", note)
	}
	if len(cfg.Serve.AllowedOrigins) > 0 {
		fmt.Fprintln(stderr, "Note: serve.allowed_origins is ignored: this server never accepts a request made by a page in a browser.")
	}
	return cfg, nil
}

// ---------- the agents ----------

// loadServed reads the files, checks them as `run` does, and refuses what the
// person has not approved. It never asks: a server that approves things by itself
// while it starts would defeat the approvals (requirement T1).
func loadServed(rt *runtime.Runtime, files, wanted []string) ([]serve.Agent, error) {
	byFile := make([][]*lang.AgentDef, len(files))
	for i, file := range files {
		defs, err := runtime.LoadAgents(file)
		if err != nil {
			return nil, err
		}
		rt.Linker.Register(defs) // all of them, before any is checked: files may link to each other
		byFile[i] = defs
	}
	for i, defs := range byFile {
		if err := runtime.CheckAgents(rt, defs, files[i]); err != nil {
			return nil, err
		}
		if err := rt.Authorize(defs, files[i], nil); err != nil {
			return nil, err
		}
	}
	return pickServed(rt, byFile, wanted)
}

// pickServed chooses the agents to serve: all of them, or the ones asked for.
func pickServed(rt *runtime.Runtime, byFile [][]*lang.AgentDef, wanted []string) ([]serve.Agent, error) {
	var all []*lang.AgentDef
	for _, defs := range byFile {
		all = append(all, defs...)
	}
	if len(wanted) > 0 {
		var err error
		if all, err = onlyAgents(all, wanted); err != nil {
			return nil, err
		}
	}
	if len(all) == 0 {
		return nil, diag.New("there is no agent to serve").Fix("start a file with a line like: agent Helper")
	}
	agents := make([]serve.Agent, len(all))
	for i, def := range all {
		agents[i] = serve.NewRuntimeAgent(rt, def)
	}
	return agents, nil
}

func onlyAgents(all []*lang.AgentDef, wanted []string) ([]*lang.AgentDef, error) {
	byName := make(map[string]*lang.AgentDef, len(all))
	names := make([]string, len(all))
	for i, def := range all {
		byName[def.Name] = def
		names[i] = def.Name
	}
	var chosen []*lang.AgentDef
	for _, name := range wanted {
		def, ok := byName[name]
		if !ok {
			return nil, diag.Newf("there is no agent called `%s`", name).Fixf("the agents in these files are: %s", strings.Join(names, ", "))
		}
		chosen = append(chosen, def)
	}
	return chosen, nil
}

// ---------- the server ----------

type serving struct {
	rt        *runtime.Runtime
	plan      *serve.Plan
	agents    []serve.Agent
	token     string
	generated bool
	quiet     bool
	mcp       bool
}

// runServer opens the port and serves until the context ends.
func runServer(ctx context.Context, s serving, stderr io.Writer, env serveEnv) int {
	cfg := s.rt.Config
	ln, err := net.Listen("tcp", s.plan.Address)
	if err != nil {
		printError(stderr, diag.Newf("I could not listen on %s", s.plan.Address).
			Fix("another program may be using that port; choose another with --port"))
		return 1
	}
	_, port := serve.Listening(ln)
	tlsConfig, err := s.plan.TLSConfig()
	if err != nil {
		_ = ln.Close()
		printError(stderr, err)
		return 1
	}
	hosts := s.plan.HostsFor(port)
	srv, err := serve.New(serve.Config{
		Token:                 s.token,
		Hosts:                 hosts,
		BaseURL:               s.plan.BaseURL(port),
		AuthFailuresPerMinute: cfg.Serve.AuthFailuresPerMinute,
		PublicCard:            s.plan.PublicCard,
		NoThrottle:            s.plan.NoThrottle,
		Version:               Version,
		MaxInFlight:           cfg.Serve.MaxRunningTasks,
		MaxConversations:      cfg.Serve.MaxRetainedTasks,
		ConversationTTL:       secondsOf(cfg.Serve.TaskRetentionSeconds),
		MaxBody:               cfg.Serve.MaxBodyBytes,
		MaxCallDepth:          cfg.Runtime.MaxCallDepth,
		MCP:                   s.mcp,
		Log:                   s.rt.Log,
	}, s.agents)
	if err != nil {
		_ = ln.Close()
		printError(stderr, err)
		return 1
	}
	var handler http.Handler = srv
	if !s.quiet {
		handler = serve.AccessLog(srv, stderr)
	}
	announce(stderr, s, srv, s.plan.BaseURL(port), ln.Addr().String(), port)
	if env.ready != nil {
		env.ready(ln.Addr().String(), s.token)
	}
	return serveUntilDone(ctx, ln, handler, tlsConfig, srv, cfg, stderr)
}

// serveUntilDone runs the server and its janitor, and waits for both to end.
func serveUntilDone(ctx context.Context, ln net.Listener, handler http.Handler, tlsConfig *tls.Config, srv *serve.Server, cfg *config.Config, stderr io.Writer) int {
	janitorCtx, stopJanitor := context.WithCancel(ctx)
	janitorDone := make(chan struct{})
	go func() { srv.Run(janitorCtx); close(janitorDone) }()
	err := serve.Serve(ctx, ln, handler, tlsConfig, serve.RunOptions{
		WriteTimeout:      srv.WriteTimeout(),
		MaxConnections:    cfg.Serve.MaxConnections,
		ReadHeaderTimeout: secondsOf(cfg.Serve.ReadHeaderTimeoutSeconds),
		ReadTimeout:       secondsOf(cfg.Serve.ReadTimeoutSeconds),
		IdleTimeout:       secondsOf(cfg.Serve.IdleTimeoutSeconds),
		MaxHeaderBytes:    cfg.Serve.MaxHeaderBytes,
	})
	stopJanitor()
	<-janitorDone
	if err != nil {
		printError(stderr, diag.Newf("the server stopped: %v", err))
		return 1
	}
	return 0
}

func secondsOf(n int) time.Duration { return time.Duration(n) * time.Second }

// announce tells the person what is being served and where, and what a client has
// to do to be answered. The token is written only when this run made it: then the
// person is at the terminal on this computer, and has no other way to learn it.
// base is how clients reach the server and listen is where it listens, which are not the same
// when a proxy stands in front of it.
func announce(stderr io.Writer, s serving, srv *serve.Server, base, listen string, port int) {
	names := srv.Names()
	fmt.Fprintf(stderr, "Serving %d %s on %s. Press Ctrl-C to stop.\n", len(names), plural(len(names), "agent"), base)
	for _, name := range names {
		fmt.Fprintf(stderr, "  %-12s %s/agents/%s\n", name, base, name)
	}
	if srv.ServesMCP() {
		fmt.Fprintf(stderr, "  %-12s %s%s (the agents as MCP tools)\n", "MCP", base, serve.MCPPath)
	}
	if line := whereItListens(s.plan, base, listen, port); line != "" {
		fmt.Fprintln(stderr, line)
	}
	fmt.Fprintln(stderr, "Every request needs the header  Authorization: Bearer TOKEN")
	if s.generated {
		fmt.Fprintf(stderr, "Token for this run, kept nowhere else: %s\n", s.token)
	}
	cfg := s.rt.Config
	fmt.Fprintf(stderr, "Up to %d conversations at once, each may keep up to %d KiB of state.\n",
		cfg.Serve.MaxRetainedTasks, cfg.Limits.MaxStateBytes/1024)
}

// whereItListens says where the server listens, and for which Host, when that is not what the
// address of the banner says. Behind a proxy the banner shows the address of the outside, and the
// person who writes the proxy needs the one of the inside.
func whereItListens(plan *serve.Plan, base, listen string, port int) string {
	scheme, protocol := "http", "plain HTTP"
	if plan.TLS {
		scheme, protocol = "https", "HTTPS"
	}
	if base == scheme+"://"+listen {
		return ""
	}
	return fmt.Sprintf("Listening on %s, in %s, for the Host: %s", listen, protocol, strings.Join(plan.HostsFor(port), ", "))
}

// ---------- MCP over standard input and output ----------

// serveStdio serves the agents as MCP tools on the streams of the process. There is
// no port and no token: the program that started this one is who talks to it, and
// who decides who may.
func serveStdio(ctx context.Context, flags *serveArgs, cfg *config.Config, stdout, stderr io.Writer, env serveEnv) int {
	if err := checkStdioOptions(flags); err != nil {
		printError(stderr, err)
		return 2
	}
	rt := runtime.New(cfg)
	defer rt.Close() // ends the programs the server started
	agents, err := loadServed(rt, flags.files, flags.agents)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	server, err := serve.NewMCP(serve.MCPConfig{
		Version:          Version,
		MaxInFlight:      cfg.Serve.MaxRunningTasks,
		MaxConversations: cfg.Serve.MaxRetainedTasks,
		Log:              rt.Log,
	}, agents)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	announceMCP(stderr, server.ToolNames(), env.terminal)
	if err := server.RunIO(ctx, env.stdin, stdout); err != nil && !endedNormally(ctx, err) {
		printError(stderr, diag.Newf("the server stopped: %v", err))
		return 1
	}
	return 0
}

// checkStdioOptions refuses the options of the network: with them the person would
// believe the server listens somewhere, and it does not.
func checkStdioOptions(flags *serveArgs) error {
	if flags.mcp {
		return diag.New("--mcp serves MCP over HTTP, and --stdio serves it on standard input and output").
			Fix("keep only one of the two")
	}
	o := flags.options
	used := []struct {
		name string
		set  bool
	}{
		{"--port", o.Port >= 0}, {"--bind", o.Bind != ""}, {"--public", o.Public}, {"--behind-proxy", o.BehindProxy},
		{"--public-card", o.PublicCard}, {"--tls-cert", o.TLSCert != ""}, {"--tls-key", o.TLSKey != ""},
		{"--public-url", o.PublicURL != ""}, {"--host", len(o.Hosts) > 0},
	}
	for _, option := range used {
		if option.set {
			return diag.Newf("%s does not apply to --stdio, which uses no network", option.name).
				Fix("remove it, or serve over A2A by leaving out --stdio")
		}
	}
	return nil
}

// endedNormally is true for the ways an MCP session ends that are not a failure: the
// other side closed its streams, or the person stopped the server.
func endedNormally(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed)
}

// announceMCP says what is being served. It goes to the error output: the standard
// output belongs to the protocol, and one stray line there would break it.
func announceMCP(stderr io.Writer, tools []string, terminal bool) {
	fmt.Fprintf(stderr, "Serving %d %s over MCP on standard input and output.\n", len(tools), plural(len(tools), "tool"))
	for _, name := range tools {
		fmt.Fprintf(stderr, "  %s\n", name)
	}
	if terminal {
		fmt.Fprintln(stderr, "This is meant for a program that speaks MCP and starts it; press Ctrl-D to stop.")
	}
}
