package config

import (
	"fmt"
	"sort"
	"strings"
)

// The statements of the Worker, for each database of a destination. The control tables are the same for all
// of them (etl_jobs, etl_batches, ...), and so are the names and the values of the statements, because the
// Worker calls them by name; what differs is the SQL. PostgreSQL and SQLite have an upsert and an
// INSERT ... ON CONFLICT DO NOTHING. SQL Server and Oracle write "insert if it is not there" as an INSERT ... SELECT ...
// WHERE NOT EXISTS (with FROM dual in Oracle), and an upsert as an UPDATE followed by such an INSERT, as
// samples/async-elt does, which is tried against real servers.

// eltDialect is the database of a destination.
type eltDialect string

func (d eltDialect) onConflict() bool { return d == "sqlite" || d == "postgres" }

// now is the current time.
func (d eltDialect) now() string {
	switch d {
	case "postgres":
		return "now()"
	case "sqlserver":
		return "SYSUTCDATETIME()"
	case "oracle":
		return "SYSTIMESTAMP"
	}
	return "CURRENT_TIMESTAMP"
}

// text asks for a parameter to be taken as text, which PostgreSQL wants when it cannot tell its type.
func (d eltDialect) text(param string) string {
	if d == "postgres" {
		return "CAST(" + param + " AS text)"
	}
	return param
}

// excluded is how the row that was not inserted is called in an upsert.
func (d eltDialect) excluded() string {
	if d == "postgres" {
		return "EXCLUDED"
	}
	return "excluded"
}

// older is the condition "older than N days" for a column, N being a parameter.
func (d eltDialect) older(column, param string) string {
	switch d {
	case "postgres":
		return fmt.Sprintf("%s < now() - make_interval(days => CAST(CAST(%s AS text) AS integer))", column, param)
	case "sqlserver":
		return fmt.Sprintf("%s < DATEADD(day, -CAST(%s AS INT), SYSUTCDATETIME())", column, param)
	case "oracle":
		return fmt.Sprintf("%s < SYSTIMESTAMP - NUMTODSINTERVAL(%s, 'DAY')", column, param)
	}
	return fmt.Sprintf("%s < datetime('now', '-' || CAST(%s AS INTEGER) || ' days')", column, param)
}

// column reads a column into the answer of a statement. Oracle gives the names in capitals unless they are quoted.
func (d eltDialect) column(name string) string {
	if d == "oracle" {
		return fmt.Sprintf(`%s AS "%s"`, name, name)
	}
	return name
}

// named reads an expression into the answer of a statement under a name.
func (d eltDialect) named(expression, name string) string {
	if d == "oracle" {
		return fmt.Sprintf(`%s AS "%s"`, expression, name)
	}
	return fmt.Sprintf("%s AS %s", expression, name)
}

// empty is what stands for "no reason" in the answer of job_status: an empty text is nothing in Oracle.
func (d eltDialect) empty() string {
	if d == "oracle" {
		return "'-'"
	}
	return "''"
}

// lastRows is a subquery of the last n batches of the job, newest first.
func (d eltDialect) lastRows(n int) string {
	switch d {
	case "sqlserver":
		return fmt.Sprintf("SELECT TOP %d state FROM etl_batches WHERE job_id = :job ORDER BY seq DESC", n)
	case "oracle":
		return fmt.Sprintf("SELECT state FROM etl_batches WHERE job_id = :job ORDER BY seq DESC FETCH FIRST %d ROWS ONLY", n)
	}
	return fmt.Sprintf("SELECT state FROM etl_batches WHERE job_id = :job ORDER BY seq DESC LIMIT %d", n)
}

// insertMissing is an INSERT that does nothing for a row that is there already. keys say which of the columns
// tell one row from another; withTarget names them in ON CONFLICT, where the database wants it named.
func (d eltDialect) insertMissing(table string, columns, values []string, keys []int, withTarget bool) string {
	list := strings.Join(columns, ", ")
	if d.onConflict() {
		target := ""
		if withTarget {
			names := make([]string, len(keys))
			for i, k := range keys {
				names[i] = columns[k]
			}
			target = "(" + strings.Join(names, ", ") + ") "
		}
		return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT %sDO NOTHING", table, list, strings.Join(values, ", "), target)
	}
	same := make([]string, len(keys))
	for i, k := range keys {
		same[i] = fmt.Sprintf("%s = %s", columns[k], values[k])
	}
	from := ""
	if d == "oracle" {
		from = " FROM dual"
	}
	return fmt.Sprintf("INSERT INTO %s (%s) SELECT %s%s WHERE NOT EXISTS (SELECT 1 FROM %s WHERE %s)",
		table, list, strings.Join(values, ", "), from, table, strings.Join(same, " AND "))
}

