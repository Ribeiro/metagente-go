package config

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

// ELT is a section [elt.NAME]: the description of one copy of a table by the asynchronous ELT. The agents
// of the Extractor and of the Worker, and the statements that they call, are made from it (see
// docs/design-async-elt.md and docs/tutorial-elt.md).
type ELT struct {
	Name string
	// Key is the column that orders the table and marks where a batch ends; a whole number that grows.
	Key string
	// Columns are the columns of the batch event, in order. The key is one of them.
	Columns []string
	// Source is what only the Extractor needs, and Destination what only the Worker needs. Either may be
	// left out in the file of a machine that has only one of the two.
	Source      *ELTSource
	Destination *ELTDestination
	line        int
}

// ELTSource describes where the Extractor reads and where it keeps its outbox.
type ELTSource struct {
	// Connection is the [sql.NAME] that is read, Outbox the [sql.NAME] that the Extractor keeps (a SQLite
	// file), and Broker the [broker.NAME] where the batches are published.
	Connection string
	Outbox     string
	Broker     string
	// Table is the table that is read.
	Table string
	// Rows and Bytes are the most rows of a page and the size that a batch aims at before it is packed.
	Rows  int
	Bytes int
	// Mask says, for a column, how many of its last characters leave the source: the rest never does.
	Mask map[string]int
	// Select gives, for a column, the expression that reads it, in the dialect of the source.
	Select map[string]string
}

// ELTDestination describes where the Worker lands and loads the rows.
type ELTDestination struct {
	Connection string
	// Staging is the table where a batch lands as it came. It has job_id, seq and the columns of the event.
	Staging string
	// Table is the final table, UpsertOn its business key, and Set the expression, over the columns of
	// staging, that gives each of its columns.
	Table    string
	UpsertOn string
	Set      map[string]string
	// Reject are the rules of the transformation: a row for which a rule holds is rejected, with its code.
	Reject []ELTRule
	// RejectShare is the percent of a batch that may be rejected before the batch is stopped, and
	// PauseAfter how many batches in a row stopped this way pause the job.
	RejectShare float64
	PauseAfter  int
}

// ELTRule is a rule that rejects a row.
type ELTRule struct {
	When string
	Code string
}

// Defaults of a description.
const (
	DefaultELTRows        = 1000
	DefaultELTBytes       = 262144
	DefaultELTRejectShare = 20
	DefaultELTPauseAfter  = 3
)

var (
	eltIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	eltTable      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}(\.[A-Za-z_][A-Za-z0-9_]{0,62})?$`)
	eltCode       = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,39}$`)
	eltLast       = regexp.MustCompile(`^last ([1-9][0-9]?)$`)
)

// eltReserved are the names that the statements already use for the values of a call: a column with
// one of them would be taken for that value.
var eltReserved = map[string]bool{
	"job": true, "seq": true, "row_count": true, "rows": true, "version": true, "code": true, "batches": true,
	"days": true, "reason": true, "after": true, "upto": true, "size": true, "after_key": true, "upto_key": true,
}

// Uses says whether a description names the connection.
func (e *ELT) Uses(connection string) bool {
	if e.Source != nil && (e.Source.Connection == connection || e.Source.Outbox == connection) {
		return true
	}
	return e.Destination != nil && e.Destination.Connection == connection
}

// eltUses says whether a description names the connection, so that it may be left without statements:
// they are made from the description.
func (cfg *Config) eltUses(connection string) bool {
	for _, e := range cfg.ELT {
		if e.Uses(connection) {
			return true
		}
	}
	return false
}

// addELT reads one [elt.NAME] section. The line is the one of its header.
func (cfg *Config) addELT(file, text string, e entry) error {
	fail := func(format string, args ...any) *diag.Diagnostic {
		return diag.New(fmt.Sprintf(format, args...)).At(file, e.line, 1).WithSource(text)
	}
	if !eltIdentifier.MatchString(e.key) {
		return fail("`%s` is not a name for a description: use letters, digits and `_`", e.key).
			Fix("write the section like: [elt.orders]")
	}
	table, ok := e.value.(map[string]any)
	if !ok {
		return fail("`%s` in [elt] must be a section of its own", e.key).
			Fix("write one section for each table, for example [elt.orders], and put key and columns under it")
	}
	where := "[elt." + e.key + "]"
	elt := &ELT{Name: e.key, Key: "id", line: e.line}
	for _, key := range sortedKeys(table) {
		var err error
		switch key {
		case "key":
			elt.Key, err = sqlText(table[key])
		case "columns":
			elt.Columns, err = sqlTexts(table[key])
		case "source":
			elt.Source, err = readELTSource(table[key])
		case "destination":
			elt.Destination, err = readELTDestination(table[key])
		default:
			return fail("I do not know the setting `%s` in %s", key, where).
				Fix("a description has: key, columns, and the sections source and destination")
		}
		if err != nil {
			return fail("`%s` in %s %s", key, where, err.Error()).Fix("change it in " + FileName)
		}
	}
	if elt.Destination != nil && elt.Destination.Staging == "" {
		elt.Destination.Staging = "stg_" + e.key
	}
	if err := elt.check(); err != nil {
		return fail("%s %s", where, err.Error()).Fix("change it in " + FileName)
	}
	if _, twice := cfg.ELT[e.key]; twice {
		return fail("%s is written twice", where).Fix("keep one")
	}
	cfg.ELT[e.key] = elt
	return nil
}

