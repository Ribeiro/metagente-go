package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// A description of the copy of the table of samples/async-elt, for a destination in the dialect of driver.
func eltSettings(driver string) string {
	return `
[sql.source]
driver = "sqlite"
path = "source.db"

[sql.outbox]
driver = "sqlite"
path = "outbox.db"
mode = "write"

[sql.dest]
` + destinationPlace(driver) + `
mode = "write"

[broker.main]
driver = "memory"

[elt.orders]
key = "id"
columns = ["id", "customer", "document", "total", "note"]

[elt.orders.source]
connection = "source"
table = "orders"
outbox = "outbox"
broker = "main"
mask = { document = "last 4" }

[elt.orders.destination]
connection = "dest"
table = "orders_final"
upsert_on = "id"
set = { id = "id", customer = "trim(customer)", total_cents = "CAST(round(total * 100) AS INTEGER)", loaded_job = "job_id" }
reject = [
  { when = "total < 0", code = "TOTAL_NEGATIVE" },
  { when = "trim(customer) = ''", code = "CUSTOMER_EMPTY" },
]
`
}

func destinationPlace(driver string) string {
	if driver == "sqlite" {
		return `driver = "sqlite"` + "\n" + `path = "warehouse.db"`
	}
	return `driver = "` + driver + `"` + "\n" + `host = "db.example.com"` + "\n" + `database = "warehouse"` + "\n" + `user = "worker"`
}

func loadELT(t *testing.T, text string) (*Config, error) {
	t.Helper()
	cfg := Default()
	return cfg, cfg.apply("metagente.toml", text)
}

func TestADescriptionMakesTheStatementsOfTheSourceTheOutboxAndTheDestination(t *testing.T) {
	cfg, err := loadELT(t, eltSettings("sqlite"))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if got := strings.Join(cfg.SQL["source"].Names(), " "); got != "page range" {
		t.Errorf("the source has %s", got)
	}
	page := cfg.SQL["source"].Statements["page"].Parsed.Text
	if want := "SELECT id, customer, '***' || substr(document, -4) AS document, total, note FROM orders WHERE id > :after ORDER BY id LIMIT :size"; page != want {
		t.Errorf("page is\n%s\nwant\n%s", page, want)
	}
	if got := strings.Join(cfg.SQL["outbox"].Names(), " "); got != "confirm edges half int last_upto next_planned next_seq plan totals version" {
		t.Errorf("the outbox has %s", got)
	}
	dest := cfg.SQL["dest"]
	for _, name := range []string{"land_batch", "transform_batch", "register_totals", "resume", "purge_control"} {
		if dest.Transactions[name] == nil {
			t.Errorf("no transaction %s", name)
		}
	}
	if got := strings.Join(dest.Transactions["transform_batch"].Steps, " "); got != "reject_1 reject_2 load finish_batch" {
		t.Errorf("transform_batch has the steps %s", got)
	}
	load := dest.Statements["load"].Parsed.Text
	for _, want := range []string{
		"INSERT INTO orders_final (customer, id, loaded_job, total_cents) SELECT trim(customer), id, job_id, CAST(round(total * 100) AS INTEGER) FROM stg_orders",
		"CASE WHEN (total < 0) OR (trim(customer) = '') THEN 1 ELSE 0 END = 0",
		"ON CONFLICT (id) DO UPDATE SET customer = excluded.customer, loaded_job = excluded.loaded_job, total_cents = excluded.total_cents",
	} {
		if !strings.Contains(load, want) {
			t.Errorf("load lacks %q:\n%s", want, load)
		}
	}
	if land := dest.Statements["land"]; land.Each != "rows" || strings.Join(land.Columns, " ") != "id customer document total note" {
		t.Errorf("land = %+v", land)
	}
}

