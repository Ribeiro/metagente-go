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
var ELTDestinationDrivers = []string{"sqlite", "postgres"}

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
		return fail("[elt.%s] makes a statement that [sql.%s] does not accept: %s", e.Name, s.Connection, err.Error()).
			Fix("check `columns`, `mask` and `select` in the source of the description")
	}
	if err := outbox.addGenerated(eltOutboxStatements(), nil); err != nil {
		return fail("[elt.%s] makes a statement that [sql.%s] does not accept: %s", e.Name, s.Outbox, err.Error()).
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
		return fail("the destination of [elt.%s], [sql.%s], is a %s database, and a description makes the statements of %s only", e.Name, d.Connection, dest.Driver, strings.Join(ELTDestinationDrivers, " and ")).
			Fix("write the statements by hand, as samples/async-elt does for SQL Server and Oracle")
	}
	if !dest.Writes() {
		return fail("the destination of [elt.%s], [sql.%s], has to have mode = \"write\"", e.Name, d.Connection).
			Fix(`add mode = "write" to the section`)
	}
	statements, transactions := e.destinationStatements(dest.Driver)
	if err := dest.addGenerated(statements, transactions); err != nil {
		return fail("[elt.%s] makes a statement that [sql.%s] does not accept: %s", e.Name, d.Connection, err.Error()).
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

// ---- the destination ----

// eltDialect holds what differs between the databases of a destination.
type eltDialect struct {
	postgres bool
}

// now is the current time.
func (d eltDialect) now() string {
	if d.postgres {
		return "now()"
	}
	return "CURRENT_TIMESTAMP"
}

// text asks for a parameter to be taken as text, which PostgreSQL wants when it cannot tell its type.
func (d eltDialect) text(param string) string {
	if d.postgres {
		return "CAST(" + param + " AS text)"
	}
	return param
}

// excluded is how the row that was not inserted is called in an upsert.
func (d eltDialect) excluded() string {
	if d.postgres {
		return "EXCLUDED"
	}
	return "excluded"
}

// before is the condition "older than N days" (or minutes) for a column.
func (d eltDialect) older(column, param, unit string) string {
	if d.postgres {
		return fmt.Sprintf("%s < now() - make_interval(%s => CAST(CAST(%s AS text) AS integer))", column, unit, param)
	}
	return fmt.Sprintf("%s < datetime('now', '-' || CAST(%s AS INTEGER) || ' %s')", column, param, unit)
}

// deleteFrom is the start of a DELETE of a table with a name for its rows.
func (d eltDialect) deleteFrom(table, alias string) string {
	if d.postgres {
		return fmt.Sprintf("DELETE FROM %s %s", table, alias)
	}
	return fmt.Sprintf("DELETE FROM %s AS %s", table, alias)
}

// rejects is the condition that holds for a row that one of the rules rejects, as 1 or 0, or "" when there
// are no rules.
func (e *ELT) rejected() string {
	if len(e.Destination.Reject) == 0 {
		return "1 = 0"
	}
	parts := make([]string, len(e.Destination.Reject))
	for i, rule := range e.Destination.Reject {
		parts[i] = "(" + rule.When + ")"
	}
	return strings.Join(parts, " OR ")
}

// destinationStatements are the statements and the transactions of the Worker.
func (e *ELT) destinationStatements(driver string) ([]generated, []generatedTx) {
	d := eltDialect{postgres: driver == "postgres"}
	dest := e.Destination
	stg := dest.Staging
	columns := strings.Join(e.Columns, ", ")
	params := make([]string, len(e.Columns))
	for i, column := range e.Columns {
		params[i] = ":" + column
	}
	share := fmt.Sprintf("SELECT coalesce(100.0 * sum(CASE WHEN %s THEN 1 ELSE 0 END) / count(*), 0) FROM %s WHERE job_id = :job AND seq = :seq", e.rejected(), stg)
	if d.postgres {
		share = fmt.Sprintf("SELECT CAST(coalesce(100.0 * sum(CASE WHEN %s THEN 1 ELSE 0 END) / count(*), 0) AS DOUBLE PRECISION) FROM %s WHERE job_id = :job AND seq = :seq", e.rejected(), stg)
	}
	var out []generated
	add := func(name string, value any) { out = append(out, generated{name, value}) }

	add("version", rowsOf("SELECT schema_version FROM etl_meta", ResultValue))
	add("batch_state", rowsOf("SELECT state FROM etl_batches WHERE job_id = :job AND seq = :seq", ResultValue))
	add("batch_counts", rowsOf("SELECT rows_read, rows_loaded, rows_rejected FROM etl_batches WHERE job_id = :job AND seq = :seq", ResultRow))
	add("job_state", rowsOf("SELECT state FROM etl_jobs WHERE job_id = :job", ResultValue))
	// A job that is not known yet gives nothing; a job that a brake stopped says why.
	add("job_status", rowsOf("SELECT state, coalesce(pause_reason, '') AS reason FROM etl_jobs WHERE job_id = :job", ResultRow))

	// Transaction 1, "land": the rows go to staging and the batch is marked, all or nothing. A copy of a row is harmless.
	add("open_job", "INSERT INTO etl_jobs (job_id) VALUES (:job) ON CONFLICT (job_id) DO NOTHING")
	add("land", map[string]any{
		"sql":  fmt.Sprintf("INSERT INTO %s (job_id, seq, %s) VALUES (:job, :seq, %s) ON CONFLICT (job_id, seq, %s) DO NOTHING", stg, columns, strings.Join(params, ", "), e.Key),
		"each": "rows", "columns": anyStrings(e.Columns),
	})
	add("mark_landed", "INSERT INTO etl_batches (job_id, seq, state, rows_read) VALUES (:job, :seq, 'landed', :row_count) ON CONFLICT (job_id, seq) DO NOTHING")

	// The rules of the transformation. A row that breaks one goes to etl_rejects with a code, and the batch
	// goes on. `reject_share` counts the same rules, to tell the brake how much of a batch would be rejected.
	add("reject_share", rowsOf(share, ResultValue))
	transform := []string{}
	for i, rule := range dest.Reject {
		name := fmt.Sprintf("reject_%d", i+1)
		add(name, fmt.Sprintf("INSERT INTO etl_rejects (job_id, seq, source_key, reason_code) SELECT job_id, seq, %s, '%s' FROM %s WHERE job_id = :job AND seq = :seq AND (%s) ON CONFLICT DO NOTHING",
			e.Key, rule.Code, stg, rule.When))
		transform = append(transform, name)
	}

	// Transaction 2, "transform": the upsert into the final table, and the batch marked done, all or nothing.
	targets := make([]string, 0, len(dest.Set))
	for column := range dest.Set {
		targets = append(targets, column)
	}
	sort.Strings(targets)
	values := make([]string, len(targets))
	updates := []string{}
	for i, column := range targets {
		values[i] = dest.Set[column]
		if column != dest.UpsertOn {
			updates = append(updates, fmt.Sprintf("%s = %s.%s", column, d.excluded(), column))
		}
	}
	conflict := "DO NOTHING"
	if len(updates) > 0 {
		conflict = "DO UPDATE SET " + strings.Join(updates, ", ")
	}
	add("load", fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s WHERE job_id = :job AND seq = :seq AND CASE WHEN %s THEN 1 ELSE 0 END = 0 ON CONFLICT (%s) %s",
		dest.Table, strings.Join(targets, ", "), strings.Join(values, ", "), stg, e.rejected(), dest.UpsertOn, conflict))
	transform = append(transform, "load")
	counted := "SELECT count(DISTINCT source_key) FROM etl_rejects WHERE job_id = :job AND seq = :seq"
	add("finish_batch", fmt.Sprintf("UPDATE etl_batches SET state = 'done', rows_rejected = (%s), rows_loaded = rows_read - (%s), done_at = %s, transform_version = %s, last_error_code = NULL WHERE job_id = :job AND seq = :seq",
		counted, counted, d.now(), d.text(":version")))
	transform = append(transform, "finish_batch")

	// A batch that a brake stopped, and the brake of the whole job: when the last batches (by their number) are
	// all stopped, the job is paused, because a rate of rejection that high is a change in the source and not bad luck.
	add("fail_batch", fmt.Sprintf("UPDATE etl_batches SET state = 'failed', failed_at = %s, attempts = attempts + 1, last_error_code = %s WHERE job_id = :job AND seq = :seq AND state <> 'done'",
		d.now(), d.text(":code")))
	add("pause_if_failing", fmt.Sprintf("UPDATE etl_jobs SET state = 'paused', pause_reason = 'QUALITY' WHERE job_id = :job AND state = 'running' AND (SELECT count(*) FROM (SELECT state FROM etl_batches WHERE job_id = :job ORDER BY seq DESC LIMIT %d) last_batches WHERE state = 'failed') = %d",
		dest.PauseAfter, dest.PauseAfter))
	add("resume_job", "UPDATE etl_jobs SET state = 'running', pause_reason = NULL WHERE job_id = :job AND state = 'paused'")
	add("forget_alerts", "DELETE FROM etl_alerts WHERE job_id = :job AND kind IN ('JOB_PAUSED', 'BATCH_FAILED', 'BATCH_STUCK')")

	// The end of a job: the totals the Extractor announced, and the one conditional UPDATE that closes it (two Workers
	// that try at the same time close it once). It is `done` when every batch is done and the rows add up.
	add("set_totals", fmt.Sprintf("UPDATE etl_jobs SET total_batches = :batches, total_rows = :rows, totals_at = %s WHERE job_id = :job", d.now()))
	add("try_close", fmt.Sprintf("UPDATE etl_jobs SET state = CASE WHEN (SELECT coalesce(sum(rows_loaded + rows_rejected), 0) FROM etl_batches WHERE job_id = :job AND state = 'done') = total_rows THEN 'done' ELSE 'mismatch' END, finished_at = %s WHERE job_id = :job AND state = 'running' AND total_batches IS NOT NULL AND (SELECT count(*) FROM etl_batches WHERE job_id = :job AND state = 'done') = total_batches", d.now()))

	// An event the Worker refused for what it is (a code and a place, never the content): the sweeper tells the team.
	add("record_incident", fmt.Sprintf("INSERT INTO etl_incidents (job_id, seq, code) VALUES (:job, :seq, %s) ON CONFLICT DO NOTHING", d.text(":code")))

	// Staging holds the rows as they came, so it is cleaned: the batches done more than N days ago.
	add("purge", fmt.Sprintf("%s WHERE EXISTS (SELECT 1 FROM etl_batches b WHERE b.job_id = s.job_id AND b.seq = s.seq AND b.state = 'done' AND %s)",
		d.deleteFrom(stg, "s"), d.older("b.done_at", ":days", "days")))
	// The control tables are cleaned too: the jobs that are `done`, ended more than N days ago and have nothing left in staging.
	old := fmt.Sprintf("job_id IN (SELECT j.job_id FROM etl_jobs j WHERE j.state = 'done' AND %s AND NOT EXISTS (SELECT 1 FROM %s s WHERE s.job_id = j.job_id))",
		d.older("j.finished_at", ":days", "days"), stg)
	purged := []string{}
	for _, table := range []string{"alerts", "incidents", "resends", "rejects", "batches", "jobs"} {
		add("old_"+table, fmt.Sprintf("DELETE FROM etl_%s WHERE %s", table, old))
		purged = append(purged, "old_"+table)
	}

	transactions := []generatedTx{
		{"land_batch", []string{"open_job", "land", "mark_landed"}},
		{"transform_batch", transform},
		{"register_totals", []string{"open_job", "set_totals"}},
		{"resume", []string{"resume_job", "forget_alerts"}},
		{"purge_control", purged},
	}
	return out, transactions
}