func sortedKeys(table map[string]any) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// check looks at what the parts of a description say about each other.
func (e *ELT) check() error {
	if !eltIdentifier.MatchString(e.Key) {
		return fmt.Errorf("has the key `%s`, and a column is written with letters, digits and `_`", e.Key)
	}
	if err := e.checkColumns(); err != nil {
		return err
	}
	if e.Source != nil {
		if err := e.Source.check(e); err != nil {
			return fmt.Errorf("has a problem in source: %s", err.Error())
		}
	}
	if e.Destination != nil {
		if err := e.Destination.check(); err != nil {
			return fmt.Errorf("has a problem in destination: %s", err.Error())
		}
	}
	return nil
}

func (e *ELT) checkColumns() error {
	if len(e.Columns) == 0 {
		return fmt.Errorf("needs `columns`, the columns of the event, for example: columns = [\"id\", \"customer\", \"total\"]")
	}
	seen := map[string]bool{}
	for _, column := range e.Columns {
		switch {
		case !eltIdentifier.MatchString(column):
			return fmt.Errorf("has the column `%s`, and a column is written with letters, digits and `_`", column)
		case eltReserved[column]:
			return fmt.Errorf("has a column called `%s`, which the statements already use for something else; read it with another name in `select`", column)
		case seen[column]:
			return fmt.Errorf("has the column `%s` twice", column)
		}
		seen[column] = true
	}
	if !seen[e.Key] {
		return fmt.Errorf("has the key `%s`, and it is not one of the `columns`", e.Key)
	}
	return nil
}

func (s *ELTSource) check(e *ELT) error {
	switch {
	case s.Connection == "" || s.Outbox == "" || s.Broker == "":
		return fmt.Errorf("needs `connection` (the database that is read), `outbox` (the SQLite file that the Extractor keeps) and `broker`")
	case s.Table == "" || !eltTable.MatchString(s.Table):
		return fmt.Errorf("needs `table`, the table that is read, written with letters, digits and `_`")
	}
	for column := range s.Mask {
		if !contains(e.Columns, column) {
			return fmt.Errorf("masks `%s`, which is not one of the `columns`", column)
		}
	}
	for column := range s.Select {
		if !contains(e.Columns, column) {
			return fmt.Errorf("selects `%s`, which is not one of the `columns`", column)
		}
	}
	return nil
}

func (d *ELTDestination) check() error {
	switch {
	case d.Connection == "":
		return fmt.Errorf("needs `connection`, the database where the rows are loaded")
	case d.Table == "" || !eltTable.MatchString(d.Table):
		return fmt.Errorf("needs `table`, the final table, written with letters, digits and `_`")
	case d.Staging == "" || !eltTable.MatchString(d.Staging):
		return fmt.Errorf("has a `staging` that is not a table name: letters, digits and `_`")
	case d.RejectShare <= 0 || d.RejectShare > 100:
		return fmt.Errorf("has `reject_share` = %s, and it is a percent from above 0 to 100", strconv.FormatFloat(d.RejectShare, 'f', -1, 64))
	case d.PauseAfter < 1:
		return fmt.Errorf("has `pause_after` = %d, and it is at least 1", d.PauseAfter)
	}
	if err := d.checkSet(); err != nil {
		return err
	}
	return d.checkRules()
}

func (d *ELTDestination) checkSet() error {
	switch {
	case len(d.Set) == 0:
		return fmt.Errorf("needs `set`, what each column of the final table is made of, for example: set = { id = \"id\", total = \"total\" }")
	case d.UpsertOn == "" || !eltIdentifier.MatchString(d.UpsertOn):
		return fmt.Errorf("needs `upsert_on`, the column of the final table that tells a row from another")
	}
	if _, ok := d.Set[d.UpsertOn]; !ok {
		return fmt.Errorf("has `upsert_on` = `%s`, and `set` does not give that column", d.UpsertOn)
	}
	for column, expression := range d.Set {
		if !eltIdentifier.MatchString(column) {
			return fmt.Errorf("sets the column `%s`, and a column is written with letters, digits and `_`", column)
		}
		if strings.TrimSpace(expression) == "" {
			return fmt.Errorf("sets `%s` to nothing", column)
		}
	}
	return nil
}

func (d *ELTDestination) checkRules() error {
	codes := map[string]bool{}
	for _, rule := range d.Reject {
		switch {
		case strings.TrimSpace(rule.When) == "":
			return fmt.Errorf("has a rule in `reject` with no `when`")
		case !eltCode.MatchString(rule.Code):
			return fmt.Errorf("has the code `%s` in `reject`, and a code is made of capital letters, digits and `_`", rule.Code)
		case codes[rule.Code]:
			return fmt.Errorf("has the code `%s` twice in `reject`", rule.Code)
		}
		codes[rule.Code] = true
	}
	return nil
}

