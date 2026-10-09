package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

// The statements of a description are made here, in the dialect of each database, and put in the
// connections that the description names. They go through the same reading as a statement written in
// metagente.toml: the same checks of what a statement may do, and the same list that `metagente trust`
// shows. The text of the control tables (etl_jobs, etl_batches, ...) is fixed: it is the contract that
// the Worker, the sweeper and the migrations of samples/async-elt share.

// generated is a statement or a transaction that a description makes.
type generated struct {
	name  string
	value any // the text of the statement, or the table that readStatement reads
}

type generatedTx struct {
	name  string
	steps []string
}

// ELTDestinationDrivers are the databases that the Worker's statements are made for.
var ELTDestinationDrivers = []string{"sqlite", "postgres", "sqlserver", "oracle"}

// notAccepted is the problem of a statement that a description makes and a connection refuses.
const notAccepted = "[elt.%s] makes a statement that [sql.%s] does not accept: %s"

// makeELT puts the statements of every description in the connections it names.
func (cfg *Config) makeELT(file, text string) error {
	names := make([]string, 0, len(cfg.ELT))
	for name := range cfg.ELT {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e := cfg.ELT[name]
		fail := func(format string, args ...any) *diag.Diagnostic {
			return diag.New(fmt.Sprintf(format, args...)).At(file, e.line, 1).WithSource(text)
		}
		if e.Source != nil {
			if err := cfg.makeELTSource(e, fail); err != nil {
				return err
			}
		}
		if e.Destination != nil {
			if err := cfg.makeELTDestination(e, fail); err != nil {
				return err
			}
		}
	}
	return nil
}

func (cfg *Config) eltConn(e *ELT, name, what string, fail func(string, ...any) *diag.Diagnostic) (*SQLConn, error) {
	conn, ok := cfg.SQL[name]
	if !ok {
		return nil, fail("[elt.%s] names the connection `%s` as its %s, and %s has no section [sql.%s]", e.Name, name, what, FileName, name).
			Fixf("add [sql.%s] with the driver and the place of the database; its statements are made from the description", name)
	}
	return conn, nil
}

func (cfg *Config) makeELTSource(e *ELT, fail func(string, ...any) *diag.Diagnostic) error {
	s := e.Source
	source, err := cfg.eltConn(e, s.Connection, "source", fail)
	if err != nil {
		return err
	}
	outbox, err := cfg.eltConn(e, s.Outbox, "outbox", fail)
	if err != nil {
		return err
	}
	if _, ok := cfg.Broker[s.Broker]; !ok {
		return fail("[elt.%s] names the broker `%s`, and %s has no section [broker.%s]", e.Name, s.Broker, FileName, s.Broker).
			Fixf("add [broker.%s] with the driver, the address and the stream", s.Broker)
	}
	if outbox.Driver != "sqlite" || !outbox.Writes() {
		return fail("the outbox of [elt.%s], [sql.%s], has to be a SQLite file with mode = \"write\": it is the small file that the Extractor keeps on its own machine", e.Name, s.Outbox).
			Fix("use a SQLite file for the outbox")
	}
	if source.Writes() {
		return fail("the source of [elt.%s], [sql.%s], is written to: a source is only read", e.Name, s.Connection).
			Fix("remove mode = \"write\" from it")
	}
	if err := source.addGenerated(e.sourceStatements(source.Driver), nil); err != nil {
		return fail(notAccepted, e.Name, s.Connection, err.Error()).
			Fix("check `columns`, `mask` and `select` in the source of the description")
	}
	if err := outbox.addGenerated(eltOutboxStatements(), nil); err != nil {
		return fail(notAccepted, e.Name, s.Outbox, err.Error()).
			Fix("do not write a statement with the name of one that the description makes")
	}
	return nil
}

func (cfg *Config) makeELTDestination(e *ELT, fail func(string, ...any) *diag.Diagnostic) error {
	d := e.Destination
	dest, err := cfg.eltConn(e, d.Connection, "destination", fail)
	if err != nil {
		return err
	}
	if !contains(ELTDestinationDrivers, dest.Driver) {
		return fail("the destination of [elt.%s], [sql.%s], is a %s database, and a description makes the statements of %s only", e.Name, d.Connection, dest.Driver, strings.Join(ELTDestinationDrivers, ", ")).
			Fix("write the statements by hand, as samples/async-elt does")
	}
	if !dest.Writes() {
		return fail("the destination of [elt.%s], [sql.%s], has to have mode = \"write\"", e.Name, d.Connection).
			Fix(`add mode = "write" to the section`)
	}
	statements, transactions := e.destinationStatements(dest.Driver)
	if err := dest.addGenerated(statements, transactions); err != nil {
		return fail(notAccepted, e.Name, d.Connection, err.Error()).
			Fix("check `set`, `reject` and the names of the tables in the destination of the description")
	}
	return nil
}

