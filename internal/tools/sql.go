package tools

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Ribeiro/metagente-go/internal/clip"
	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/secret"
	"github.com/Ribeiro/metagente-go/internal/sqlscan"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// sqlDriver is what a build knows about one database: how to open it so that it can only be read.
type sqlDriver struct {
	// name is the name the driver has in database/sql.
	name string
	// file is true when the location is a file, which must exist: opening must not create it.
	file bool
	// dsn gives the connection string for a location, in a way that allows reading only, or that allows
	// writing when write is true.
	dsn func(location string, write bool) string
	// connect gives the connection string of a network database: it is given the connection, the
	// password (which may be empty) and the folder of the project, and builds the string so that the
	// session can only read, unless the connection is one that writes.
	connect func(conn *config.SQLConn, password, root string) (string, error)
	// readOnlyTx makes each statement run in a transaction that the database knows to be read only.
	readOnlyTx bool
	// readOnlyStart is the statement that makes a transaction read only, for a database whose driver cannot
	// ask for it: it runs first in the transaction of each statement that reads. It is empty when readOnlyTx
	// is enough or when the database has no such thing.
	readOnlyStart string
	// prepare asks the database to prepare each statement, which is how some drivers give numbers as numbers.
	prepare bool
	// numberType is the name that the driver gives to the columns of numbers that it hands over as text, if it does:
	// the numbers are made numbers again when the rows are read. It is empty for a driver that gives numbers as numbers.
	numberType string
	// transient says whether an error of this driver is one that may pass: a connection that dropped, a
	// deadlock, a server that is starting. It may be nil.
	transient func(error) bool
}

// sqlDrivers are the drivers this build has. The file of each driver adds itself, so a build made with
// -tags nosqlite has none and says so when a database is used.
var sqlDrivers = map[string]sqlDriver{}

// placeholders are the signs each driver wants for the parameters of a statement.
var placeholders = map[string]func(int) string{
	"sqlite":   func(int) string { return "?" },
	"postgres": func(n int) string { return "$" + strconv.Itoa(n) },
	"mysql":    func(int) string { return "?" },
	"mariadb":  func(int) string { return "?" },
	// SQL Server numbers its parameters @p1, @p2, and Oracle :1, :2.
	"sqlserver": func(n int) string { return "@p" + strconv.Itoa(n) },
	"oracle":    func(n int) string { return ":" + strconv.Itoa(n) },
}

// SQLOptions is what `tool x from sql` needs from the runtime.
type SQLOptions struct {
	// Conns are the [sql.NAME] sections of metagente.toml.
	Conns map[string]*config.SQLConn
	// Credentials say, for the name of a tool, the NAME of the variable that holds the connection string.
	Credentials map[string]string
	// Getenv reads the variable of a credential when a database is opened.
	Getenv func(string) string
	// Root is the folder of the project: a relative path of a database starts there.
	Root string
	// Pool keeps the open databases; a server shares one between its agents.
	Pool *SQLPool
	// Allow is asked before a database is opened for the first time: it is the approval of
	// `metagente trust`.
	Allow func(SQLSpec) error
}

// SQLSpec says what a `tool x from sql` reaches. It is what the person approves with `metagente trust`.
type SQLSpec struct {
	Tool       string
	Connection string
	Driver     string
	// Target is where the database is, for the person to read.
	Target string
	// Credential is the NAME of the variable that holds the connection string, if one is set.
	Credential string
	// Writes is true when the connection may change rows.
	Writes bool
	// Statements are the names of the statements the agent may call, and Transactions the groups of them
	// that run as one.
	Statements   []string
	Transactions []string
	// Fingerprint changes when the database or the text of a statement changes.
	Fingerprint string
}

