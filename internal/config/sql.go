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
var SQLDrivers = []string{"sqlite", "postgres", "mysql", "mariadb", "sqlserver", "oracle"}

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
	// ResultCount is how many rows a statement that changes things changed. It is the only answer of
	// INSERT, UPDATE and DELETE.
	ResultCount = "count"
)

// Modes of a connection.
const (
	// ModeRead only reads: the database is opened to be read, and a statement is a SELECT or a WITH. It is
	// the default.
	ModeRead = "read"
	// ModeWrite may also change rows with INSERT, UPDATE and DELETE statements. It never changes the shape
	// of the database: the tables are made by the migrations of the user.
	ModeWrite = "write"
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
	TLS    string
	CAFile string
	// Mode is ModeRead or ModeWrite.
	Mode       string
	Statements map[string]*SQLStatement
	// Transactions are the groups of statements that run as one, all or none. Only a connection that
	// writes has them.
	Transactions map[string]*SQLTransaction
}

// Writes says whether the connection may change rows.
func (c *SQLConn) Writes() bool { return c.Mode == ModeWrite }

// SQLStatement is one named statement of a connection.
type SQLStatement struct {
	Name        string
	Result      string
	Description string
	Parsed      *sqlscan.Statement
	// Each, for a statement that changes rows, is the name of a value of the call that holds a list: the
	// statement runs once for each item, all in one transaction. Columns say which parameters of the
	// statement come from the item; the other parameters come from the call, and are the same for every
	// item. An item is a record with those names as fields, or a list with the values in that order.
	Each    string
	Columns []string
}

// SQLTransaction is a group of statements that change rows and run as one, in order: if one fails, none
// of them changed anything.
type SQLTransaction struct {
	Name        string
	Steps       []string
	Description string
}

// TransactionNames lists the names of the transactions, sorted.
func (c *SQLConn) TransactionNames() []string {
	names := make([]string, 0, len(c.Transactions))
	for name := range c.Transactions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	conn := &SQLConn{Name: e.key, Mode: ModeRead, Statements: map[string]*SQLStatement{}, Transactions: map[string]*SQLTransaction{}}
	if mode, ok := table["mode"]; ok {
		text, err := sqlText(mode)
		if err == nil && text != ModeRead && text != ModeWrite {
			err = fmt.Errorf("has to be %q or %q", ModeRead, ModeWrite)
		}
		if err != nil {
			return fail("`mode` in [sql.%s] %s", e.key, err.Error()).Fix("remove it to only read, or write: mode = \"write\"")
		}
		conn.Mode = text
	}
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
		case "mode":
			// read before the loop, because the statements depend on it
		case "statements":
			err = addStatements(conn, value)
		case "transactions":
			err = addTransactions(conn, value)
		default:
			return fail("I do not know the setting `%s` in %s", key, where).
				Fix("a connection has: driver, mode, statements, transactions, and path (SQLite) or host, port, database, user, tls and ca_file")
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
	case len(conn.Transactions) > 0 && !conn.Writes():
		return fail("%s has transactions, and a connection that only reads has no use for them", where).
			Fix(`add mode = "write" to the section, or remove the transactions`)
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
	if c.CAFile != "" && c.Driver == "oracle" {
		return fail("ca_file in %s is not for Oracle, which trusts the certificates of this computer", where).
			Fix("remove it, and put the certificate of your authority among those of the computer")
	}
	if c.CAFile != "" && c.TLS != TLSVerify {
		return fail("ca_file in %s only means something with tls = \"%s\"", where, TLSVerify).
			Fix("remove it, or remove tls")
	}
	return nil
}

func defaultPort(driver string) int {
	switch driver {
	case "postgres":
		return 5432
	case "sqlserver":
		return 1433
	case "oracle":
		return 1521
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
// a table with sql, result, description and, for a statement that changes rows, each and columns.
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
		statement, err := readStatement(name, table[name], conn.Writes())
		if err != nil {
			return fmt.Errorf("has a problem in the statement `%s`: %s", name, err.Error())
		}
		conn.Statements[name] = statement
	}
	return nil
}