// The statements of the source are the ones that samples/async-elt/sources/ has, which are tried against the
// real database of each kind: a description has to make the same text.
func TestADescriptionMakesTheSourceStatementsOfTheSampleOfEveryDatabase(t *testing.T) {
	casts := map[string]string{
		"mysql": "CAST(total AS DOUBLE)", "mariadb": "CAST(total AS DOUBLE)", "oracle": "CAST(total AS BINARY_DOUBLE)",
		"postgres": "CAST(total AS double precision)", "sqlserver": "CAST(total AS float)",
	}
	for driver, cast := range casts {
		var sample struct {
			SQL struct {
				Source struct {
					Statements map[string]string `toml:"statements"`
				} `toml:"source"`
			} `toml:"sql"`
		}
		raw, err := os.ReadFile(filepath.Join("..", "..", "samples", "async-elt", "sources", "source."+driver+".toml"))
		if err != nil {
			t.Fatal(err)
		}
		if err := toml.Unmarshal(raw, &sample); err != nil {
			t.Fatalf("%s: %v", driver, err)
		}
		settings := `
[sql.source]
driver = "` + driver + `"
host = "db.example.com"
database = "orders"
user = "extractor"

[sql.outbox]
driver = "sqlite"
path = "outbox.db"
mode = "write"

[broker.main]
driver = "memory"

[elt.orders]
columns = ["id", "customer", "document", "total", "note"]

[elt.orders.source]
connection = "source"
table = "orders"
outbox = "outbox"
broker = "main"
mask = { document = "last 4" }
select = { total = "` + cast + `" }
`
		cfg, err := loadELT(t, settings)
		if err != nil {
			t.Fatalf("%s: %s", driver, problemText(t, err))
		}
		for _, name := range []string{"page", "range"} {
			if got, want := cfg.SQL["source"].Statements[name].Parsed.Text, sample.SQL.Source.Statements[name]; got != want {
				t.Errorf("%s, %s:\n got %s\nwant %s", driver, name, got, want)
			}
		}
	}
}

// The Worker calls the statements of the destination by name, so a description has to make the same names
// with the same values and the same shape of answer as the ones written by hand.
func TestTheStatementsOfADescriptionHaveTheShapeOfTheOnesOfTheSampleDestination(t *testing.T) {
	for _, driver := range []string{"postgres"} {
		want := loadSample(t, "metagente.postgres.toml")
		// The model step is not made from a description: a job that needs it is written by hand.
		delete(want.Transactions, "open_budget")
		delete(want.Transactions, "save_enrichment")
		cfg, err := loadELT(t, eltSettings(driver))
		if err != nil {
			t.Fatal(problemText(t, err))
		}
		got := cfg.SQL["dest"]
		sameStatements(t, driver, want, got)
		sameTransactions(t, driver, want, got)
		for _, name := range []string{"version", "batch_state", "batch_counts", "job_state", "job_status", "open_job", "land", "mark_landed",
			"reject_share", "finish_batch", "fail_batch", "pause_if_failing", "resume_job", "set_totals", "try_close", "record_incident", "purge"} {
			if got.Statements[name] == nil || want.Statements[name] == nil {
				t.Errorf("%s: %s is missing (%v, %v)", driver, name, got.Statements[name] != nil, want.Statements[name] != nil)
			}
		}
	}
}

func TestAnUpsertThatSetsOnlyItsKeyDoesNothingOnAConflict(t *testing.T) {
	text := strings.Replace(eltSettings("postgres"), `set = { id = "id", customer = "trim(customer)", total_cents = "CAST(round(total * 100) AS INTEGER)", loaded_job = "job_id" }`, `set = { id = "id" }`, 1)
	cfg, err := loadELT(t, text)
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if load := cfg.SQL["dest"].Statements["load"].Parsed.Text; !strings.Contains(load, "ON CONFLICT (id) DO NOTHING") {
		t.Errorf("load is %s", load)
	}
}

func TestADescriptionWithNoRulesRejectsNothing(t *testing.T) {
	text := eltSettings("sqlite")
	text = text[:strings.Index(text, "reject = [")]
	cfg, err := loadELT(t, text)
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	dest := cfg.SQL["dest"]
	if got := strings.Join(dest.Transactions["transform_batch"].Steps, " "); got != "load finish_batch" {
		t.Errorf("transform_batch has the steps %s", got)
	}
	if share := dest.Statements["reject_share"].Parsed.Text; !strings.Contains(share, "CASE WHEN 1 = 0 THEN 1 ELSE 0 END") {
		t.Errorf("reject_share is %s", share)
	}
}