// SQLSpecOf is the spec of a `tool x from sql`. It is a problem when the connection is not in
// metagente.toml.
func SQLSpecOf(decl *lang.ToolDecl, conns map[string]*config.SQLConn, credentials map[string]string) (SQLSpec, error) {
	conn, ok := conns[decl.ConnectionName()]
	if !ok {
		return SQLSpec{}, diag.Newf("the tool `%s` uses the connection `%s`, and %s has no section [sql.%s]",
			decl.Name, decl.ConnectionName(), config.FileName, decl.ConnectionName()).
			Fixf("add [sql.%s] with a driver, a path and some statements (see docs/LANGUAGE.md)", decl.ConnectionName())
	}
	spec := SQLSpec{
		Tool: decl.Name, Connection: conn.Name, Driver: conn.Driver,
		Credential: credentials[decl.Name], Writes: conn.Writes(), Statements: conn.Names(), Transactions: conn.TransactionNames(),
	}
	spec.Target = conn.Path
	if config.IsNetworkDriver(conn.Driver) {
		spec.Target = fmt.Sprintf("%s@%s:%d/%s (tls %s)", conn.User, conn.Host, conn.Port, conn.Database, conn.TLS)
	} else if spec.Credential != "" {
		spec.Target = "the address held in the variable " + spec.Credential
	}
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00%s\x00%s\x00", conn.Driver, conn.Path, spec.Credential)
	if config.IsNetworkDriver(conn.Driver) {
		fmt.Fprintf(sum, "%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00", conn.Host, conn.Port, conn.Database, conn.User, conn.TLS, conn.CAFile)
	}
	for _, name := range spec.Statements {
		st := conn.Statements[name]
		fmt.Fprintf(sum, "%s\x00%s\x00%s\x00", name, st.Result, st.Parsed.Text)
		if conn.Writes() { // what a connection that only reads never had stays as it was, and stays approved
			fmt.Fprintf(sum, "%s\x00%s\x00", st.Each, strings.Join(st.Columns, ","))
		}
	}
	if conn.Writes() {
		fmt.Fprintf(sum, "%s\x00", conn.Mode)
		for _, name := range spec.Transactions {
			fmt.Fprintf(sum, "tx\x00%s\x00%s\x00", name, strings.Join(conn.Transactions[name].Steps, ","))
		}
	}
	spec.Fingerprint = hex.EncodeToString(sum.Sum(nil))[:12]
	return spec, nil
}

// SQLPool keeps the databases that were opened, so a server does not open one for each conversation.
type SQLPool struct {
	mu  sync.Mutex
	dbs map[string]*sql.DB
}

// NewSQLPool creates an empty pool.
func NewSQLPool() *SQLPool { return &SQLPool{dbs: map[string]*sql.DB{}} }

func (p *SQLPool) get(key string, open func() (*sql.DB, error)) (*sql.DB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if db, ok := p.dbs[key]; ok {
		return db, nil
	}
	db, err := open()
	if err != nil {
		return nil, err
	}
	p.dbs[key] = db
	return db, nil
}

// Close closes the databases.
func (p *SQLPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []error
	for key, db := range p.dbs {
		errs = append(errs, db.Close())
		delete(p.dbs, key)
	}
	return errors.Join(errs...)
}

// SQL is `tool x from sql "NAME"`: the statements of a connection, called by their names. The agent
// never writes SQL, a value is always handed over as a parameter, the database is opened to be read only,
// and an answer has a ceiling of rows and of bytes.
type SQL struct {
	decl   *lang.ToolDecl
	conn   *config.SQLConn
	spec   SQLSpec
	opts   SQLOptions
	limits config.Limits
	stmts  map[string]*sqlStatement
	txs    map[string]*sqlTransaction
	names  []string // statements, then transactions
	owned  bool     // the pool is this tool's own
}

type sqlStatement struct {
	name        string
	query       string
	order       []string // the parameter of each place of the query
	params      []string // the distinct parameters, in the order of first use
	result      string
	description string
	writes      bool
	each        string   // the list of the call that the statement runs for, or ""
	columns     []string // the parameters that come from each item
}

// sqlTransaction is a group of statements that change rows and run as one.
type sqlTransaction struct {
	name        string
	steps       []*sqlStatement
	scalars     []string // the single values of the call
	lists       []string // the lists of the call
	description string
}

