package lang

import (
	"fmt"
	"math"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/broker"
	"github.com/Ribeiro/metagente-go/internal/diag"
)

var (
	agentWords     = []string{"goal", "tool", "link", "remote", "accepts", "on"}
	builtinTools   = []string{"file", "http", "env", "state", "clock"}
	statementWords = []string{"reply", "fail", "if", "otherwise", "for", "repeat", "think"}
)

// reservedAfterUsing are the words that end the tool list of `using`.
var reservedAfterUsing = map[string]bool{
	"and": true, "or": true, "is": true, "contains": true, "within": true,
}

// ParseFile parses a whole .ag file into its agents. Every error says what is
// wrong and how to fix it.
func ParseFile(name, path, text string) ([]*AgentDef, error) {
	if len(text) > MaxSourceBytes {
		return nil, diag.New("this file is larger than 1 MiB").
			At(name, 0, 0).
			Fix("split it into several smaller agent files")
	}
	lines, err := lex(name, text)
	if err != nil {
		return nil, err
	}
	p := &parser{lines: lines, source: &SourceFile{Name: name, Path: path, Text: text}}
	return p.file()
}

type parser struct {
	lines  []Line
	pos    int
	source *SourceFile
}

// cursor walks over the tokens of one line.
type cursor struct {
	tokens []Token
	i      int
	line   int
	// endCol is the column just after the last token, for "missing
	// something" errors.
	endCol int
}

func (p *parser) cursor(line Line, start int) *cursor {
	endCol := 1
	if n := len(line.Tokens); n > 0 {
		endCol = line.Tokens[n-1].Col + 1
	}
	return &cursor{tokens: line.Tokens, i: start, line: line.Number, endCol: endCol}
}

func (c *cursor) at(i int) *Token {
	if i >= 0 && i < len(c.tokens) {
		return &c.tokens[i]
	}
	return nil
}

func (c *cursor) peek() *Token {
	return c.at(c.i)
}

func (c *cursor) peekWord(word string) bool {
	t := c.peek()
	return t != nil && t.Kind == TokWord && t.Word == word
}

func (c *cursor) peekString() bool {
	t := c.peek()
	return t != nil && t.Kind == TokString
}

func (c *cursor) peekSym(sym rune) bool {
	t := c.peek()
	return t != nil && t.Kind == TokSym && t.Sym == sym
}

func (c *cursor) span() Span {
	col := 1
	if t := c.peek(); t != nil {
		col = t.Col
	}
	return Span{Line: c.line, Col: col}
}

func (p *parser) err(line, col int, msg, fix string) error {
	return diag.New(msg).At(p.source.Name, line, col).WithSource(p.source.Text).Fix(fix)
}

// missing reports something absent at the cursor, or at the end of the line.
func (p *parser) missing(c *cursor, msg, fix string) error {
	col := c.endCol
	if t := c.peek(); t != nil {
		col = t.Col
	}
	return p.err(c.line, col, msg, fix)
}

func (p *parser) name(c *cursor, msg, fix string) (string, error) {
	t := c.peek()
	if t == nil || t.Kind != TokWord {
		return "", p.missing(c, msg, fix)
	}
	c.i++
	return t.Word, nil
}

func (p *parser) text(c *cursor, msg, fix string) ([]TextPart, error) {
	t := c.peek()
	if t == nil || t.Kind != TokString {
		return nil, p.missing(c, msg, fix)
	}
	c.i++
	return t.Parts, nil
}

// finish requires the line to be fully consumed.
func (p *parser) finish(c *cursor) error {
	if t := c.peek(); t != nil {
		return p.err(c.line, t.Col,
			fmt.Sprintf("I did not expect %s here; the line should have ended", t.Describe()),
			"remove it, or check for a missing quote or colon before it")
	}
	return nil
}

func startsWithWord(line Line, word string) bool {
	return len(line.Tokens) > 0 && line.Tokens[0].Kind == TokWord && line.Tokens[0].Word == word
}

func (p *parser) file() ([]*AgentDef, error) {
	if len(p.lines) == 0 {
		return nil, diag.New("this file has no agent in it").
			At(p.source.Name, 0, 0).
			Fix("start with a line like: agent Helper")
	}
	var agents []*AgentDef
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		if line.Indent != 0 || !startsWithWord(line, "agent") {
			col := 1
			if len(line.Tokens) > 0 {
				col = line.Tokens[0].Col
			}
			col = max(col, line.Indent*2+1)
			return nil, p.err(line.Number, col,
				"every agent must begin with the word `agent` at the start of a line",
				"write: agent Name   (then indent the lines that belong to it by 2 spaces)")
		}
		agent, err := p.agent()
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, nil
}

