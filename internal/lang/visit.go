package lang

import "strings"

// visitStmts calls fn for every expression in stmts, nested ones included,
// in source order.
func visitStmts(stmts []Stmt, fn func(Expr)) {
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *AssignStmt:
			visitExpr(s.Value, fn)
		case *ExprStmt:
			visitExpr(s.Expr, fn)
		case *ReplyStmt:
			visitExpr(s.Value, fn)
		case *FailStmt:
			visitExpr(s.Value, fn)
		case *IfStmt:
			visitExpr(s.Cond, fn)
			visitStmts(s.Then, fn)
			visitStmts(s.Otherwise, fn)
		case *ForStmt:
			visitExpr(s.Iter, fn)
			visitStmts(s.Body, fn)
		case *RepeatStmt:
			visitExpr(s.Cond, fn)
			visitStmts(s.Body, fn)
		}
	}
}

// visitExpr calls fn for e and then for the expressions inside it.
func visitExpr(expr Expr, fn func(Expr)) {
	fn(expr)
	switch e := expr.(type) {
	case *ListExpr:
		for _, item := range e.Items {
			visitExpr(item, fn)
		}
	case *CallExpr:
		for _, arg := range e.Args {
			visitExpr(arg.Value, fn)
		}
	case *ThinkExpr:
		visitExpr(e.Prompt, fn)
	case *NotExpr:
		visitExpr(e.Inner, fn)
	case *AndExpr:
		visitExpr(e.Left, fn)
		visitExpr(e.Right, fn)
	case *OrExpr:
		visitExpr(e.Left, fn)
		visitExpr(e.Right, fn)
	case *CompareExpr:
		visitExpr(e.Left, fn)
		visitExpr(e.Right, fn)
	}
}

// CollectCalls returns every call in stmts. A name with a field and no
// values, like `clock.now`, counts as a call too.
func CollectCalls(stmts []Stmt) []*CallExpr {
	var calls []*CallExpr
	visitStmts(stmts, func(expr Expr) {
		switch e := expr.(type) {
		case *CallExpr:
			calls = append(calls, e)
		case *PathExpr:
			if len(e.Path) >= 2 {
				calls = append(calls, &CallExpr{
					Span:   e.Span,
					Target: e.Path[0],
					Action: strings.Join(e.Path[1:], "."),
				})
			}
		}
	})
	return calls
}

// UsesThink reports whether a handler of the agent asks a language model.
func UsesThink(agent *AgentDef) bool {
	found := false
	scan := func(stmts []Stmt) {
		visitStmts(stmts, func(expr Expr) {
			if _, ok := expr.(*ThinkExpr); ok {
				found = true
			}
		})
	}
	for _, handler := range agent.Handlers {
		scan(handler.Body)
	}
	if agent.Start != nil {
		scan(agent.Start.Body)
	}
	return found
}