// NewSQL creates the tool. It is a problem when the connection is not in metagente.toml.
func NewSQL(decl *lang.ToolDecl, opts SQLOptions, limits config.Limits) (*SQL, error) {
	spec, err := SQLSpecOf(decl, opts.Conns, opts.Credentials)
	if err != nil {
		return nil, err
	}
	conn := opts.Conns[decl.ConnectionName()]
	place := placeholders[conn.Driver]
	if place == nil {
		place = placeholders["sqlite"]
	}
	s := &SQL{decl: decl, conn: conn, spec: spec, opts: opts, limits: limits, stmts: map[string]*sqlStatement{}, txs: map[string]*sqlTransaction{}}
	for _, name := range spec.Statements {
		st := conn.Statements[name]
		query, order := st.Parsed.Rewrite(place)
		if st.Parsed.Kind == sqlscan.Merge && conn.Driver == "sqlserver" {
			query += ";" // SQL Server wants a MERGE to end with a semicolon; the others refuse one
		}
		s.stmts[name] = &sqlStatement{
			name: name, query: query, order: order, params: st.Parsed.Params, result: st.Result, description: st.Description,
			writes: st.Parsed.Kind.Writes(), each: st.Each, columns: st.Columns,
		}
	}
	for _, name := range spec.Transactions {
		tx := conn.Transactions[name]
		scalars, lists, err := conn.TransactionParams(tx)
		if err != nil {
			return nil, err
		}
		t := &sqlTransaction{name: name, scalars: scalars, lists: lists, description: tx.Description}
		for _, step := range tx.Steps {
			t.steps = append(t.steps, s.stmts[step])
		}
		s.txs[name] = t
	}
	s.names = append(append([]string{}, spec.Statements...), spec.Transactions...)
	if s.opts.Pool == nil {
		s.opts.Pool, s.owned = NewSQLPool(), true
	}
	return s, nil
}

// Close closes the databases that this tool opened on its own; the ones of a shared pool are closed
// with the pool.
func (s *SQL) Close() error {
	if s.owned {
		return s.opts.Pool.Close()
	}
	return nil
}

func (s *SQL) Name() string { return s.decl.Name }

// Actions are the statements and the transactions: each one is an action, and its parameters are its values.
func (s *SQL) Actions(context.Context) ([]lang.ActionInfo, error) {
	actions := make([]lang.ActionInfo, 0, len(s.names))
	for _, name := range s.names {
		var info lang.ActionInfo
		if tx, ok := s.txs[name]; ok {
			info = tx.info()
		} else {
			info = s.stmts[name].info()
		}
		actions = append(actions, info)
	}
	return actions, nil
}

func (st *sqlStatement) info() lang.ActionInfo {
	description := st.description
	if description == "" {
		description = "Runs the statement " + st.name + " and gives " + resultWords(st.result)
	}
	info := lang.ActionInfo{Name: st.name, Description: description}
	for _, p := range st.callParams() {
		info.Params = append(info.Params, lang.ParamInfo{Name: p, Required: true})
	}
	return info
}

func (t *sqlTransaction) info() lang.ActionInfo {
	description := t.description
	if description == "" {
		description = "Runs the statements " + t.name + " as one, all or none, and gives how many rows each changed"
	}
	info := lang.ActionInfo{Name: t.name, Description: description}
	for _, p := range t.params() {
		info.Params = append(info.Params, lang.ParamInfo{Name: p, Required: true})
	}
	return info
}

func resultWords(result string) string {
	switch result {
	case config.ResultRow:
		return "its first row, or nothing"
	case config.ResultValue:
		return "its value, or nothing"
	case config.ResultCount:
		return "how many rows it changed"
	}
	return "its rows"
}

func (s *SQL) Call(ctx context.Context, action string, args Args) (value.Value, error) {
	if tx, ok := s.txs[action]; ok {
		return s.callTransaction(ctx, tx, args)
	}
	st, ok := s.stmts[action]
	if !ok {
		return value.Nothing, UnknownAction(s.decl.Name, action, s.names)
	}
	if st.writes {
		return s.callWrite(ctx, st, args)
	}
	bound, err := s.bind(st, args)
	if err != nil {
		return value.Nothing, err
	}
	db, err := s.database(ctx)
	if err != nil {
		return value.Nothing, err
	}
	return s.run(ctx, db, st, bound)
}

