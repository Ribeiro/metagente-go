package lang

import "strings"

// Span is a place in a source file. Every node knows where it came from.
type Span struct{ Line, Col int }

// Pos returns the span itself, so every node that embeds a Span has a Pos
// method.
func (s Span) Pos() Span { return s }

// SourceFile is a source file with its text, kept so errors can show the
// offending line.
type SourceFile struct {
	Name string
	Path string
	Text string
}

// AgentDef is one agent of a .ag file.
type AgentDef struct {
	Name     string
	Span     Span
	Goal     *GoalDecl
	Tools    []*ToolDecl
	Links    []*LinkDecl
	Remotes  []*RemoteDecl
	Accepts  []*Accept
	Handlers []*Handler
	// Start is the optional `on start` handler. It is not part of Accepts.
	Start  *Handler
	Source *SourceFile
}

// FindHandler returns the handler of a message, or nil.
func (a *AgentDef) FindHandler(message string) *Handler {
	for _, h := range a.Handlers {
		if h.Message == message {
			return h
		}
	}
	return nil
}

// FindAccept returns the accepted message with that name, or nil.
func (a *AgentDef) FindAccept(message string) *Accept {
	for _, acc := range a.Accepts {
		if acc.Message == message {
			return acc
		}
	}
	return nil
}