func TestAMachineMayHaveOnlyItsOwnPartOfTheDescription(t *testing.T) {
	text := eltSettings("sqlite")
	worker := text[:strings.Index(text, "[elt.orders.source]")] + text[strings.Index(text, "[elt.orders.destination]"):]
	worker = strings.Replace(worker, "[sql.source]\ndriver = \"sqlite\"\npath = \"source.db\"\n", "", 1)
	worker = strings.Replace(worker, "[sql.outbox]\ndriver = \"sqlite\"\npath = \"outbox.db\"\nmode = \"write\"\n", "", 1)
	if _, err := loadELT(t, worker); err != nil {
		t.Errorf("the Worker's file: %s", problemText(t, err))
	}
	extractor := text[:strings.Index(text, "[elt.orders.destination]")]
	extractor = strings.Replace(extractor, "[sql.dest]\ndriver = \"sqlite\"\npath = \"warehouse.db\"\nmode = \"write\"\n", "", 1)
	if _, err := loadELT(t, extractor); err != nil {
		t.Errorf("the Extractor's file: %s", problemText(t, err))
	}
}

func TestAProblemInADescriptionIsToldWithTheLineOfItsSection(t *testing.T) {
	base := eltSettings("sqlite")
	replace := func(old, with string) string {
		if !strings.Contains(base, old) {
			t.Fatalf("the base has no %q", old)
		}
		return strings.Replace(base, old, with, 1)
	}
	for name, c := range map[string]struct{ text, want string }{
		"no columns":                  {replace(`columns = ["id", "customer", "document", "total", "note"]`, ""), "needs `columns`"},
		"key not a column":            {replace(`key = "id"`, `key = "order_no"`), "`order_no`, and it is not one of the `columns`"},
		"reserved column":             {replace(`"note"]`, `"seq"]`), "a column called `seq`"},
		"column twice":                {replace(`"total", "note"]`, `"total", "total"]`), "the column `total` twice"},
		"unknown setting":             {replace(`key = "id"`, "key = \"id\"\nkeys = 1"), "I do not know the setting `keys`"},
		"unknown source part":         {replace(`table = "orders"`, "table = \"orders\"\nlimit = 3"), "I do not know the setting `limit` in source"},
		"no outbox":                   {replace("outbox = \"outbox\"\n", ""), "needs `connection`"},
		"mask of a stranger":          {replace(`mask = { document = "last 4" }`, `mask = { secret = "last 4" }`), "masks `secret`"},
		"mask of another kind":        {replace(`"last 4"`, `"first 4"`), "the only mask is \"last N\""},
		"rule without code":           {replace(`code = "CUSTOMER_EMPTY"`, `code = "empty"`), "the code `empty`"},
		"rule code twice":             {replace(`code = "CUSTOMER_EMPTY"`, `code = "TOTAL_NEGATIVE"`), "the code `TOTAL_NEGATIVE` twice"},
		"no set":                      {replace(`set = { id = "id", customer = "trim(customer)", total_cents = "CAST(round(total * 100) AS INTEGER)", loaded_job = "job_id" }`, ""), "needs `set`"},
		"upsert outside set":          {replace(`upsert_on = "id"`, `upsert_on = "number"`), "`set` does not give that column"},
		"share too big":               {replace(`upsert_on = "id"`, "upsert_on = \"id\"\nreject_share = 150"), "a percent from above 0 to 100"},
		"connection missing":          {replace(`connection = "dest"`, `connection = "warehouse"`) + "\n[sql.dest.statements]\nsomething = \"SELECT 1\"\n", "has no section [sql.warehouse]"},
		"broker missing":              {replace(`broker = "main"`, `broker = "bus"`), "has no section [broker.bus]"},
		"outbox not sqlite":           {replace("[sql.outbox]\ndriver = \"sqlite\"\npath = \"outbox.db\"", "[sql.outbox]\ndriver = \"postgres\"\nhost = \"h\"\ndatabase = \"d\"\nuser = \"u\""), "has to be a SQLite file"},
		"source written to":           {replace("[sql.source]\ndriver = \"sqlite\"\npath = \"source.db\"", "[sql.source]\ndriver = \"sqlite\"\npath = \"source.db\"\nmode = \"write\""), "is written to"},
		"destination reads":           {replace("[sql.dest]\ndriver = \"sqlite\"\npath = \"warehouse.db\"\nmode = \"write\"", "[sql.dest]\ndriver = \"sqlite\"\npath = \"warehouse.db\""), "has to have mode = \"write\""},
		"a rule the database refuses": {replace(`when = "total < 0"`, `when = "total < 0; DROP TABLE orders"`), "does not accept"},
		"a name clash":                {replace("[sql.source]\ndriver = \"sqlite\"\npath = \"source.db\"", "[sql.source]\ndriver = \"sqlite\"\npath = \"source.db\"\n[sql.source.statements]\npage = \"SELECT id FROM orders\""), "the description makes one with that name"},
	} {
		_, err := loadELT(t, c.text)
		if err == nil {
			t.Errorf("%s: no problem was found", name)
			continue
		}
		if got := problemText(t, err); !strings.Contains(got, c.want) {
			t.Errorf("%s: the problem is\n%s\nwant it to say %q", name, got, c.want)
		}
	}
}