// run runs the statement, in a read only transaction when the driver has them, and gives its answer.
func (s *SQL) run(ctx context.Context, db *sql.DB, st *sqlStatement, bound []any) (value.Value, error) {
	driver := sqlDrivers[s.conn.Driver]
	var q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	} = db
	if driver.readOnlyTx {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return value.Nothing, s.failure(ctx, st, err)
		}
		defer func() { _ = tx.Rollback() }()
		q = tx
		if driver.prepare {
			stmt, err := tx.PrepareContext(ctx, st.query)
			if err != nil {
				return value.Nothing, s.failure(ctx, st, err)
			}
			defer stmt.Close()
			q = preparedQuery{stmt}
		}
	} else if driver.readOnlyStart != "" {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return value.Nothing, s.failure(ctx, st, err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, driver.readOnlyStart); err != nil {
			return value.Nothing, s.failure(ctx, st, err)
		}
		q = tx
	} else if driver.prepare {
		stmt, err := db.PrepareContext(ctx, st.query)
		if err != nil {
			return value.Nothing, s.failure(ctx, st, err)
		}
		defer stmt.Close()
		q = preparedQuery{stmt}
	}
	rows, err := q.QueryContext(ctx, st.query, bound...)
	if err != nil {
		return value.Nothing, s.failure(ctx, st, err)
	}
	defer rows.Close()
	return s.collect(ctx, st, rows)
}

// preparedQuery runs a statement that was prepared already: the text is not given again.
type preparedQuery struct{ stmt *sql.Stmt }

func (p preparedQuery) QueryContext(ctx context.Context, _ string, args ...any) (*sql.Rows, error) {
	return p.stmt.QueryContext(ctx, args...)
}

// bind turns the values of the call into the parameters of the statement, in the order of its places.
func (s *SQL) bind(st *sqlStatement, args Args) ([]any, error) {
	if err := s.checkArgs(st.name, st.callParams(), args); err != nil {
		return nil, err
	}
	return s.place(st, func(name string) value.Value { return args[name] })
}

// checkArgs refuses a call that gives a value the action does not take, or leaves one out.
func (s *SQL) checkArgs(action string, takes []string, args Args) error {
	for name := range args {
		if !contains(takes, name) {
			return diag.Newf("`%s.%s` takes no value called `%s`", s.decl.Name, action, name).
				Fix(takesText(takes))
		}
	}
	for _, p := range takes {
		if _, ok := args[p]; !ok {
			return diag.Newf("`%s.%s` needs a value for `%s`", s.decl.Name, action, p).
				Fixf("add it to the call, for example: %s.%s %s: ...", s.decl.Name, action, p)
		}
	}
	return nil
}

// place gives the parameters of the statement in the order of its places, each from get.
func (s *SQL) place(st *sqlStatement, get func(name string) value.Value) ([]any, error) {
	bound := make([]any, len(st.order))
	for i, name := range st.order {
		v, err := parameter(get(name))
		if err != nil {
			return nil, diag.Newf("`%s` in `%s.%s` %s", name, s.decl.Name, st.name, err.Error()).
				Fix("give a text, a number, yes or no, or nothing")
		}
		bound[i] = v
	}
	return bound, nil
}

func takesText(takes []string) string {
	if len(takes) == 0 {
		return "this statement takes no values"
	}
	return "it takes: " + strings.Join(takes, ", ")
}

func contains(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}
	return false
}

// maxExactInt is the largest whole number that a number of the language holds without losing digits.
const maxExactInt = 1 << 53

func parameter(v value.Value) (any, error) {
	switch v.Kind {
	case value.KindNothing:
		return nil, nil
	case value.KindText:
		return v.Text, nil
	case value.KindBool:
		if v.Bool {
			return int64(1), nil
		}
		return int64(0), nil
	case value.KindNumber:
		if v.Number == math.Trunc(v.Number) && math.Abs(v.Number) <= maxExactInt {
			return int64(v.Number), nil
		}
		return v.Number, nil
	}
	return nil, fmt.Errorf("got %s", v.Describe())
}