// FindTool returns the tool declared with that name, or nil.
func (a *AgentDef) FindTool(name string) *ToolDecl {
	for _, t := range a.Tools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// GoalDecl is the `goal "text"` line.
type GoalDecl struct {
	Text string
	Span Span
}

// ToolKind tells the built in tools and MCP tools apart.
type ToolKind int

const (
	ToolFile ToolKind = iota
	ToolHTTP
	ToolState
	ToolClock
	ToolEnv
	ToolMCP
	ToolSQL
)

// ToolDecl is one `tool` line.
type ToolDecl struct {
	// Name is the name used in calls, for example `file` or `weather`.
	Name string
	Kind ToolKind
	Span Span

	// Scope is the folder of `tool file "folder/"`; HasScope tells an
	// empty folder from no folder.
	Scope    string
	HasScope bool
	// EnvNames are the variables `tool env "A" "B"` may read.
	EnvNames []string
	// Command is the command or address of `tool x from mcp "..."`, or the name of the connection of
	// `tool x from sql "..."` (see ConnectionName).
	Command string
	// MCPEnv are the extra variables passed to the tool server
	// (`... from mcp "cmd" env "A"`, requirement E1).
	MCPEnv []string
	// ReadOnly removes the actions that change things (requirement L7).
	ReadOnly bool
	// Allow lists the domains `tool http allow "..."` may reach (requirement H2).
	Allow []string
	// AllowPrivate is `tool http allow private`.
	AllowPrivate bool
}

// ConnectionName is the name of the connection of a `tool x from sql "name"`: the [sql.name] section of
// metagente.toml, which holds the database and the statements. It is kept in Command, so that the record
// of the language, which lists every field of a declaration, does not change for all the other tools.
func (t *ToolDecl) ConnectionName() string { return t.Command }

// LinkDecl is `link Name [from "path.ag"]`.
type LinkDecl struct {
	Name    string
	Path    string
	HasPath bool
	Span    Span
}

// RemoteDecl is `remote Name at "https://..."`.
type RemoteDecl struct {
	Name string
	URL  string
	Span Span
}

// Accept is `accepts message param param`.
type Accept struct {
	Message     string
	Params      []string
	Description string
	Span        Span
}

// Handler is an `on message` section.
type Handler struct {
	Message string
	Body    []Stmt
	Span    Span
}

// Stmt is a statement.
type Stmt interface {
	Pos() Span
	stmtNode()
}

type (
	// AssignStmt is `name = expression`.
	AssignStmt struct {
		Span
		Name  string
		Value Expr
	}
	// ExprStmt is an expression used as a statement, usually a call.
	ExprStmt struct {
		Span
		Expr Expr
	}
	// ReplyStmt is `reply expression`.
	ReplyStmt struct {
		Span
		Value Expr
	}
	// IfStmt is `if` with an optional `otherwise`.
	IfStmt struct {
		Span
		Cond      Expr
		Then      []Stmt
		Otherwise []Stmt
	}
	// ForStmt is `for item in list`.
	ForStmt struct {
		Span
		Var  string
		Iter Expr
		Body []Stmt
	}
	// RepeatStmt is `repeat while condition`, with an optional `up to N times`.
	RepeatStmt struct {
		Span
		Cond Expr
		// Limit is the N of `up to N times`; it is only meant when HasLimit is true.
		Limit    int
		HasLimit bool
		Body     []Stmt
	}
	// FailStmt is `fail "message"`: the failure is final.
	FailStmt struct {
		Span
		Value Expr
	}
	// FailRetryStmt is `fail "message" retry`, or `fail "message" retry in 60 seconds`: the failure may
	// pass, so whoever called may ask again. It is a statement of its own, and not a mark on FailStmt,
	// so that what the parser makes of a program with no `retry` stays as it was.
	FailRetryStmt struct {
		Span
		Value Expr
		// After is the N of `in N seconds`, a suggestion for the wait; it is only meant when HasAfter is true.
		After    float64
		HasAfter bool
		// AfterCol is where the number is written, for the checks.
		AfterCol int
	}
)

func (*AssignStmt) stmtNode() {}
func (*ExprStmt) stmtNode()   {}
func (*ReplyStmt) stmtNode()  {}
func (*IfStmt) stmtNode()     {}
func (*ForStmt) stmtNode()    {}
func (*FailStmt) stmtNode()   {}

func (*FailRetryStmt) stmtNode() {
	// Only marks FailRetryStmt as a statement.
}

func (*RepeatStmt) stmtNode() {
	// Only marks RepeatStmt as a statement.
}

// CompareOp is a comparison operator.
type CompareOp int

const (
	CompareIs CompareOp = iota
	CompareIsNot
	CompareMoreThan
	CompareLessThan
	CompareContains
)

// Expr is an expression.
type Expr interface {
	Pos() Span
	exprNode()
}

// Arg is one `key: value` pair of a call.
type Arg struct {
	Key   string
	Value Expr
}

type (
	// TextExpr is a text with `{name}` holes already split out.
	TextExpr struct {
		Span
		Parts []TextPart
	}
	// NumberExpr is a number literal.
	NumberExpr struct {
		Span
		Value float64
	}
	// BoolExpr is `yes` or `no`.
	BoolExpr struct {
		Span
		Value bool
	}
	// NothingExpr is `nothing`.
	NothingExpr struct {
		Span
	}
	// ListExpr is `[a, b, c]`.
	ListExpr struct {
		Span
		Items []Expr
	}
	// PathExpr is a name with optional fields, like `forecast.summary`.
	PathExpr struct {
		Span
		Path []string
	}
	// CallExpr is `target.action key: value [within N seconds]`.
	CallExpr struct {
		Span
		Target    string
		Action    string
		Args      []Arg
		Within    float64
		HasWithin bool
	}
	// ThinkExpr is `think "prompt" [using tool tool]`.
	ThinkExpr struct {
		Span
		Prompt Expr
		// Using limits the tools the model may use (requirement L7). Empty
		// means every declared tool.
		Using []string
	}
	// NotExpr is `not x`.
	NotExpr struct {
		Span
		Inner Expr
	}
	// AndExpr is `x and y`.
	AndExpr struct {
		Span
		Left, Right Expr
	}
	// OrExpr is `x or y`.
	OrExpr struct {
		Span
		Left, Right Expr
	}
	// CompareExpr is a comparison such as `a is more than b`.
	CompareExpr struct {
		Span
		Op          CompareOp
		Left, Right Expr
	}
)

func (*TextExpr) exprNode()    {}
func (*NumberExpr) exprNode()  {}
func (*BoolExpr) exprNode()    {}
func (*NothingExpr) exprNode() {}
func (*ListExpr) exprNode()    {}
func (*PathExpr) exprNode()    {}
func (*CallExpr) exprNode()    {}
func (*ThinkExpr) exprNode()   {}
func (*NotExpr) exprNode()     {}
func (*AndExpr) exprNode()     {}
func (*OrExpr) exprNode()      {}
func (*CompareExpr) exprNode() {}

// joinLit turns the parts of a text back into one string, with each hole
// written as {name}. It is used for texts that must be fixed, like addresses.
func joinLit(parts []TextPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.IsVar {
			b.WriteString("{" + strings.Join(p.Var, ".") + "}")
		} else {
			b.WriteString(p.Lit)
		}
	}
	return b.String()
}