func (p *parser) agent() (*AgentDef, error) {
	header := p.lines[p.pos]
	p.pos++
	c := p.cursor(header, 1)
	name, err := p.name(c, "the agent needs a name", "write: agent Weather")
	if err != nil {
		return nil, err
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	agent := &AgentDef{
		Name:   name,
		Span:   Span{Line: header.Number, Col: header.Tokens[0].Col},
		Source: p.source,
	}
	for p.pos < len(p.lines) && p.lines[p.pos].Indent > 0 {
		line := p.lines[p.pos]
		if line.Indent != 1 {
			return nil, p.err(line.Number, 1,
				"this line is indented too far for the agent's own lines",
				"indent it by 2 spaces under `agent`, or move it under an `on` handler")
		}
		p.pos++
		if err := p.declaration(agent, line); err != nil {
			return nil, err
		}
	}
	return agent, nil
}

func (p *parser) declaration(agent *AgentDef, line Line) error {
	first := line.Tokens[0]
	if first.Kind != TokWord {
		return p.err(line.Number, first.Col,
			fmt.Sprintf("I did not expect %s at the start of this line", first.Describe()),
			"start the line with one of: goal, tool, link, remote, accepts, on")
	}
	span := Span{Line: line.Number, Col: first.Col}
	c := p.cursor(line, 1)

	switch first.Word {
	case "goal":
		return p.goalDecl(agent, c, line, span)
	case "tool":
		decl, err := p.toolDecl(c, span)
		if err != nil {
			return err
		}
		agent.Tools = append(agent.Tools, decl)
		return nil
	case "link":
		return p.linkDecl(agent, c, span)
	case "remote":
		return p.remoteDecl(agent, c, span)
	case "accepts":
		return p.acceptsDecl(agent, c, line, span)
	case "on":
		return p.onDecl(agent, c, line, span)
	case "agent":
		return p.err(line.Number, first.Col,
			"an agent cannot be defined inside another agent",
			"move it to the start of a line, with no indentation")
	}
	return p.unknownDecl(line, first)
}

// goalDecl reads `goal "text"`. The goal is fixed text: it has no {names}.
func (p *parser) goalDecl(agent *AgentDef, c *cursor, line Line, span Span) error {
	parts, err := p.text(c, "the goal needs a text", `write: goal "Answer questions"`)
	if err != nil {
		return err
	}
	var goal strings.Builder
	for _, part := range parts {
		if part.IsVar {
			return p.err(line.Number, span.Col,
				"the goal is fixed text and cannot contain {names}", "remove the braces")
		}
		goal.WriteString(part.Lit)
	}
	if err := p.finish(c); err != nil {
		return err
	}
	agent.Goal = &GoalDecl{Text: goal.String(), Span: span}
	return nil
}

// linkDecl reads `link Name` or `link Name from "path.ag"`.
func (p *parser) linkDecl(agent *AgentDef, c *cursor, span Span) error {
	name, err := p.name(c, "`link` needs the name of the agent to call", "write: link Weather")
	if err != nil {
		return err
	}
	decl := &LinkDecl{Name: name, Span: span}
	if c.peekWord("from") {
		c.i++
		parts, err := p.text(c, "`from` needs a file path in quotes", `write: link Weather from "weather.ag"`)
		if err != nil {
			return err
		}
		decl.Path, decl.HasPath = joinLit(parts), true
	}
	if err := p.finish(c); err != nil {
		return err
	}
	agent.Links = append(agent.Links, decl)
	return nil
}

// remoteDecl reads `remote Name at "address"`.
func (p *parser) remoteDecl(agent *AgentDef, c *cursor, span Span) error {
	name, err := p.name(c, "`remote` needs a name for the other agent", `write: remote Bob at "https://..."`)
	if err != nil {
		return err
	}
	if !c.peekWord("at") {
		return p.missing(c, "`remote` needs `at` and the address", `write: remote Bob at "https://host:8080"`)
	}
	c.i++
	parts, err := p.text(c, "the address must be in quotes", `write: remote Bob at "https://host:8080"`)
	if err != nil {
		return err
	}
	if err := p.finish(c); err != nil {
		return err
	}
	agent.Remotes = append(agent.Remotes, &RemoteDecl{Name: name, URL: joinLit(parts), Span: span})
	return nil
}

// acceptsDecl reads `accepts message value value`. The comment after it describes the message.
func (p *parser) acceptsDecl(agent *AgentDef, c *cursor, line Line, span Span) error {
	message, err := p.name(c, "`accepts` needs the name of a message", "write: accepts ask city")
	if err != nil {
		return err
	}
	accept := &Accept{Message: message, Span: span, Description: line.Comment}
	for {
		t := c.peek()
		if t == nil || t.Kind != TokWord {
			break
		}
		accept.Params = append(accept.Params, t.Word)
		c.i++
	}
	if err := p.finish(c); err != nil {
		return err
	}
	agent.Accepts = append(agent.Accepts, accept)
	return nil
}

// onDecl reads `on message` and the lines under it. `on start` is the handler that runs first.
func (p *parser) onDecl(agent *AgentDef, c *cursor, line Line, span Span) error {
	message, err := p.name(c, "`on` needs the name of a message", "write: on ask")
	if err != nil {
		return err
	}
	if err := p.finish(c); err != nil {
		return err
	}
	body, err := p.block(line.Indent)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return p.err(line.Number, span.Col,
			fmt.Sprintf("`on %s` has nothing under it", message),
			`indent at least one line under it, for example: reply "Hello"`)
	}
	handler := &Handler{Message: message, Body: body, Span: span}
	if message == "start" {
		agent.Start = handler
	} else {
		agent.Handlers = append(agent.Handlers, handler)
	}
	return nil
}