// database opens the connection the first time, after the approval.
func (s *SQL) database(ctx context.Context) (*sql.DB, error) {
	if s.opts.Allow != nil {
		if err := s.opts.Allow(s.spec); err != nil {
			return nil, err
		}
	}
	driver, ok := sqlDrivers[s.conn.Driver]
	if !ok {
		return nil, diag.Newf("this build of Metagente was made without the `%s` driver", s.conn.Driver).
			Fixf("use a build that has it (the releases do), or build without the tag %s", omitTag(s.conn.Driver))
	}
	location, dsn, err := s.where(driver)
	if err != nil {
		return nil, err
	}
	db, err := s.opts.Pool.get(s.conn.Driver+"\x00"+dsn, func() (*sql.DB, error) {
		db, err := sql.Open(driver.name, dsn)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(4)
		return db, nil
	})
	if err != nil {
		return nil, s.openFailure(err, location)
	}
	if err := db.PingContext(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, s.openFailure(err, location)
	}
	return db, nil
}

// omitTag is the build tag that leaves a driver out.
func omitTag(driver string) string {
	switch driver {
	case "postgres":
		return "nopostgres"
	case "mysql", "mariadb":
		return "nomysql"
	case "sqlserver":
		return "nosqlserver"
	case "oracle":
		return "nooracle"
	}
	return "nosqlite"
}

// where gives the connection string, and the place that an error must not name (the path of a file).
// A network database has its host in the errors: it is what a person needs to see, and it is no secret.
func (s *SQL) where(driver sqlDriver) (location, dsn string, err error) {
	if driver.connect != nil {
		password := ""
		if variable := s.spec.Credential; variable != "" {
			password = s.opts.Getenv(variable)
			if password == "" {
				return "", "", diag.Newf("the password of the database for `%s` is not set: the variable %s is empty", s.decl.Name, variable).
					Fixf("set it in the terminal that runs Metagente, for example: export %s=...", variable)
			}
		}
		dsn, err = driver.connect(s.conn, password, s.opts.Root)
		return "", dsn, err
	}
	location, err = s.location(driver)
	if err != nil {
		return "", "", err
	}
	return location, driver.dsn(location, s.conn.Writes()), nil
}

// location says where the database is: the variable of a credential if one is set, or the path of the
// connection, from the folder of the project.
func (s *SQL) location(driver sqlDriver) (string, error) {
	location := s.conn.Path
	if variable := s.spec.Credential; variable != "" {
		location = strings.TrimSpace(s.opts.Getenv(variable))
		if location == "" {
			return "", diag.Newf("the address of the database for `%s` is not set: the variable %s is empty", s.decl.Name, variable).
				Fixf("set it in the terminal that runs Metagente, for example: export %s=...", variable)
		}
	}
	if location == "" {
		return "", diag.Newf("[sql.%s] has no path for its database", s.conn.Name).
			Fix(`write the path in the section, for example: path = "data/orders.db"`)
	}
	if driver.file {
		if !filepath.IsAbs(location) {
			location = filepath.Join(s.opts.Root, location)
		}
		location = filepath.Clean(location)
		info, err := os.Stat(location)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return "", diag.Newf("the database file of `%s` does not exist: %s", s.decl.Name, location).
				Fix("check the path in the section [sql." + s.conn.Name + "]; it starts at the folder of the project")
		case err != nil:
			return "", diag.Newf("I could not look at the database file of `%s`: %s", s.decl.Name, s.clean(err))
		case info.IsDir():
			return "", diag.Newf("the database of `%s` is a folder, not a file: %s", s.decl.Name, location).
				Fix("write the path of the file")
		}
	}
	return location, nil
}

func (s *SQL) secrets() []string {
	var secrets []string
	if variable := s.spec.Credential; variable != "" && s.opts.Getenv != nil {
		secrets = append(secrets, strings.TrimSpace(s.opts.Getenv(variable)))
	}
	return secrets
}