// rejected is the condition that holds for a row that one of the rules rejects.
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
	d := eltDialect(driver)
	w := &worker{e: e, d: d, stg: e.Destination.Staging}
	w.control()
	w.landing()
	w.transformation()
	w.brakes()
	w.closing()
	w.cleaning()
	return w.out, w.transactions()
}

// worker collects the statements as they are made.
type worker struct {
	e         *ELT
	d         eltDialect
	stg       string
	out       []generated
	transform []string
}

func (w *worker) add(name string, value any) { w.out = append(w.out, generated{name, value}) }

// control are the statements that read the control tables.
func (w *worker) control() {
	d := w.d
	w.add("version", rowsOf("SELECT schema_version FROM etl_meta", ResultValue))
	w.add("batch_state", rowsOf("SELECT state FROM etl_batches WHERE job_id = :job AND seq = :seq", ResultValue))
	w.add("batch_counts", rowsOf(fmt.Sprintf("SELECT %s, %s, %s FROM etl_batches WHERE job_id = :job AND seq = :seq",
		d.column("rows_read"), d.column("rows_loaded"), d.column("rows_rejected")), ResultRow))
	w.add("job_state", rowsOf("SELECT state FROM etl_jobs WHERE job_id = :job", ResultValue))
	// A job that is not known yet gives nothing; a job that a brake stopped says why.
	w.add("job_status", rowsOf(fmt.Sprintf("SELECT %s, %s FROM etl_jobs WHERE job_id = :job",
		d.column("state"), d.named(fmt.Sprintf("coalesce(pause_reason, %s)", d.empty()), "reason")), ResultRow))
}

// landing is transaction 1: the rows go to staging and the batch is marked, all or nothing. A copy of a row is harmless.
func (w *worker) landing() {
	d, e := w.d, w.e
	w.add("open_job", d.insertMissing("etl_jobs", []string{"job_id"}, []string{":job"}, []int{0}, true))
	columns := append([]string{"job_id", "seq"}, e.Columns...)
	values := []string{":job", ":seq"}
	keys := []int{0, 1}
	for i, column := range e.Columns {
		values = append(values, ":"+column)
		if column == e.Key {
			keys = append(keys, i+2)
		}
	}
	w.add("land", map[string]any{"sql": d.insertMissing(w.stg, columns, values, keys, true), "each": "rows", "columns": anyStrings(e.Columns)})
	w.add("mark_landed", d.insertMissing("etl_batches", []string{"job_id", "seq", "state", "rows_read"},
		[]string{":job", ":seq", "'landed'", ":row_count"}, []int{0, 1}, true))
}

// transformation is transaction 2: the rules, the upsert into the final table, and the batch marked done.
func (w *worker) transformation() {
	d, e, dest := w.d, w.e, w.e.Destination
	// `reject_share` counts the same rules, to tell the brake how much of a batch would be rejected. A row that breaks a
	// rule goes to etl_rejects with a code, and the batch goes on.
	share := fmt.Sprintf("coalesce(100.0 * sum(CASE WHEN %s THEN 1 ELSE 0 END) / NULLIF(count(*), 0), 0)", e.rejected())
	if d.onConflict() {
		share = fmt.Sprintf("coalesce(100.0 * sum(CASE WHEN %s THEN 1 ELSE 0 END) / count(*), 0)", e.rejected())
	}
	if d == "postgres" {
		share = "CAST(" + share + " AS DOUBLE PRECISION)"
	}
	w.add("reject_share", rowsOf(fmt.Sprintf("SELECT %s FROM %s WHERE job_id = :job AND seq = :seq", share, w.stg), ResultValue))
	for i, rule := range dest.Reject {
		name := fmt.Sprintf("reject_%d", i+1)
		w.add(name, w.reject(rule))
		w.transform = append(w.transform, name)
	}
	w.upsert()
	counted := "SELECT count(DISTINCT source_key) FROM etl_rejects WHERE job_id = :job AND seq = :seq"
	w.add("finish_batch", fmt.Sprintf("UPDATE etl_batches SET state = 'done', rows_rejected = (%s), rows_loaded = rows_read - (%s), done_at = %s, transform_version = %s, last_error_code = NULL WHERE job_id = :job AND seq = :seq",
		counted, counted, d.now(), d.text(":version")))
	w.transform = append(w.transform, "finish_batch")
}

