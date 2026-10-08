//go:build nosqlite

package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/sqlscan"
)

func TestABuildWithoutTheDriverSaysSoWhenTheDatabaseIsFirstUsed(t *testing.T) {
	parsed, err := sqlscan.Parse("select 1")
	if err != nil {
		t.Fatal(err)
	}
	conn := &config.SQLConn{Name: "x", Driver: "sqlite", Path: "x.db",
		Statements: map[string]*config.SQLStatement{"one": {Name: "one", Result: config.ResultRows, Parsed: parsed}}}
	decl := &lang.ToolDecl{Name: "x", Kind: lang.ToolSQL, Command: "x"}
	tool, err := NewSQL(decl, SQLOptions{Conns: map[string]*config.SQLConn{"x": conn}, Getenv: func(string) string { return "" }}, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tool.Close() })
	_, err = tool.Call(context.Background(), "one", Args{})
	if err == nil || !strings.Contains(err.Error(), "without the `sqlite` driver") {
		t.Errorf("error = %v", err)
	}
}
