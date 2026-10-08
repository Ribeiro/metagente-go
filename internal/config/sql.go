package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/sqlscan"
)

// SQLDrivers are the databases that a [sql.NAME] section may name. Whether a build can open one of
// them is another matter: a build made with -tags nosqlite cannot open SQLite.
var SQLDrivers = []string{"sqlite"}

// Results are the shapes in which a statement gives its answer.
const (
	// ResultRows is a list with a record for each row. It is the default.
	ResultRows = "rows"
	// ResultRow is the first row as a record, or nothing when there is none.
	ResultRow = "row"
	// ResultValue is the one value of a statement that gives one row of one column, or nothing.
	ResultValue = "value"
)

// SQLConn is a [sql.NAME] section: a database and the statements that an agent may run on it with
// `tool x from sql "NAME"`. An agent never writes SQL; it calls a statement by its name.
type SQLConn struct {
	Name   string
	Driver string
	// Path is the file of a SQLite database, relative to the folder of the project.
	Path       string
	Statements map[string]*SQLStatement
}

// SQLStatement is one named statement of a connection.
type SQLStatement struct {
	Name        string
	Result      string
	Description string
	Parsed      *sqlscan.Statement
}

// Names lists the names of the statements, sorted.
func (c *SQLConn) Names() []string {
	names := make([]string, 0, len(c.Statements))
	for name := range c.Statements {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var statementName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// addSQL reads one [sql.NAME] section. The line is the one of its header.
func (cfg *Config) addSQL(file, text string, e entry) error {
	fail := func(format string, args ...any) *diag.Diagnostic {
		return diag.New(fmt.Sprintf(format, args...)).At(file, e.line, 1).WithSource(text)
	}
	if !lang.ValidConnectionName(e.key) {
		return fail("`%s` is not a name for a connection: use letters, digits, `_` and `-`", e.key).
			Fix("write the section like: [sql.orders-db]")
	}
	table, ok := e.value.(map[string]any)
	if !ok {
		return fail("`%s` in [sql] must be a section of its own", e.key).
			Fix("write one section for each connection, for example [sql.orders-db], and put driver, path and the statements under it")
	}
	conn := &SQLConn{Name: e.key, Statements: map[string]*SQLStatement{}}
	where := "[sql." + e.key + "]"
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := table[key]
		var err error
		switch key {
		case "driver":
			conn.Driver, err = sqlText(value)
		case "path":
			conn.Path, err = sqlText(value)
		case "statements":
			err = addStatements(conn, value)
		default:
			return fail("I do not know the setting `%s` in %s", key, where).
				Fix("a connection has: driver, path and statements")
		}
		if err != nil {
			return fail("`%s` in %s %s", key, where, err.Error()).Fix("change it in " + FileName)
		}
	}
	switch {
	case conn.Driver == "":
		return fail("%s needs a driver", where).Fixf("write, for example: driver = %q", SQLDrivers[0])
	case !knownDriver(conn.Driver):
		return fail("%s names the driver `%s`, which this version does not have", where, conn.Driver).
			Fixf("the drivers are: %s", strings.Join(SQLDrivers, ", "))
	case len(conn.Statements) == 0:
		return fail("%s has no statements", where).
			Fixf("add some under [sql.%s.statements], for example: next_page = \"SELECT id FROM orders WHERE id > :after ORDER BY id LIMIT :size\"", e.key)
	}
	if _, twice := cfg.SQL[e.key]; twice {
		return fail("%s is written twice", where).Fix("keep one")
	}
	cfg.SQL[e.key] = conn
	return nil
}

func knownDriver(name string) bool {
	for _, d := range SQLDrivers {
		if d == name {
			return true
		}
	}
	return false
}

func sqlText(value any) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("must be a text in quotes")
	}
	return text, nil
}

// addStatements reads the statements of a connection: each is a text (a statement that gives rows), or
// a table with sql, result and description.
func addStatements(conn *SQLConn, value any) error {
	table, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("must be a section of statements, written as [sql.%s.statements]", conn.Name)
	}
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !statementName.MatchString(name) {
			return fmt.Errorf("has a statement called `%s`, and a name here is made of letters, digits, `_` and `-`, and begins with a letter or `_`", name)
		}
		statement, err := readStatement(name, table[name])
		if err != nil {
			return fmt.Errorf("has a problem in the statement `%s`: %s", name, err.Error())
		}
		conn.Statements[name] = statement
	}
	return nil
}

func readStatement(name string, value any) (*SQLStatement, error) {
	statement := &SQLStatement{Name: name, Result: ResultRows}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case map[string]any:
		for key, field := range v {
			var err error
			switch key {
			case "sql":
				text, err = sqlText(field)
			case "result":
				statement.Result, err = sqlText(field)
			case "description":
				statement.Description, err = sqlText(field)
			default:
				return nil, fmt.Errorf("I do not know the setting `%s`; a statement has: sql, result and description", key)
			}
			if err != nil {
				return nil, fmt.Errorf("`%s` %s", key, err.Error())
			}
		}
	default:
		return nil, fmt.Errorf("must be the text of the statement, or a table with sql, result and description")
	}
	switch statement.Result {
	case ResultRows, ResultRow, ResultValue:
	default:
		return nil, fmt.Errorf("`result` is `%s`, and it has to be %s, %s or %s", statement.Result, ResultRows, ResultRow, ResultValue)
	}
	parsed, err := sqlscan.Parse(text)
	if err != nil {
		return nil, err
	}
	statement.Parsed = parsed
	return statement, nil
}