func TestADescriptionIsWrittenOnce(t *testing.T) {
	// The library of TOML refuses a table that is written twice; a description that is written in two ways is the same.
	text := eltSettings("sqlite") + "\n[elt.orders]\nkey = \"id\"\n"
	if _, err := loadELT(t, text); err == nil {
		t.Error("two sections [elt.orders] were accepted")
	}
}

func TestTheSettingsOfADescriptionMustBeOfTheKindThatTheyAre(t *testing.T) {
	base := eltSettings("sqlite")
	replace := func(old, with string) string {
		if !strings.Contains(base, old) {
			t.Fatalf("the base has no %q", old)
		}
		return strings.Replace(base, old, with, 1)
	}
	for name, c := range map[string]struct{ text, want string }{
		"name":                {strings.Replace(base, "[elt.orders", "[elt.order-s", 3), "is not a name for a description"},
		"not a section":       {"[elt]\norders = 3\n", "must be a section of its own"},
		"key":                 {replace(`key = "id"`, `key = "order no"`), "a column is written with letters"},
		"key not text":        {replace(`key = "id"`, `key = 3`), "a text in quotes"},
		"columns not a list":  {replace(`columns = ["id", "customer", "document", "total", "note"]`, `columns = "id"`), "a list of texts"},
		"source not table":    {"[elt.orders]\ncolumns = [\"id\"]\nsource = 3\n", "must be a section"},
		"rows":                {replace(`table = "orders"`, "table = \"orders\"\nrows = 0"), "a whole number of at least 1"},
		"bytes":               {replace(`table = "orders"`, "table = \"orders\"\nbytes = \"big\""), "a whole number of at least 1"},
		"source table":        {replace(`table = "orders"`, `table = "orders; x"`), "needs `table`"},
		"select":              {replace(`mask =`, "select = { stranger = \"1\" }\nmask ="), "selects `stranger`"},
		"select not text":     {replace(`mask =`, "select = { total = 3 }\nmask ="), "must hold texts"},
		"mask not table":      {replace(`mask = { document = "last 4" }`, `mask = "last 4"`), "must be a table"},
		"mask not text":       {replace(`mask = { document = "last 4" }`, `mask = { document = 4 }`), "must be a table"},
		"destination setting": {replace(`upsert_on = "id"`, "upsert_on = \"id\"\nspeed = 3"), "I do not know the setting `speed` in destination"},
		"destination table":   {replace(`table = "orders_final"`, `table = "orders final"`), "needs `table`"},
		"staging":             {replace(`table = "orders_final"`, "table = \"orders_final\"\nstaging = \"a b\""), "not a table name"},
		"set not table":       {replace(`set = {`, `set = 3 #`), "must be a table of texts"},
		"set empty":           {replace(`loaded_job = "job_id" }`, `loaded_job = " " }`), "sets `loaded_job` to nothing"},
		"set column":          {replace(`set = { id = "id",`, `set = { id = "id", "bad column" = "1",`), "a column is written with letters"},
		"rule not a table":    {replace(`reject = [`, "reject = [3,\n"), "each with `when` and `code`"},
		"rule setting":        {replace(`code = "CUSTOMER_EMPTY"`, `code = "CUSTOMER_EMPTY", why = "x"`), "a rule has `when` and `code`"},
		"rule not text":       {replace(`code = "CUSTOMER_EMPTY"`, `code = 3`), "has a rule whose `code`"},
		"rule no when":        {replace(`{ when = "trim(customer) = ''", code = "CUSTOMER_EMPTY" }`, `{ code = "CUSTOMER_EMPTY" }`), "with no `when`"},
		"share not a number":  {replace(`upsert_on = "id"`, "upsert_on = \"id\"\nreject_share = \"a\""), "must be a number"},
		"pause after":         {replace(`upsert_on = "id"`, "upsert_on = \"id\"\npause_after = 0"), "a whole number of at least 1"},
	} {
		_, err := loadELT(t, c.text)
		if err == nil {
			t.Errorf("%s: no problem was found", name)
			continue
		}
		if got := problemText(t, err); !strings.Contains(got, c.want) {
			t.Errorf("%s: the problem is\n%s\nwant it to say %q", name, got, c.want)
		}
	}
}

