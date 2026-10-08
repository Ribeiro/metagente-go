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
// them is another matter: a build made with -tags nosqlite cannot open SQLite. MariaDB speaks the
// protocol of MySQL, and has its own name here only so that the file says what it is.
var SQLDrivers = []string{"sqlite", "postgres", "mysql", "mariadb"}

// IsNetworkDriver says whether the database of a driver is reached over the network, with a host, a
// port and a user, and not as a file.
func IsNetworkDriver(driver string) bool { return driver != "sqlite" }

// TLS modes of a network database.
const (
	// TLSVerify encrypts and checks the certificate and the name of the server. It is the default.
	TLSVerify = "verify"
	// TLSRequire encrypts and does not check the certificate.
	TLSRequire = "require"
	// TLSDisable sends everything in the clear.
	TLSDisable = "disable"
)

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
	Path string
	// Host, Port, Database and User say where a network database is and who reads it. The password is
	// never here: it comes from [credentials].
	Host     string
	Port     int
	Database string
	User     string
	// TLS is one of the TLS modes, and CAFile the certificates to trust with TLSVerify, relative to the
	// folder of the project.
	TLS        string
	CAFile     string
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
		case "host":
			conn.Host, err = sqlText(value)
		case "database":
			conn.Database, err = sqlText(value)
		case "user":
			conn.User, err = sqlText(value)
		case "tls":
			conn.TLS, err = sqlText(value)
		case "ca_file":
			conn.CAFile, err = sqlText(value)
		case "port":
			conn.Port, err = sqlPort(value)
		case "statements":
			err = addStatements(conn, value)
		default:
			return fail("I do not know the setting `%s` in %s", key, where).
				Fix("a connection has: driver, statements, and path (SQLite) or host, port, database, user, tls and ca_file")
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
	}
	if err := conn.checkPlace(fail, where); err != nil {
		return err
	}
	switch {
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

// checkPlace looks at the settings that say where the database is: a file for SQLite, a host and a user
// for the others, and never a mix. It fills in the port and the TLS mode that were left out.
func (c *SQLConn) checkPlace(fail func(string, ...any) *diag.Diagnostic, where string) error {
	if !IsNetworkDriver(c.Driver) {
		if c.Host != "" || c.Port != 0 || c.Database != "" || c.User != "" || c.TLS != "" || c.CAFile != "" {
			return fail("%s is a SQLite database, and host, port, database, user, tls and ca_file are for the others", where).
				Fix("write only the path of the file")
		}
		return nil
	}
	switch {
	case c.Path != "":
		return fail("%s names the driver `%s`, which is reached over the network and has no path", where, c.Driver).
			Fix("write host, database and user instead")
	case c.Host == "" || c.Database == "" || c.User == "":
		return fail("%s needs a host, a database and a user", where).
			Fix(`write, for example: host = "db.example.com", database = "orders", user = "reader"; the password goes in [credentials]`)
	case strings.ContainsAny(c.Host, " /@?#\\:") || strings.ContainsAny(c.Database+c.User, "\x00\r\n"):
		return fail("%s has a host, a database or a user with signs that do not belong there", where).
			Fix("write the host as a name or an address, with no port: the port has its own setting")
	}
	if c.Port == 0 {
		c.Port = defaultPort(c.Driver)
	}
	switch c.TLS {
	case "":
		c.TLS = TLSVerify
	case TLSVerify, TLSRequire, TLSDisable:
	default:
		return fail("tls in %s is `%s`, and it has to be %s, %s or %s", where, c.TLS, TLSVerify, TLSRequire, TLSDisable).
			Fix("remove it to have the default, " + TLSVerify)
	}
	if c.CAFile != "" && c.TLS != TLSVerify {
		return fail("ca_file in %s only means something with tls = \"%s\"", where, TLSVerify).
			Fix("remove it, or remove tls")
	}
	return nil
}

func defaultPort(driver string) int {
	if driver == "postgres" {
		return 5432
	}
	return 3306
}

func sqlPort(value any) (int, error) {
	n, ok := value.(int64)
	if !ok || n < 1 || n > 65535 {
		return 0, fmt.Errorf("must be a whole number from 1 to 65535")
	}
	return int(n), nil
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