func contains(list []string, item string) bool {
	for _, x := range list {
		if x == item {
			return true
		}
	}
	return false
}

func readELTSource(value any) (*ELTSource, error) {
	table, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be a section, written as [elt.NAME.source]")
	}
	s := &ELTSource{Rows: DefaultELTRows, Bytes: DefaultELTBytes}
	for _, key := range sortedKeys(table) {
		var err error
		switch key {
		case "connection":
			s.Connection, err = sqlText(table[key])
		case "outbox":
			s.Outbox, err = sqlText(table[key])
		case "broker":
			s.Broker, err = sqlText(table[key])
		case "table":
			s.Table, err = sqlText(table[key])
		case "rows":
			s.Rows, err = positive(table[key])
		case "bytes":
			s.Bytes, err = positive(table[key])
		case "mask":
			s.Mask, err = readMask(table[key])
		case "select":
			s.Select, err = textTable(table[key])
		default:
			return nil, fmt.Errorf("I do not know the setting `%s` in source; it has: connection, outbox, broker, table, rows, bytes, mask and select", key)
		}
		if err != nil {
			return nil, fmt.Errorf("`%s` in source %s", key, err.Error())
		}
	}
	return s, nil
}

func readELTDestination(value any) (*ELTDestination, error) {
	table, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be a section, written as [elt.NAME.destination]")
	}
	d := &ELTDestination{RejectShare: DefaultELTRejectShare, PauseAfter: DefaultELTPauseAfter}
	for _, key := range sortedKeys(table) {
		var err error
		switch key {
		case "connection":
			d.Connection, err = sqlText(table[key])
		case "staging":
			d.Staging, err = sqlText(table[key])
		case "table":
			d.Table, err = sqlText(table[key])
		case "upsert_on":
			d.UpsertOn, err = sqlText(table[key])
		case "set":
			d.Set, err = textTable(table[key])
		case "reject":
			d.Reject, err = readRules(table[key])
		case "reject_share":
			d.RejectShare, err = number(table[key])
		case "pause_after":
			d.PauseAfter, err = positive(table[key])
		default:
			return nil, fmt.Errorf("I do not know the setting `%s` in destination; it has: connection, staging, table, upsert_on, set, reject, reject_share and pause_after", key)
		}
		if err != nil {
			return nil, fmt.Errorf("`%s` in destination %s", key, err.Error())
		}
	}
	return d, nil
}

func positive(value any) (int, error) {
	n, ok := value.(int64)
	if !ok || n < 1 || n > 1<<31 {
		return 0, fmt.Errorf("must be a whole number of at least 1")
	}
	return int(n), nil
}

func number(value any) (float64, error) {
	switch n := value.(type) {
	case int64:
		return float64(n), nil
	case float64:
		return n, nil
	}
	return 0, fmt.Errorf("must be a number")
}

// textTable reads a table of texts, such as { total = "round(total * 100)" }.
func textTable(value any) (map[string]string, error) {
	table, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be a table of texts, for example { total = \"round(total * 100)\" }")
	}
	out := make(map[string]string, len(table))
	for key, v := range table {
		text, err := sqlText(v)
		if err != nil {
			return nil, fmt.Errorf("must hold texts in quotes (`%s` does not)", key)
		}
		out[key] = text
	}
	return out, nil
}

// readMask reads { document = "last 4" }: how many of the last characters of a column leave the source.
func readMask(value any) (map[string]int, error) {
	texts, err := textTable(value)
	if err != nil {
		return nil, fmt.Errorf("must be a table such as { document = \"last 4\" }")
	}
	out := make(map[string]int, len(texts))
	for column, text := range texts {
		m := eltLast.FindStringSubmatch(text)
		if m == nil {
			return nil, fmt.Errorf("has `%s` for `%s`, and the only mask is \"last N\", with N from 1 to 99, for example \"last 4\"", text, column)
		}
		n, _ := strconv.Atoi(m[1])
		out[column] = n
	}
	return out, nil
}

func readRules(value any) ([]ELTRule, error) {
	var items []any
	switch v := value.(type) {
	case []any:
		items = v
	case []map[string]any:
		for _, item := range v {
			items = append(items, item)
		}
	default:
		return nil, fmt.Errorf("must be a list of rules, each with `when` and `code`, for example [{ when = \"total < 0\", code = \"TOTAL_NEGATIVE\" }]")
	}
	rules := make([]ELTRule, 0, len(items))
	for _, item := range items {
		table, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("must be a list of rules, each with `when` and `code`")
		}
		var rule ELTRule
		for _, key := range sortedKeys(table) {
			text, err := sqlText(table[key])
			if err != nil {
				return nil, fmt.Errorf("has a rule whose `%s` %s", key, err.Error())
			}
			switch key {
			case "when":
				rule.When = text
			case "code":
				rule.Code = text
			default:
				return nil, fmt.Errorf("has a rule with the setting `%s`, and a rule has `when` and `code`", key)
			}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}