func TestARuleThatIsNotAListOfTablesIsRefused(t *testing.T) {
	text := strings.Replace(eltSettings("sqlite"), "reject = [\n  { when = \"total < 0\", code = \"TOTAL_NEGATIVE\" },\n  { when = \"trim(customer) = ''\", code = \"CUSTOMER_EMPTY\" },\n]\n", "reject = \"total < 0\"\n", 1)
	_, err := loadELT(t, text)
	if err == nil || !strings.Contains(problemText(t, err), "must be a list of rules") {
		t.Errorf("err = %v", err)
	}
	// The same rules written as tables of an array are the same rules.
	tables := strings.Replace(eltSettings("sqlite"), "reject = [\n  { when = \"total < 0\", code = \"TOTAL_NEGATIVE\" },\n  { when = \"trim(customer) = ''\", code = \"CUSTOMER_EMPTY\" },\n]\n", "", 1) +
		"\n[[elt.orders.destination.reject]]\nwhen = \"total < 0\"\ncode = \"TOTAL_NEGATIVE\"\n"
	cfg, err := loadELT(t, tables)
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if rules := cfg.ELT["orders"].Destination.Reject; len(rules) != 1 || rules[0].Code != "TOTAL_NEGATIVE" {
		t.Errorf("rules = %+v", rules)
	}
}

func TestAKeyThatIsReadUnderAnotherNameIsFoundByItsExpression(t *testing.T) {
	text := strings.Replace(eltSettings("sqlite"), `mask = { document = "last 4" }`, `select = { id = "order_no" }`, 1)
	cfg, err := loadELT(t, text)
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	want := "SELECT order_no AS id, customer, document, total, note FROM orders WHERE order_no > :after ORDER BY order_no LIMIT :size"
	if got := cfg.SQL["source"].Statements["page"].Parsed.Text; got != want {
		t.Errorf("page is\n%s\nwant\n%s", got, want)
	}
}

// The statements that do not depend on the columns of the table are the ones that samples/async-elt tried against real
// servers: a description has to make the same text for SQL Server and Oracle, and the others have the same shape.
func TestTheStatementsOfADescriptionForSQLServerAndOracleAreTheOnesOfTheSample(t *testing.T) {
	same := []string{"version", "batch_state", "batch_counts", "job_state", "job_status", "open_job", "mark_landed", "finish_batch",
		"fail_batch", "pause_if_failing", "resume_job", "forget_alerts", "set_totals", "try_close", "record_incident", "purge",
		"old_alerts", "old_incidents", "old_resends", "old_rejects", "old_batches", "old_jobs"}
	for _, driver := range []string{"sqlserver", "oracle"} {
		want := loadSample(t, "metagente."+driver+".toml")
		delete(want.Transactions, "open_budget")
		delete(want.Transactions, "save_enrichment")
		cfg, err := loadELT(t, eltSettings(driver))
		if err != nil {
			t.Fatalf("%s: %s", driver, problemText(t, err))
		}
		got := cfg.SQL["dest"]
		for _, name := range same {
			if a, b := want.Statements[name], got.Statements[name]; a == nil || b == nil {
				t.Errorf("%s: %s is missing (%v, %v)", driver, name, b != nil, a != nil)
			} else if strings.ReplaceAll(a.Parsed.Text, "last_three", "last_batches") != b.Parsed.Text || a.Result != b.Result {
				t.Errorf("%s: %s is\n%s\nthe sample has\n%s", driver, name, b.Parsed.Text, a.Parsed.Text)
			}
		}
		sameStatements(t, driver, want, got)
		sameTransactions(t, driver, want, got)
		// An upsert is an update and an insert of what is not there.
		if got.Statements["update_final"] == nil || strings.Join(got.Transactions["transform_batch"].Steps, " ") != "reject_1 reject_2 update_final load finish_batch" {
			t.Errorf("%s: transform_batch has the steps %v", driver, got.Transactions["transform_batch"].Steps)
		}
	}
}