// addGenerated reads the statements and the transactions that a description makes into the connection.
// One that the connection has already, written by hand, is a problem: the description does not
// replace it silently.
func (c *SQLConn) addGenerated(statements []generated, transactions []generatedTx) error {
	for _, g := range statements {
		if c.Statements[g.name] != nil {
			return fmt.Errorf("the statement `%s` is written in [sql.%s.statements], and the description makes one with that name", g.name, c.Name)
		}
		st, err := readStatement(g.name, g.value, c.Writes())
		if err != nil {
			return fmt.Errorf("the statement `%s` %s", g.name, err.Error())
		}
		c.Statements[g.name] = st
	}
	for _, g := range transactions {
		if c.Transactions[g.name] != nil || c.Statements[g.name] != nil {
			return fmt.Errorf("the transaction `%s` is written in [sql.%s], and the description makes one with that name", g.name, c.Name)
		}
		tx, err := readTransaction(c, g.name, anyStrings(g.steps))
		if err != nil {
			return fmt.Errorf("the transaction `%s` %s", g.name, err.Error())
		}
		c.Transactions[g.name] = tx
	}
	return nil
}

func anyStrings(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

func rowsOf(sql, result string) map[string]any {
	return map[string]any{"sql": sql, "result": result}
}

// ---- the source ----

// maskedColumn is the expression that reads a column of the source, with the mask of the description.
func (e *ELT) maskedColumn(driver, column string) string {
	expression := column
	if own, ok := e.Source.Select[column]; ok {
		expression = own
	}
	masked := false
	if n, ok := e.Source.Mask[column]; ok {
		masked = true
		switch driver {
		case "postgres":
			expression = fmt.Sprintf("'***' || right(%s, %d)", expression, n)
		case "mysql", "mariadb":
			expression = fmt.Sprintf("CONCAT('***', RIGHT(%s, %d))", expression, n)
		case "sqlserver":
			expression = fmt.Sprintf("'***' + RIGHT(%s, %d)", expression, n)
		case "oracle":
			expression = fmt.Sprintf("'***' || SUBSTR(%s, -%d)", expression, n)
		default:
			expression = fmt.Sprintf("'***' || substr(%s, -%d)", expression, n)
		}
	}
	switch {
	case driver == "oracle":
		return fmt.Sprintf("%s AS \"%s\"", expression, column) // Oracle gives the names in capitals unless they are quoted
	case masked || expression != column:
		return fmt.Sprintf("%s AS %s", expression, column)
	}
	return column
}

// keyExpression is how the key is read in the WHERE and the ORDER BY: as the column of the table, or as the
// expression that `select` gives for it (a name in a SELECT cannot be used there in every database).
func (e *ELT) keyExpression() string {
	if own, ok := e.Source.Select[e.Key]; ok {
		return own
	}
	return e.Key
}

// sourceStatements are `page` and `range`: a page of the table by key, and the same rows again by the
// edges that the outbox saved.
func (e *ELT) sourceStatements(driver string) []generated {
	list := make([]string, len(e.Columns))
	for i, column := range e.Columns {
		list[i] = e.maskedColumn(driver, column)
	}
	columns := strings.Join(list, ", ")
	s, key := e.Source, e.keyExpression()
	page := fmt.Sprintf("SELECT %s FROM %s WHERE %s > :after ORDER BY %s LIMIT :size", columns, s.Table, key, key)
	switch driver {
	case "sqlserver":
		page = fmt.Sprintf("SELECT TOP (:size) %s FROM %s WHERE %s > :after ORDER BY %s", columns, s.Table, key, key)
	case "oracle":
		page = fmt.Sprintf("SELECT %s FROM %s WHERE %s > :after ORDER BY %s FETCH FIRST :size ROWS ONLY", columns, s.Table, key, key)
	}
	rng := fmt.Sprintf("SELECT %s FROM %s WHERE %s > :after AND %s <= :upto ORDER BY %s", columns, s.Table, key, key, key)
	return []generated{{"page", rowsOf(page, ResultRows)}, {"range", rowsOf(rng, ResultRows)}}
}

// eltOutboxStatements are the statements of the outbox of the Extractor, whose table is in
// samples/async-elt/migrations/outbox.sql.
func eltOutboxStatements() []generated {
	return []generated{
		{"version", rowsOf("SELECT schema_version FROM outbox_meta", ResultValue)},
		// A value that came from the command line is a text: these make it a whole number, and halve it.
		{"int", rowsOf("SELECT CAST(:n AS INTEGER)", ResultValue)},
		{"half", rowsOf("SELECT max(1, CAST(:n AS INTEGER) / 2)", ResultValue)},
		{"next_seq", rowsOf("SELECT coalesce(max(seq), 0) + 1 FROM outbox WHERE job_id = :job", ResultValue)},
		{"last_upto", rowsOf("SELECT coalesce(max(upto_key), 0) FROM outbox WHERE job_id = :job", ResultValue)},
		{"next_planned", rowsOf("SELECT seq, after_key, upto_key FROM outbox WHERE job_id = :job AND state = 'planned' ORDER BY seq LIMIT 1", ResultRow)},
		{"plan", "INSERT INTO outbox (job_id, seq, after_key, upto_key, row_count, state) VALUES (:job, :seq, :after_key, :upto_key, :row_count, 'planned')"},
		{"confirm", "UPDATE outbox SET state = 'published', published_at = CURRENT_TIMESTAMP WHERE job_id = :job AND seq = :seq AND state = 'planned'"},
		{"edges", rowsOf("SELECT after_key, upto_key FROM outbox WHERE job_id = :job AND seq = :seq", ResultRow)},
		{"totals", rowsOf("SELECT count(*) AS batches, coalesce(sum(row_count), 0) AS rows FROM outbox WHERE job_id = :job", ResultRow)},
	}
}