// reject writes a row that breaks a rule in etl_rejects, with its key and the code, never its content.
func (w *worker) reject(rule ELTRule) string {
	e := w.e
	if w.d.onConflict() {
		return fmt.Sprintf("INSERT INTO etl_rejects (job_id, seq, source_key, reason_code) SELECT job_id, seq, %s, '%s' FROM %s WHERE job_id = :job AND seq = :seq AND (%s) ON CONFLICT DO NOTHING",
			e.Key, rule.Code, w.stg, rule.When)
	}
	return fmt.Sprintf("INSERT INTO etl_rejects (job_id, seq, source_key, reason_code) SELECT s.job_id, s.seq, s.%s, '%s' FROM %s s WHERE s.job_id = :job AND s.seq = :seq AND (%s) "+
		"AND NOT EXISTS (SELECT 1 FROM etl_rejects r WHERE r.job_id = s.job_id AND r.seq = s.seq AND r.source_key = s.%s AND r.reason_code = '%s')",
		e.Key, rule.Code, w.stg, rule.When, e.Key, rule.Code)
}

// upsert makes the statements that put the rows of a batch in the final table.
func (w *worker) upsert() {
	d, dest := w.d, w.e.Destination
	targets := make([]string, 0, len(dest.Set))
	for column := range dest.Set {
		targets = append(targets, column)
	}
	sort.Strings(targets)
	values := make([]string, len(targets))
	for i, column := range targets {
		values[i] = dest.Set[column]
	}
	if d.onConflict() {
		updates := []string{}
		for _, column := range targets {
			if column != dest.UpsertOn {
				updates = append(updates, fmt.Sprintf("%s = %s.%s", column, d.excluded(), column))
			}
		}
		conflict := "DO NOTHING"
		if len(updates) > 0 {
			conflict = "DO UPDATE SET " + strings.Join(updates, ", ")
		}
		w.add("load", fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s WHERE job_id = :job AND seq = :seq AND CASE WHEN %s THEN 1 ELSE 0 END = 0 ON CONFLICT (%s) %s",
			dest.Table, strings.Join(targets, ", "), strings.Join(values, ", "), w.stg, w.e.rejected(), dest.UpsertOn, conflict))
		w.transform = append(w.transform, "load")
		return
	}
	// The rows to load, with the columns of the final table: the expressions are read against staging alone, so a
	// name that both tables have cannot be taken for the wrong one.
	parts := make([]string, len(targets))
	for i, column := range targets {
		parts[i] = values[i] + " AS " + column
	}
	rows := fmt.Sprintf("(SELECT %s FROM %s WHERE job_id = :job AND seq = :seq AND CASE WHEN %s THEN 1 ELSE 0 END = 0) s",
		strings.Join(parts, ", "), w.stg, w.e.rejected())
	var changed []string
	for _, column := range targets {
		if column != dest.UpsertOn {
			changed = append(changed, column)
		}
	}
	if len(changed) > 0 {
		w.add("update_final", w.update(rows, changed))
		w.transform = append(w.transform, "update_final")
	}
	picked := make([]string, len(targets))
	for i, column := range targets {
		picked[i] = "s." + column
	}
	w.add("load", fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s WHERE NOT EXISTS (SELECT 1 FROM %s f WHERE f.%s = s.%s)",
		dest.Table, strings.Join(targets, ", "), strings.Join(picked, ", "), rows, dest.Table, dest.UpsertOn, dest.UpsertOn))
	w.transform = append(w.transform, "load")
}

// update changes the rows of the final table that the batch has a new version of.
func (w *worker) update(rows string, changed []string) string {
	dest := w.e.Destination
	if w.d == "sqlserver" {
		sets := make([]string, len(changed))
		for i, column := range changed {
			sets[i] = fmt.Sprintf("%s = s.%s", column, column)
		}
		return fmt.Sprintf("UPDATE f SET %s FROM %s f JOIN %s ON s.%s = f.%s", strings.Join(sets, ", "), dest.Table, rows, dest.UpsertOn, dest.UpsertOn)
	}
	picked := make([]string, len(changed))
	for i, column := range changed {
		picked[i] = "s." + column
	}
	return fmt.Sprintf("UPDATE %s f SET (%s) = (SELECT %s FROM %s WHERE s.%s = f.%s) WHERE f.%s IN (SELECT s.%s FROM %s)",
		dest.Table, strings.Join(changed, ", "), strings.Join(picked, ", "), rows, dest.UpsertOn, dest.UpsertOn, dest.UpsertOn, dest.UpsertOn, rows)
}