func readStatement(name string, value any, write bool) (*SQLStatement, error) {
	statement := &SQLStatement{Name: name}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case map[string]any:
		var err error
		if text, err = readStatementTable(statement, v); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("must be the text of the statement, or a table with sql, result and description")
	}
	parse := sqlscan.Parse
	if write {
		parse = sqlscan.ParseWrite
	}
	parsed, err := parse(text)
	if err != nil {
		if _, again := sqlscan.ParseWrite(text); !write && again == nil {
			return nil, fmt.Errorf("%s; a statement that changes rows needs a connection with mode = \"write\"", err.Error())
		}
		return nil, err
	}
	statement.Parsed = parsed
	if err := statement.checkShape(); err != nil {
		return nil, err
	}
	return statement, nil
}

// readStatementTable reads the settings of a statement written as a table, and gives its text.
func readStatementTable(statement *SQLStatement, table map[string]any) (string, error) {
	var text string
	for key, field := range table {
		var err error
		switch key {
		case "sql":
			text, err = sqlText(field)
		case "result":
			statement.Result, err = sqlText(field)
		case "description":
			statement.Description, err = sqlText(field)
		case "each":
			statement.Each, err = sqlText(field)
		case "columns":
			statement.Columns, err = sqlTexts(field)
		default:
			return "", fmt.Errorf("I do not know the setting `%s`; a statement has: sql, result, description, each and columns", key)
		}
		if err != nil {
			return "", fmt.Errorf("`%s` %s", key, err.Error())
		}
	}
	return text, nil
}

// checkShape looks at what the statement answers and at how it is repeated, and fills in the answer that
// was left out.
func (st *SQLStatement) checkShape() error {
	writes := st.Parsed.Kind.Writes()
	switch {
	case st.Result == "" && writes:
		st.Result = ResultCount
	case st.Result == "":
		st.Result = ResultRows
	case writes && st.Result != ResultCount:
		return fmt.Errorf("`result` is `%s`, and a statement that changes rows only gives %s", st.Result, ResultCount)
	case !writes && st.Result != ResultRows && st.Result != ResultRow && st.Result != ResultValue:
		return fmt.Errorf("`result` is `%s`, and it has to be %s, %s or %s", st.Result, ResultRows, ResultRow, ResultValue)
	}
	if st.Each == "" && len(st.Columns) == 0 {
		return nil
	}
	if !writes {
		return fmt.Errorf("`each` and `columns` are for a statement that changes rows")
	}
	if st.Each == "" || len(st.Columns) == 0 {
		return fmt.Errorf("`each` and `columns` go together: the first names the list of the call, the second the parameters that come from each item")
	}
	if !statementName.MatchString(st.Each) {
		return fmt.Errorf("`each` is `%s`, and a name here is made of letters, digits, `_` and `-`", st.Each)
	}
	params := map[string]bool{}
	for _, p := range st.Parsed.Params {
		params[p] = true
	}
	if params[st.Each] {
		return fmt.Errorf("`each` is `%s`, which is also a parameter of the statement; give the list another name", st.Each)
	}
	seen := map[string]bool{}
	for _, column := range st.Columns {
		switch {
		case !params[column]:
			return fmt.Errorf("`columns` names `%s`, and the statement has no parameter :%s", column, column)
		case seen[column]:
			return fmt.Errorf("`columns` names `%s` twice", column)
		}
		seen[column] = true
	}
	return nil
}

func sqlTexts(value any) ([]string, error) {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("must be a list of texts in quotes, for example [\"a\", \"b\"]")
	}
	texts := make([]string, len(list))
	for i, item := range list {
		text, err := sqlText(item)
		if err != nil {
			return nil, err
		}
		texts[i] = text
	}
	return texts, nil
}

// MaxTransactionSteps is the most statements a transaction may have.
const MaxTransactionSteps = 32