// clean is what an error of the driver says, with no secret in it and short.
func (s *SQL) clean(err error) string {
	return clip.Collapse(secret.Redact(err.Error(), s.secrets()...), 300)
}

func (s *SQL) openFailure(err error, location string) error {
	text := s.clean(err)
	var d *diag.Diagnostic
	if location == "" {
		d = diag.Newf("I could not reach the database of `%s`: %s", s.decl.Name, text).
			Fixf("check the host, the port, the user and the password of [sql.%s], and that this computer may reach the database", s.conn.Name)
	} else {
		d = diag.Newf("I could not open the database of `%s`: %s", s.decl.Name, strings.ReplaceAll(text, location, "the database")).
			Fix("check the path, and that the file is a database that you may read")
	}
	return s.markIfItMayPass(d, err)
}

// markIfItMayPass tells whoever called that a failure may pass when it is a network that failed, a connection
// that dropped, a time that ran out, or something the driver knows to be temporary.
func (s *SQL) markIfItMayPass(d *diag.Diagnostic, err error) *diag.Diagnostic {
	if sqlMayPass(err) || (sqlDrivers[s.conn.Driver].transient != nil && sqlDrivers[s.conn.Driver].transient(err)) {
		d.WithRetry(0)
	}
	return d
}

// sqlMayPass says whether an error is of the network or of the connection, whatever the driver.
func sqlMayPass(err error) bool {
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET)
}

func (s *SQL) failure(ctx context.Context, st *sqlStatement, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	d := diag.Newf("the database could not run `%s.%s`: %s", s.decl.Name, st.name, scrubValues(s.clean(err))).
		Fixf("check the statement `%s` in [sql.%s] of %s", st.name, s.conn.Name, config.FileName)
	return s.markIfItMayPass(d, err)
}

// collect reads the rows of the answer, within the ceilings, in the shape the statement asks for.
func (s *SQL) collect(ctx context.Context, st *sqlStatement, rows *sql.Rows) (value.Value, error) {
	columns, err := rows.Columns()
	if err != nil {
		return value.Nothing, s.failure(ctx, st, err)
	}
	if err := s.checkColumns(st, columns); err != nil {
		return value.Nothing, err
	}
	numeric := s.textNumbers(rows)
	maxRows := s.limits.MaxSQLRows
	if st.result != config.ResultRows {
		maxRows = 1
	}
	var list []value.Value
	var size int64
	for rows.Next() {
		if len(list) >= maxRows {
			return value.Nothing, s.tooMany(st, maxRows)
		}
		cells := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range cells {
			pointers[i] = &cells[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return value.Nothing, s.failure(ctx, st, err)
		}
		numbersFromText(cells, numeric)
		fields := make(map[string]value.Value, len(columns))
		for i, column := range columns {
			v, bytes, err := cellValue(column, cells[i])
			if err != nil {
				return value.Nothing, diag.Newf("`%s.%s`: %s", s.decl.Name, st.name, err.Error()).
					Fix("turn the column into text or a number in the statement, for example with CAST or hex")
			}
			fields[column] = v
			size += bytes + int64(len(column)) + 8
		}
		if size > s.limits.MaxSQLBytes {
			return value.Nothing, diag.Newf("the answer of `%s.%s` is larger than the %d bytes an answer may have here", s.decl.Name, st.name, s.limits.MaxSQLBytes).
				Fix("ask for fewer rows or columns, or raise max_sql_bytes in the [limits] section of metagente.toml")
		}
		list = append(list, value.Record(fields))
	}
	if err := rows.Err(); err != nil {
		return value.Nothing, s.failure(ctx, st, err)
	}
	return shape(st, columns, list), nil
}

// textNumbers says, for each column, whether the driver hands over its numbers as text. It is nil when no column does.
func (s *SQL) textNumbers(rows *sql.Rows) []bool {
	name := sqlDrivers[s.conn.Driver].numberType
	if name == "" {
		return nil
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil
	}
	numeric := make([]bool, len(types))
	for i, t := range types {
		numeric[i] = t.DatabaseTypeName() == name
	}
	return numeric
}

// numbersFromText turns the text of the columns of numbers into numbers: a whole number stays whole (and a very large
// one is told as text later), and the rest is a number with a fraction. Whatever is not a number is left as it is.
func numbersFromText(cells []any, numeric []bool) {
	for i, flag := range numeric {
		text, ok := cells[i].(string)
		if !flag || !ok {
			continue
		}
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			cells[i] = n
		} else if f, err := strconv.ParseFloat(text, 64); err == nil {
			cells[i] = f
		}
	}
}