// brakes are a batch that a brake stopped, and the brake of the whole job: when the last batches (by their number)
// are all stopped, the job is paused, because a rate of rejection that high is a change in the source and not bad luck.
func (w *worker) brakes() {
	d, dest := w.d, w.e.Destination
	w.add("fail_batch", fmt.Sprintf("UPDATE etl_batches SET state = 'failed', failed_at = %s, attempts = attempts + 1, last_error_code = %s WHERE job_id = :job AND seq = :seq AND state <> 'done'",
		d.now(), d.text(":code")))
	w.add("pause_if_failing", fmt.Sprintf("UPDATE etl_jobs SET state = 'paused', pause_reason = 'QUALITY' WHERE job_id = :job AND state = 'running' AND (SELECT count(*) FROM (%s) last_batches WHERE state = 'failed') = %d",
		d.lastRows(dest.PauseAfter), dest.PauseAfter))
	w.add("resume_job", "UPDATE etl_jobs SET state = 'running', pause_reason = NULL WHERE job_id = :job AND state = 'paused'")
	w.add("forget_alerts", "DELETE FROM etl_alerts WHERE job_id = :job AND kind IN ('JOB_PAUSED', 'BATCH_FAILED', 'BATCH_STUCK')")
}

// closing are the totals the Extractor announced, and the one conditional UPDATE that closes the job (two Workers that
// try at the same time close it once). It is `done` when every batch is done and the rows add up.
func (w *worker) closing() {
	d := w.d
	w.add("set_totals", fmt.Sprintf("UPDATE etl_jobs SET total_batches = :batches, total_rows = :rows, totals_at = %s WHERE job_id = :job", d.now()))
	w.add("try_close", fmt.Sprintf("UPDATE etl_jobs SET state = CASE WHEN (SELECT coalesce(sum(rows_loaded + rows_rejected), 0) FROM etl_batches WHERE job_id = :job AND state = 'done') = total_rows THEN 'done' ELSE 'mismatch' END, finished_at = %s WHERE job_id = :job AND state = 'running' AND total_batches IS NOT NULL AND (SELECT count(*) FROM etl_batches WHERE job_id = :job AND state = 'done') = total_batches", d.now()))
	// An event the Worker refused for what it is (a code and a place, never the content): the sweeper tells the team.
	w.add("record_incident", w.d.insertMissing("etl_incidents", []string{"job_id", "seq", "code"},
		[]string{":job", ":seq", w.d.text(":code")}, []int{0, 1, 2}, false))
}

// cleaning is staging, which holds the rows as they came and is cleaned (the batches done more than N days ago), and the
// control tables, much later: the jobs that are `done`, ended more than N days ago and have nothing left in staging.
func (w *worker) cleaning() {
	d := w.d
	purge := fmt.Sprintf("DELETE FROM %s s WHERE EXISTS (SELECT 1 FROM etl_batches b WHERE b.job_id = s.job_id AND b.seq = s.seq AND b.state = 'done' AND %s)",
		w.stg, d.older("b.done_at", ":days"))
	switch d {
	case "sqlite":
		purge = fmt.Sprintf("DELETE FROM %s AS s WHERE EXISTS (SELECT 1 FROM etl_batches b WHERE b.job_id = s.job_id AND b.seq = s.seq AND b.state = 'done' AND %s)",
			w.stg, d.older("b.done_at", ":days"))
	case "sqlserver", "oracle":
		purge = fmt.Sprintf("DELETE FROM %s WHERE EXISTS (SELECT 1 FROM etl_batches b WHERE b.job_id = %s.job_id AND b.seq = %s.seq AND b.state = 'done' AND %s)",
			w.stg, w.stg, w.stg, d.older("b.done_at", ":days"))
	}
	w.add("purge", purge)
	old := fmt.Sprintf("job_id IN (SELECT j.job_id FROM etl_jobs j WHERE j.state = 'done' AND %s AND NOT EXISTS (SELECT 1 FROM %s s WHERE s.job_id = j.job_id))",
		d.older("j.finished_at", ":days"), w.stg)
	for _, table := range []string{"alerts", "incidents", "resends", "rejects", "batches", "jobs"} {
		w.add("old_"+table, fmt.Sprintf("DELETE FROM etl_%s WHERE %s", table, old))
	}
}

func (w *worker) transactions() []generatedTx {
	return []generatedTx{
		{"land_batch", []string{"open_job", "land", "mark_landed"}},
		{"transform_batch", w.transform},
		{"register_totals", []string{"open_job", "set_totals"}},
		{"resume", []string{"resume_job", "forget_alerts"}},
		{"purge_control", []string{"old_alerts", "old_incidents", "old_resends", "old_rejects", "old_batches", "old_jobs"}},
	}
}