// unknownDecl is the problem of a line that begins with a word that declares nothing, with the
// closest word that does.
func (p *parser) unknownDecl(line Line, first Token) error {
	hint := "use one of: goal, tool, link, remote, accepts, on"
	if s := ClosestName(first.Word, agentWords); s != "" {
		hint = fmt.Sprintf("did you mean `%s`?", s)
	}
	return p.err(line.Number, first.Col, fmt.Sprintf("I do not know the word `%s` here", first.Word), hint)
}

// readNames reads quoted names until the line ends or something else comes.
func (p *parser) readNames(c *cursor) []string {
	var names []string
	for c.peekString() {
		parts, _ := p.text(c, "", "")
		names = append(names, joinLit(parts))
	}
	return names
}

func (p *parser) toolDecl(c *cursor, span Span) (*ToolDecl, error) {
	name, err := p.name(c, "`tool` needs the name of a tool", "write: tool file")
	if err != nil {
		return nil, err
	}
	switch name {
	case "file":
		return p.fileTool(c, span)
	case "http":
		return p.httpTool(c, span)
	case "state", "clock":
		return p.plainTool(c, span, name)
	case "env":
		return p.envTool(c, span)
	}
	if c.peekWord("from") {
		return p.serverTool(c, span, name)
	}
	return nil, p.unknownTool(c, span, name)
}