func (s *SQL) checkColumns(st *sqlStatement, columns []string) error {
	if st.result == config.ResultValue {
		// The name of the column never reaches the agent: only the value does.
		if len(columns) != 1 {
			return diag.Newf("`%s.%s` has result = \"value\", and its statement gives %d columns", s.decl.Name, st.name, len(columns)).
				Fix("select one column, or use result = \"row\"")
		}
		return nil
	}
	seen := map[string]bool{}
	for _, column := range columns {
		if !validColumn(column) {
			return diag.Newf("`%s.%s` gives a column called `%s`, and a field of a record is made of letters, digits, `_` and `-`", s.decl.Name, st.name, column).
				Fixf("give it a name in the statement, for example: %s AS total", column)
		}
		if seen[column] {
			return diag.Newf("`%s.%s` gives two columns called `%s`", s.decl.Name, st.name, column).
				Fix("give each column its own name with AS")
		}
		seen[column] = true
	}
	return nil
}

func (s *SQL) tooMany(st *sqlStatement, limit int) error {
	if st.result != config.ResultRows {
		return diag.Newf("`%s.%s` has result = \"%s\", and its statement gives more than one row", s.decl.Name, st.name, st.result).
			Fix("limit the statement to one row (LIMIT 1), or use result = \"rows\"")
	}
	return diag.Newf("`%s.%s` gives more than the %d rows an answer may have here", s.decl.Name, st.name, limit).
		Fix("add a LIMIT to the statement, or raise max_sql_rows in the [limits] section of metagente.toml")
}

func shape(st *sqlStatement, columns []string, list []value.Value) value.Value {
	switch st.result {
	case config.ResultRow:
		if len(list) == 0 {
			return value.Nothing
		}
		return list[0]
	case config.ResultValue:
		if len(list) == 0 {
			return value.Nothing
		}
		return list[0].Record[columns[0]]
	}
	if list == nil {
		list = []value.Value{}
	}
	return value.List(list)
}

// validColumn says whether the name of a column can be a field of a record.
func validColumn(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= 0x80:
		case i > 0 && (c == '-' || (c >= '0' && c <= '9')):
		default:
			return false
		}
	}
	return true
}

// cellValue turns what the driver gave into a value of the language, and says about how many bytes it
// weighs. A whole number that a number of the language cannot hold without losing digits comes as a
// text with all its digits, which can be handed back as a parameter.
func cellValue(column string, cell any) (value.Value, int64, error) {
	switch x := cell.(type) {
	case nil:
		return value.Nothing, 0, nil
	case int64:
		if x > maxExactInt || x < -maxExactInt {
			text := strconv.FormatInt(x, 10)
			return value.Text(text), int64(len(text)), nil
		}
		return value.Number(float64(x)), 8, nil
	case float64:
		return value.Number(x), 8, nil
	case bool:
		return value.Bool(x), 1, nil
	case string:
		return value.Text(strings.ToValidUTF8(x, "�")), int64(len(x)), nil
	case []byte:
		if !utf8.Valid(x) {
			return value.Nothing, 0, fmt.Errorf("the column `%s` holds binary data, which a value of the language cannot hold", column)
		}
		return value.Text(string(x)), int64(len(x)), nil
	case time.Time:
		text := x.UTC().Format(time.RFC3339Nano)
		return value.Text(text), int64(len(text)), nil
	}
	return value.Nothing, 0, fmt.Errorf("the column `%s` holds a kind of value that I cannot read", column)
}

// absolute is the path of a file named in metagente.toml: it starts at the folder of the project.
func absolute(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}