// addTransactions reads the transactions of a connection: each is a list of the names of statements that
// change rows, or a table with steps and description.
func addTransactions(conn *SQLConn, value any) error {
	table, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("must be a section, written as [sql.%s.transactions]", conn.Name)
	}
	if !conn.Writes() {
		return fmt.Errorf("is for a connection with mode = \"write\"")
	}
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch {
		case !statementName.MatchString(name):
			return fmt.Errorf("has a transaction called `%s`, and a name here is made of letters, digits, `_` and `-`, and begins with a letter or `_`", name)
		case conn.Statements[name] != nil:
			return fmt.Errorf("has a transaction and a statement that are both called `%s`", name)
		}
		tx, err := readTransaction(conn, name, table[name])
		if err != nil {
			return fmt.Errorf("has a problem in the transaction `%s`: %s", name, err.Error())
		}
		conn.Transactions[name] = tx
	}
	return nil
}

func readTransaction(conn *SQLConn, name string, value any) (*SQLTransaction, error) {
	tx := &SQLTransaction{Name: name}
	var err error
	switch v := value.(type) {
	case []any:
		tx.Steps, err = sqlTexts(v)
	case map[string]any:
		for key, field := range v {
			switch key {
			case "steps":
				tx.Steps, err = sqlTexts(field)
			case "description":
				tx.Description, err = sqlText(field)
			default:
				return nil, fmt.Errorf("I do not know the setting `%s`; a transaction has: steps and description", key)
			}
			if err != nil {
				return nil, fmt.Errorf("`%s` %s", key, err.Error())
			}
		}
	default:
		return nil, fmt.Errorf("must be a list of the names of statements, or a table with steps and description")
	}
	if err != nil {
		return nil, err
	}
	if len(tx.Steps) == 0 || len(tx.Steps) > MaxTransactionSteps {
		return nil, fmt.Errorf("needs from 1 to %d steps", MaxTransactionSteps)
	}
	if _, _, err := conn.TransactionParams(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

// TransactionParams gives the values that a call of a transaction needs: the scalars, and the lists. A
// name that two steps share is one value, and it has to mean the same to both.
func (c *SQLConn) TransactionParams(tx *SQLTransaction) (scalars, lists []string, err error) {
	roles := paramRoles{role: map[string]string{}}
	for _, step := range tx.Steps {
		st := c.Statements[step]
		if st == nil {
			return nil, nil, fmt.Errorf("step `%s` is not a statement of this connection", step)
		}
		if !st.Parsed.Kind.Writes() {
			return nil, nil, fmt.Errorf("step `%s` only reads, and a transaction is made of statements that change rows", step)
		}
		if err := roles.add(st); err != nil {
			return nil, nil, err
		}
	}
	return roles.scalars, roles.lists, nil
}

// paramRoles collects the values of the steps of a transaction, each as a single value or as a list.
type paramRoles struct {
	role           map[string]string
	scalars, lists []string
}

func (r *paramRoles) add(st *SQLStatement) error {
	if st.Each != "" {
		if err := r.use(st.Each, "list"); err != nil {
			return err
		}
	}
	for _, p := range st.CallParams() {
		if err := r.use(p, "scalar"); err != nil {
			return err
		}
	}
	return nil
}

func (r *paramRoles) use(name, as string) error {
	if before, ok := r.role[name]; ok {
		if before != as {
			return fmt.Errorf("the value `%s` is a list in one step and a single value in another", name)
		}
		return nil
	}
	r.role[name] = as
	if as == "list" {
		r.lists = append(r.lists, name)
	} else {
		r.scalars = append(r.scalars, name)
	}
	return nil
}

// CallParams are the parameters that the call gives as single values: all but those that come from each item.
func (st *SQLStatement) CallParams() []string {
	if st.Each == "" {
		return st.Parsed.Params
	}
	var params []string
	for _, p := range st.Parsed.Params {
		fromItem := false
		for _, c := range st.Columns {
			fromItem = fromItem || c == p
		}
		if !fromItem {
			params = append(params, p)
		}
	}
	return params
}