func TestTheUpdateOfAnUpsertReadsTheRowsAgainstStagingAlone(t *testing.T) {
	sqlserver, err := loadELT(t, eltSettings("sqlserver"))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	want := "UPDATE f SET customer = s.customer, loaded_job = s.loaded_job, total_cents = s.total_cents FROM orders_final f JOIN " +
		"(SELECT trim(customer) AS customer, id AS id, job_id AS loaded_job, CAST(round(total * 100) AS INTEGER) AS total_cents FROM stg_orders " +
		"WHERE job_id = :job AND seq = :seq AND CASE WHEN (total < 0) OR (trim(customer) = '') THEN 1 ELSE 0 END = 0) s ON s.id = f.id"
	if got := sqlserver.SQL["dest"].Statements["update_final"].Parsed.Text; got != want {
		t.Errorf("SQL Server: update_final is\n%s\nwant\n%s", got, want)
	}
	oracle, err := loadELT(t, eltSettings("oracle"))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	text := oracle.SQL["dest"].Statements["update_final"].Parsed.Text
	for _, want := range []string{"UPDATE orders_final f SET (customer, loaded_job, total_cents) = (SELECT s.customer, s.loaded_job, s.total_cents FROM (SELECT", "WHERE s.id = f.id) WHERE f.id IN (SELECT s.id FROM (SELECT"} {
		if !strings.Contains(text, want) {
			t.Errorf("Oracle: update_final lacks %q:\n%s", want, text)
		}
	}
	load := oracle.SQL["dest"].Statements["load"].Parsed.Text
	if !strings.HasSuffix(load, "WHERE NOT EXISTS (SELECT 1 FROM orders_final f WHERE f.id = s.id)") {
		t.Errorf("Oracle: load is %s", load)
	}
}

func TestAnUpsertThatSetsOnlyItsKeyHasNothingToUpdateInSQLServerAndOracle(t *testing.T) {
	for _, driver := range []string{"sqlserver", "oracle"} {
		text := strings.Replace(eltSettings(driver), `set = { id = "id", customer = "trim(customer)", total_cents = "CAST(round(total * 100) AS INTEGER)", loaded_job = "job_id" }`, `set = { id = "id" }`, 1)
		cfg, err := loadELT(t, text)
		if err != nil {
			t.Fatal(problemText(t, err))
		}
		dest := cfg.SQL["dest"]
		if dest.Statements["update_final"] != nil || strings.Join(dest.Transactions["transform_batch"].Steps, " ") != "reject_1 reject_2 load finish_batch" {
			t.Errorf("%s: transform_batch has the steps %v", driver, dest.Transactions["transform_batch"].Steps)
		}
	}
}