// fileTool reads `tool file ["folder/"] [readonly]`.
func (p *parser) fileTool(c *cursor, span Span) (*ToolDecl, error) {
	decl := &ToolDecl{Name: "file", Kind: ToolFile, Span: span}
	if c.peekString() {
		parts, _ := p.text(c, "", "")
		decl.Scope, decl.HasScope = joinLit(parts), true
	}
	for c.peekWord("readonly") {
		c.i++
		decl.ReadOnly = true
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return decl, nil
}

// httpTool reads `tool http` and its clauses, which may come in any order and more than once.
func (p *parser) httpTool(c *cursor, span Span) (*ToolDecl, error) {
	decl := &ToolDecl{Name: "http", Kind: ToolHTTP, Span: span}
	for {
		more, err := p.httpClause(c, decl)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return decl, nil
}

// httpClause reads one clause of `tool http`. It says whether it found one.
func (p *parser) httpClause(c *cursor, decl *ToolDecl) (bool, error) {
	switch {
	case c.peekWord("readonly"):
		c.i++
		decl.ReadOnly = true
		return true, nil
	case c.peekWord("allow"):
		c.i++
		return true, p.allowClause(c, decl)
	}
	return false, nil
}

// allowClause reads what comes after `allow`: `private`, or one or more domains in quotes.
func (p *parser) allowClause(c *cursor, decl *ToolDecl) error {
	if c.peekWord("private") {
		c.i++
		decl.AllowPrivate = true
		return nil
	}
	names := p.readNames(c)
	if len(names) == 0 {
		return p.missing(c,
			"`allow` needs `private`, or one or more domain names in quotes",
			`write: tool http allow "api.example.com"`)
	}
	decl.Allow = append(decl.Allow, names...)
	return nil
}

// plainTool reads `tool state` and `tool clock`, which take nothing.
func (p *parser) plainTool(c *cursor, span Span, name string) (*ToolDecl, error) {
	kind := ToolState
	if name == "clock" {
		kind = ToolClock
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return &ToolDecl{Name: name, Kind: kind, Span: span}, nil
}

// envTool reads `tool env "NAME" "NAME"`.
func (p *parser) envTool(c *cursor, span Span) (*ToolDecl, error) {
	names := p.readNames(c)
	if len(names) == 0 {
		return nil, p.err(span.Line, span.Col,
			"`tool env` needs the names of the variables this agent may read",
			`write the names in quotes, for example: tool env "HOME" "USER"`)
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return &ToolDecl{Name: "env", Kind: ToolEnv, Span: span, EnvNames: names}, nil
}

// serverTool reads `tool name from mcp "command"` and its clauses, `env "NAME" ...` and `readonly`,
// which may come in any order: a tool that is a separate program, or an address.
func (p *parser) serverTool(c *cursor, span Span, name string) (*ToolDecl, error) {
	c.i++ // from
	if c.peekWord("sql") {
		return p.sqlTool(c, span, name)
	}
	if c.peekWord("broker") {
		return p.brokerTool(c, span, name)
	}
	if !c.peekWord("mcp") {
		return nil, p.missing(c, "after `from` I expected `mcp`",
			`write: tool weather from mcp "command or address"`)
	}
	c.i++
	parts, err := p.text(c, "the MCP command or address must be in quotes",
		`write: tool weather from mcp "npx -y weather-mcp"`)
	if err != nil {
		return nil, err
	}
	decl := &ToolDecl{Name: name, Kind: ToolMCP, Span: span, Command: joinLit(parts)}
	for {
		switch {
		case c.peekWord("readonly"):
			c.i++
			decl.ReadOnly = true
			continue
		case c.peekWord("env"):
			c.i++
			names := p.readNames(c)
			if len(names) == 0 {
				return nil, p.missing(c,
					"`env` needs the names of the variables to give the tool server, in quotes",
					`write: tool x from mcp "command" env "HTTPS_PROXY"`)
			}
			decl.MCPEnv = append(decl.MCPEnv, names...)
			continue
		}
		break
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return decl, nil
}

// sqlTool reads `tool name from sql "connection"`, with the cursor at `sql`.
func (p *parser) sqlTool(c *cursor, span Span, name string) (*ToolDecl, error) {
	c.i++ // sql
	parts, err := p.text(c, "the name of the connection must be in quotes",
		`write: tool orders from sql "orders-db"`)
	if err != nil {
		return nil, err
	}
	connection := strings.TrimSpace(joinLit(parts))
	if !ValidConnectionName(connection) {
		return nil, p.err(c.line, c.tokens[c.i-1].Col,
			"the name of a connection is made of letters, digits, `_` and `-`",
			`write it like: tool orders from sql "orders-db"`)
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return &ToolDecl{Name: name, Kind: ToolSQL, Span: span, Command: connection}, nil
}

// brokerTool reads `tool name from broker "connection" publish "subject" ...`, with the cursor at `broker`.
func (p *parser) brokerTool(c *cursor, span Span, name string) (*ToolDecl, error) {
	const example = `write: tool events from broker "main" publish "etl.orders.batch"`
	c.i++ // broker
	parts, err := p.text(c, "the name of the broker must be in quotes", example)
	if err != nil {
		return nil, err
	}
	connection := strings.TrimSpace(joinLit(parts))
	if !ValidConnectionName(connection) {
		return nil, p.err(c.line, c.tokens[c.i-1].Col,
			"the name of a broker is made of letters, digits, `_` and `-`", example)
	}
	decl := &ToolDecl{Name: name, Kind: ToolBroker, Span: span, Command: connection}
	for c.peekWord("publish") {
		c.i++
		col := 0
		if t := c.peek(); t != nil {
			col = t.Col
		}
		subjects := p.readNames(c)
		if len(subjects) == 0 {
			return nil, p.missing(c, "`publish` needs the subjects the agent may publish to, in quotes", example)
		}
		for _, subject := range subjects {
			if !broker.ValidPattern(subject) {
				return nil, p.err(c.line, col,
					fmt.Sprintf("`%s` is not a subject: it is names made of letters, digits, `_` and `-`, joined by dots, and a name may be `*` or the last `>`", subject),
					`write it like: "etl.orders.batch", or "etl.*.batch", or "etl.>"`)
			}
		}
		decl.Allow = append(decl.Allow, subjects...)
	}
	if len(decl.Allow) == 0 {
		return nil, p.missing(c, "a broker needs `publish` and the subjects the agent may publish to", example)
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return decl, nil
}

// ValidConnectionName says whether text can name a connection to a database: it is also the name of
// the [sql.NAME] section of metagente.toml.
func ValidConnectionName(text string) bool {
	if text == "" || len(text) > 64 {
		return false
	}
	for _, c := range text {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// unknownTool is the problem of a name that is not a built in tool and has no `from mcp`, with the
// closest built in tool.
func (p *parser) unknownTool(c *cursor, span Span, name string) error {
	hint := fmt.Sprintf(
		"built in tools are: %s. For an external MCP tool write: tool %s from mcp \"command\"",
		strings.Join(builtinTools, ", "), name)
	if s := ClosestName(name, builtinTools); s != "" {
		hint = fmt.Sprintf("did you mean `tool %s`?", s)
	}
	return p.err(span.Line, c.tokens[1].Col,
		fmt.Sprintf("I do not know a built in tool called `%s`", name), hint)
}

// block parses the statements indented one level deeper than parentIndent.
func (p *parser) block(parentIndent int) ([]Stmt, error) {
	var stmts []Stmt
	for p.pos < len(p.lines) && p.lines[p.pos].Indent > parentIndent {
		line := p.lines[p.pos]
		if line.Indent != parentIndent+1 {
			return nil, p.err(line.Number, 1,
				"this line is indented more than the line above it allows",
				"indent by exactly one level (2 spaces) more than the line it belongs to")
		}
		p.pos++
		stmt, err := p.statement(line)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, stmt)
	}
	return stmts, nil
}

func (p *parser) statement(line Line) (Stmt, error) {
	first := line.Tokens[0]
	span := Span{Line: line.Number, Col: first.Col}
	c := p.cursor(line, 0)
	if first.Kind != TokWord {
		return p.exprStatement(c, span)
	}

	switch first.Word {
	case "reply":
		return p.replyStatement(c, span)
	case "fail":
		return p.failStatement(c, span)
	case "if":
		return p.ifStatement(c, line, span)
	case "otherwise":
		return nil, p.err(line.Number, first.Col,
			"`otherwise` must come right after an `if` block, at the same indentation",
			"move it directly under the `if` it belongs to")
	case "for":
		return p.forStatement(c, line, span)
	case "repeat":
		// Only `repeat while` is a loop; a value may still be called `repeat`.
		if next := c.at(1); next != nil && next.Kind == TokWord && next.Word == "while" {
			return p.repeatStatement(c, line, span)
		}
	}
	return p.simpleStatement(c, line, span)
}

// exprStatement is a line that is an expression, usually a call or a `think`.
func (p *parser) exprStatement(c *cursor, span Span) (Stmt, error) {
	expr, err := p.expr(c)
	if err != nil {
		return nil, err
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return &ExprStmt{Span: span, Expr: expr}, nil
}

// replyStatement reads `reply value`.
func (p *parser) replyStatement(c *cursor, span Span) (Stmt, error) {
	c.i++
	value, err := p.expr(c)
	if err != nil {
		return nil, err
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return &ReplyStmt{Span: span, Value: value}, nil
}

// failStatement reads `fail value`, with the optional `retry` and `retry in N seconds`. `retry` has a
// meaning only right after the value, so it is still a name anywhere else.
func (p *parser) failStatement(c *cursor, span Span) (Stmt, error) {
	c.i++ // fail
	value, err := p.expr(c)
	if err != nil {
		return nil, err
	}
	if !c.peekWord("retry") {
		if err := p.finish(c); err != nil {
			return nil, err
		}
		return &FailStmt{Span: span, Value: value}, nil
	}
	c.i++
	stmt := &FailRetryStmt{Span: span, Value: value}
	if c.peekWord("in") {
		if err := p.retryWait(c, stmt); err != nil {
			return nil, err
		}
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return stmt, nil
}

// retryWait reads `in N seconds`, with the cursor at `in`.
func (p *parser) retryWait(c *cursor, stmt *FailRetryStmt) error {
	const example = `write: fail "The destination is busy" retry in 60 seconds`
	c.i++
	t := c.peek()
	if t == nil || t.Kind != TokNumber {
		return p.missing(c, "`retry in` needs a number of seconds", example)
	}
	stmt.After, stmt.HasAfter, stmt.AfterCol = t.Number, true, t.Col
	c.i++
	if !(c.peekWord("seconds") || c.peekWord("second")) {
		return p.missing(c, "after the number I expected `seconds`", example)
	}
	c.i++
	return nil
}

// ifStatement reads `if condition`, the lines under it, and the `otherwise` that may follow.
func (p *parser) ifStatement(c *cursor, line Line, span Span) (Stmt, error) {
	c.i++
	cond, err := p.expr(c)
	if err != nil {
		return nil, err
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	then, err := p.block(line.Indent)
	if err != nil {
		return nil, err
	}
	if len(then) == 0 {
		return nil, p.err(line.Number, span.Col, "this `if` has nothing under it",
			"indent the lines to run when the condition is true")
	}
	otherwise, err := p.otherwiseBlock(line)
	if err != nil {
		return nil, err
	}
	return &IfStmt{Span: span, Cond: cond, Then: then, Otherwise: otherwise}, nil
}

// otherwiseBlock reads the `otherwise` of the `if` on line, if the next line is one: it has to be at
// the same indentation. An `otherwise` with nothing under it is an empty block, not a problem.
func (p *parser) otherwiseBlock(line Line) ([]Stmt, error) {
	if p.pos >= len(p.lines) {
		return nil, nil
	}
	next := p.lines[p.pos]
	if next.Indent != line.Indent || !startsWithWord(next, "otherwise") {
		return nil, nil
	}
	p.pos++
	if err := p.finish(p.cursor(next, 1)); err != nil {
		return nil, err
	}
	return p.block(line.Indent)
}

// forStatement reads `for item in list` and the lines to repeat.
func (p *parser) forStatement(c *cursor, line Line, span Span) (Stmt, error) {
	c.i++
	variable, err := p.name(c, "`for` needs a name for each item", "write: for city in cities")
	if err != nil {
		return nil, err
	}
	if !c.peekWord("in") {
		return nil, p.missing(c, "`for` needs `in` and a list", "write: for city in cities")
	}
	c.i++
	iter, err := p.expr(c)
	if err != nil {
		return nil, err
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	body, err := p.block(line.Indent)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, p.err(line.Number, span.Col, "this `for` has nothing under it",
			"indent the lines to repeat")
	}
	return &ForStmt{Span: span, Var: variable, Iter: iter, Body: body}, nil
}

// repeatStatement reads `repeat while condition [up to N times]` and the lines to repeat.
func (p *parser) repeatStatement(c *cursor, line Line, span Span) (Stmt, error) {
	c.i += 2 // repeat while
	cond, err := p.expr(c)
	if err != nil {
		return nil, err
	}
	stmt := &RepeatStmt{Span: span, Cond: cond}
	if c.peekWord("up") {
		if stmt.Limit, err = p.upToTimes(c); err != nil {
			return nil, err
		}
		stmt.HasLimit = true
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	if stmt.Body, err = p.block(line.Indent); err != nil {
		return nil, err
	}
	if len(stmt.Body) == 0 {
		return nil, p.err(line.Number, span.Col, "this `repeat` has nothing under it",
			"indent the lines to repeat")
	}
	return stmt, nil
}

// upToTimes reads `up to N times`, where N is a whole number of at least 1.
func (p *parser) upToTimes(c *cursor) (int, error) {
	c.i++ // up
	if !c.peekWord("to") {
		return 0, p.missing(c, "after `up` I expected `to`", "write: repeat while ... up to 100 times")
	}
	c.i++
	t := c.peek()
	if t == nil || t.Kind != TokNumber {
		return 0, p.missing(c, "`up to` needs a number of times", "write: up to 100 times")
	}
	n := t.Number
	if n < 1 || n != math.Trunc(n) || n > MaxRepeatLimit {
		return 0, p.err(c.line, t.Col,
			fmt.Sprintf("`up to` needs a whole number of times, from 1 to %d", MaxRepeatLimit),
			"write it like: up to 100 times")
	}
	c.i++
	if !(c.peekWord("times") || c.peekWord("time")) {
		return 0, p.missing(c, "after the number I expected `times`", "write: up to 100 times")
	}
	c.i++
	return int(n), nil
}

// simpleStatement reads the lines that begin with a name: `name = value`, `target.action ...` and
// `think ...`. Anything else is a word that is not understood.
func (p *parser) simpleStatement(c *cursor, line Line, span Span) (Stmt, error) {
	word := line.Tokens[0].Word
	if symAt(c, 1, '=') {
		return p.assignment(c, word, span)
	}
	if word == "think" || symAt(c, 1, '.') {
		return p.exprStatement(c, span)
	}
	hint := "a line can be: name = value, target.action key: value, reply, think, if, for, or fail"
	if s := ClosestName(word, statementWords); s != "" {
		hint = fmt.Sprintf("did you mean `%s`?", s)
	}
	if word == "repeat" {
		hint = "a loop is written: repeat while condition"
	}
	return nil, p.err(line.Number, span.Col,
		fmt.Sprintf("I do not understand the line starting with `%s`", word), hint)
}

// assignment reads `name = value`.
func (p *parser) assignment(c *cursor, name string, span Span) (Stmt, error) {
	c.i = 2
	value, err := p.expr(c)
	if err != nil {
		return nil, err
	}
	if err := p.finish(c); err != nil {
		return nil, err
	}
	return &AssignStmt{Span: span, Name: name, Value: value}, nil
}

// symAt says whether the token at position i of the line is that symbol.
func symAt(c *cursor, i int, sym rune) bool {
	t := c.at(i)
	return t != nil && t.Kind == TokSym && t.Sym == sym
}

// ---------- expressions ----------

func (p *parser) expr(c *cursor) (Expr, error) {
	return p.orExpr(c)
}

func (p *parser) orExpr(c *cursor) (Expr, error) {
	left, err := p.andExpr(c)
	if err != nil {
		return nil, err
	}
	for c.peekWord("or") {
		span := left.Pos()
		c.i++
		right, err := p.andExpr(c)
		if err != nil {
			return nil, err
		}
		left = &OrExpr{Span: span, Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) andExpr(c *cursor) (Expr, error) {
	left, err := p.notExpr(c)
	if err != nil {
		return nil, err
	}
	for c.peekWord("and") {
		span := left.Pos()
		c.i++
		right, err := p.notExpr(c)
		if err != nil {
			return nil, err
		}
		left = &AndExpr{Span: span, Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) notExpr(c *cursor) (Expr, error) {
	if c.peekWord("not") {
		span := c.span()
		c.i++
		inner, err := p.notExpr(c)
		if err != nil {
			return nil, err
		}
		return &NotExpr{Span: span, Inner: inner}, nil
	}
	return p.compare(c)
}

func (p *parser) expectWord(c *cursor, word, fix string) error {
	if c.peekWord(word) {
		c.i++
		return nil
	}
	return p.missing(c, fmt.Sprintf("I expected `%s` here", word), fix)
}

func (p *parser) compare(c *cursor) (Expr, error) {
	left, err := p.primary(c, true)
	if err != nil {
		return nil, err
	}
	span := left.Pos()
	if c.peekWord("is") {
		c.i++
		op := CompareIs
		switch {
		case c.peekWord("not"):
			c.i++
			op = CompareIsNot
		case c.peekWord("more"):
			c.i++
			if err := p.expectWord(c, "than", "write: is more than"); err != nil {
				return nil, err
			}
			op = CompareMoreThan
		case c.peekWord("less"):
			c.i++
			if err := p.expectWord(c, "than", "write: is less than"); err != nil {
				return nil, err
			}
			op = CompareLessThan
		}
		right, err := p.primary(c, true)
		if err != nil {
			return nil, err
		}
		return &CompareExpr{Span: span, Op: op, Left: left, Right: right}, nil
	}
	if c.peekWord("contains") {
		c.i++
		right, err := p.primary(c, true)
		if err != nil {
			return nil, err
		}
		return &CompareExpr{Span: span, Op: CompareContains, Left: left, Right: right}, nil
	}
	return left, nil
}

func (p *parser) primary(c *cursor, allowCall bool) (Expr, error) {
	tok := c.peek()
	if tok == nil {
		return nil, p.missing(c, "the line ends but a value is missing",
			"add a value: text in quotes, a number, or a name")
	}
	token := *tok
	span := Span{Line: c.line, Col: token.Col}
	switch token.Kind {
	case TokString:
		c.i++
		return &TextExpr{Span: span, Parts: token.Parts}, nil
	case TokNumber:
		c.i++
		return &NumberExpr{Span: span, Value: token.Number}, nil
	case TokSym:
		if token.Sym == '[' {
			return p.list(c, span)
		}
		return nil, p.err(c.line, token.Col,
			fmt.Sprintf("I did not expect `%c` here", token.Sym),
			"a value can be text in quotes, a number, yes, no, a list, or a name")
	}

	switch token.Word {
	case "yes":
		c.i++
		return &BoolExpr{Span: span, Value: true}, nil
	case "no":
		c.i++
		return &BoolExpr{Span: span, Value: false}, nil
	case "nothing":
		c.i++
		return &NothingExpr{Span: span}, nil
	case "think":
		if allowCall {
			return p.think(c, span)
		}
	}
	return p.pathOrCall(c, token, span, allowCall)
}

func (p *parser) list(c *cursor, span Span) (Expr, error) {
	c.i++
	var items []Expr
	for {
		if c.peekSym(']') {
			c.i++
			break
		}
		item, err := p.primary(c, false)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		if len(items) > MaxListItems {
			return nil, p.err(c.line, span.Col,
				fmt.Sprintf("this list has more than %d items", MaxListItems),
				"keep long lists in a file and read them with `file.read`")
		}
		switch {
		case c.peekSym(','):
			c.i++
		case c.peekSym(']'):
		default:
			return nil, p.missing(c, "this list is not closed",
				"close it with ] and separate items with commas")
		}
	}
	return &ListExpr{Span: span, Items: items}, nil
}

func (p *parser) think(c *cursor, span Span) (Expr, error) {
	c.i++
	prompt, err := p.primary(c, false)
	if err != nil {
		return nil, err
	}
	expr := &ThinkExpr{Span: span, Prompt: prompt}
	if c.peekWord("using") {
		c.i++
		for {
			t := c.peek()
			if t == nil || t.Kind != TokWord || reservedAfterUsing[t.Word] {
				break
			}
			expr.Using = append(expr.Using, t.Word)
			c.i++
		}
		if len(expr.Using) == 0 {
			return nil, p.missing(c, "`using` needs the names of the tools the model may use",
				`write: think "..." using fetch clock`)
		}
	}
	return expr, nil
}

func (p *parser) pathOrCall(c *cursor, token Token, span Span, allowCall bool) (Expr, error) {
	c.i++
	segments, err := p.pathSegments(c, token.Word)
	if err != nil {
		return nil, err
	}
	if !allowCall {
		return &PathExpr{Span: span, Path: segments}, nil
	}

	// Arguments turn a path into a call.
	if !p.atPair(c) && !c.peekWord("within") {
		return &PathExpr{Span: span, Path: segments}, nil
	}
	if len(segments) < 2 {
		return nil, p.err(c.line, span.Col,
			"a call has the form target.action, followed by key: value pairs",
			`for example: weather.forecast city: "Lisbon"`)
	}
	return p.callRest(c, span, segments)
}

// pathSegments reads the names after the first one: `forecast.summary`.
func (p *parser) pathSegments(c *cursor, first string) ([]string, error) {
	segments := []string{first}
	for c.peekSym('.') {
		next := c.at(c.i + 1)
		if next == nil || next.Kind != TokWord {
			return nil, p.missing(c, "a name is missing after the dot", "write it like forecast.summary")
		}
		segments = append(segments, next.Word)
		c.i += 2
	}
	return segments, nil
}

// callRest reads what follows the name of a call: the `key: value` pairs and the time it may take.
func (p *parser) callRest(c *cursor, span Span, segments []string) (Expr, error) {
	var args []Arg
	for p.atPair(c) {
		key := c.tokens[c.i].Word
		c.i += 2
		value, err := p.primary(c, false)
		if err != nil {
			return nil, err
		}
		args = append(args, Arg{Key: key, Value: value})
	}
	call := &CallExpr{
		Span:   span,
		Target: segments[0],
		Action: strings.Join(segments[1:], "."),
		Args:   args,
	}
	if c.peekWord("within") {
		seconds, err := p.withinSeconds(c)
		if err != nil {
			return nil, err
		}
		call.Within, call.HasWithin = seconds, true
	}
	return call, nil
}

// withinSeconds reads `within 30 seconds`.
func (p *parser) withinSeconds(c *cursor) (float64, error) {
	c.i++
	t := c.peek()
	if t == nil || t.Kind != TokNumber {
		return 0, p.missing(c, "`within` needs a number of seconds", "write: within 30 seconds")
	}
	seconds := t.Number
	c.i++
	if !(c.peekWord("seconds") || c.peekWord("second")) {
		return 0, p.missing(c, "after the number I expected `seconds`", "write: within 30 seconds")
	}
	c.i++
	return seconds, nil
}

// atPair reports whether the cursor is at `word:`.
func (p *parser) atPair(c *cursor) bool {
	first, second := c.at(c.i), c.at(c.i+1)
	return first != nil && first.Kind == TokWord && second != nil && second.Kind == TokSym && second.Sym == ':'
}