// MySQL and MariaDB have neither ON CONFLICT nor MERGE: they have INSERT ... ON DUPLICATE KEY UPDATE, which does both. The
// description makes the same statements, with the same names and values, as for the others.
func TestTheStatementsOfADescriptionForMySQLAndMariaDBUseOnDuplicateKeyUpdate(t *testing.T) {
	for _, driver := range []string{"mysql", "mariadb"} {
		cfg, err := loadELT(t, eltSettings(driver))
		if err != nil {
			t.Fatalf("%s: %s", driver, problemText(t, err))
		}
		dest := cfg.SQL["dest"]
		want := map[string]string{
			"open_job":        "INSERT INTO etl_jobs (job_id) VALUES (:job) ON DUPLICATE KEY UPDATE job_id = job_id",
			"mark_landed":     "INSERT INTO etl_batches (job_id, seq, state, rows_read) VALUES (:job, :seq, 'landed', :row_count) ON DUPLICATE KEY UPDATE job_id = job_id",
			"land":            "INSERT INTO stg_orders (job_id, seq, id, customer, document, total, note) VALUES (:job, :seq, :id, :customer, :document, :total, :note) ON DUPLICATE KEY UPDATE job_id = job_id",
			"record_incident": "INSERT INTO etl_incidents (job_id, seq, code) VALUES (:job, :seq, :code) ON DUPLICATE KEY UPDATE job_id = job_id",
			"reject_1": "INSERT INTO etl_rejects (job_id, seq, source_key, reason_code) SELECT job_id, seq, id, 'TOTAL_NEGATIVE' FROM stg_orders WHERE job_id = :job AND seq = :seq AND (total < 0) " +
				"ON DUPLICATE KEY UPDATE etl_rejects.job_id = etl_rejects.job_id",
			"load": "INSERT INTO orders_final (customer, id, loaded_job, total_cents) SELECT trim(customer), id, job_id, CAST(round(total * 100) AS INTEGER) FROM stg_orders " +
				"WHERE job_id = :job AND seq = :seq AND CASE WHEN (total < 0) OR (trim(customer) = '') THEN 1 ELSE 0 END = 0 " +
				"ON DUPLICATE KEY UPDATE orders_final.customer = VALUES(customer), orders_final.loaded_job = VALUES(loaded_job), orders_final.total_cents = VALUES(total_cents)",
			"purge": "DELETE FROM stg_orders WHERE EXISTS (SELECT 1 FROM etl_batches b WHERE b.job_id = stg_orders.job_id AND b.seq = stg_orders.seq AND b.state = 'done' AND b.done_at < DATE_SUB(NOW(6), INTERVAL CAST(:days AS SIGNED) DAY))",
			"old_jobs": "DELETE FROM etl_jobs WHERE job_id IN (SELECT done.job_id FROM (SELECT j.job_id FROM etl_jobs j WHERE j.state = 'done' AND j.finished_at < DATE_SUB(NOW(6), INTERVAL CAST(:days AS SIGNED) DAY) " +
				"AND NOT EXISTS (SELECT 1 FROM stg_orders s WHERE s.job_id = j.job_id)) done)",
			"finish_batch": "UPDATE etl_batches SET state = 'done', rows_rejected = (SELECT count(DISTINCT source_key) FROM etl_rejects WHERE job_id = :job AND seq = :seq), " +
				"rows_loaded = rows_read - (SELECT count(DISTINCT source_key) FROM etl_rejects WHERE job_id = :job AND seq = :seq), done_at = NOW(6), transform_version = :version, last_error_code = NULL WHERE job_id = :job AND seq = :seq",
		}
		for name, text := range want {
			if got := dest.Statements[name].Parsed.Text; got != text {
				t.Errorf("%s: %s is\n%s\nwant\n%s", driver, name, got, text)
			}
		}
		if share := dest.Statements["reject_share"].Parsed.Text; !strings.HasPrefix(share, "SELECT CAST(coalesce(100.0 * sum(CASE WHEN") || !strings.Contains(share, "/ NULLIF(count(*), 0), 0) AS DOUBLE) FROM stg_orders") {
			t.Errorf("%s: reject_share is %s", driver, share)
		}
		if got := strings.Join(dest.Transactions["transform_batch"].Steps, " "); got != "reject_1 reject_2 load finish_batch" {
			t.Errorf("%s: transform_batch has the steps %s", driver, got)
		}
	}
}

func TestTheStatementsOfADescriptionForMySQLHaveTheShapeOfTheOnesOfTheSampleDestination(t *testing.T) {
	for _, driver := range []string{"mysql", "mariadb"} {
		want := loadSample(t, "metagente.postgres.toml")
		delete(want.Transactions, "open_budget")
		delete(want.Transactions, "save_enrichment")
		cfg, err := loadELT(t, eltSettings(driver))
		if err != nil {
			t.Fatal(problemText(t, err))
		}
		sameStatements(t, driver, want, cfg.SQL["dest"])
		sameTransactions(t, driver, want, cfg.SQL["dest"])
	}
}

func TestAnUpsertThatSetsOnlyItsKeyChangesNothingInMySQL(t *testing.T) {
	text := strings.Replace(eltSettings("mariadb"), `set = { id = "id", customer = "trim(customer)", total_cents = "CAST(round(total * 100) AS INTEGER)", loaded_job = "job_id" }`, `set = { id = "id" }`, 1)
	cfg, err := loadELT(t, text)
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if load := cfg.SQL["dest"].Statements["load"].Parsed.Text; !strings.HasSuffix(load, "ON DUPLICATE KEY UPDATE orders_final.id = orders_final.id") {
		t.Errorf("load is %s", load)
	}
}
