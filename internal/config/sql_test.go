package config

import (
	"strings"
	"testing"
)

const goodSQL = `
[sql.orders-db]
driver = "sqlite"
path = "data/orders.db"

[sql.orders-db.statements]
next_page = "SELECT id, customer FROM orders WHERE id > :after ORDER BY id LIMIT :size"
last_id = { sql = """
  SELECT max(id) AS last
    FROM orders
   WHERE id > :after""", result = "value", description = "The last id after one" }
one = { sql = "SELECT id FROM orders WHERE id = :id", result = "row" }

[sql.reports]
driver = "sqlite"
path = "reports.db"
statements = { totals = "SELECT region, sum(total) AS total FROM sales GROUP BY region" }
`

func TestTheConnectionsToDatabasesAreRead(t *testing.T) {
	cfg := Default()
	if err := cfg.apply("metagente.toml", goodSQL); err != nil {
		t.Fatal(problemText(t, err))
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("warnings: %v", cfg.Warnings)
	}
	orders := cfg.SQL["orders-db"]
	if orders == nil || orders.Driver != "sqlite" || orders.Path != "data/orders.db" || strings.Join(orders.Names(), " ") != "last_id next_page one" {
		t.Fatalf("orders-db = %+v", orders)
	}
	page := orders.Statements["next_page"]
	if page.Result != ResultRows || strings.Join(page.Parsed.Params, " ") != "after size" {
		t.Errorf("next_page = %+v", page)
	}
	last := orders.Statements["last_id"]
	if last.Result != ResultValue || last.Description != "The last id after one" || !strings.Contains(last.Parsed.Text, "FROM orders") {
		t.Errorf("last_id = %+v", last)
	}
	if orders.Statements["one"].Result != ResultRow {
		t.Errorf("one = %+v", orders.Statements["one"])
	}
	if reports := cfg.SQL["reports"]; reports == nil || len(reports.Statements) != 1 {
		t.Errorf("reports = %+v", reports)
	}
}

func TestAProblemInAConnectionIsToldWithTheLineOfItsSection(t *testing.T) {
	section := func(body string) string {
		return "[runtime]\ntimeout_seconds = 30\n\n[sql.orders]\n" + body
	}
	for name, c := range map[string]struct{ text, want string }{
		"no driver":        {section("path = \"a.db\"\n[sql.orders.statements]\na = \"select 1\"\n"), "[sql.orders] needs a driver"},
		"another driver":   {section("driver = \"postgres\"\n[sql.orders.statements]\na = \"select 1\"\n"), "names the driver `postgres`, which this version does not have"},
		"no statements":    {section("driver = \"sqlite\"\n"), "[sql.orders] has no statements"},
		"unknown setting":  {section("driver = \"sqlite\"\nreadonly = false\n[sql.orders.statements]\na = \"select 1\"\n"), "I do not know the setting `readonly` in [sql.orders]"},
		"driver not text":  {section("driver = 3\n[sql.orders.statements]\na = \"select 1\"\n"), "`driver` in [sql.orders] must be a text in quotes"},
		"path not text":    {section("driver = \"sqlite\"\npath = true\n[sql.orders.statements]\na = \"select 1\"\n"), "`path` in [sql.orders] must be a text in quotes"},
		"statements text":  {section("driver = \"sqlite\"\nstatements = \"select 1\"\n"), "must be a section of statements"},
		"a bad name":       {section("driver = \"sqlite\"\n[sql.orders.statements]\n\"two words\" = \"select 1\"\n"), "a statement called `two words`"},
		"a name with dots": {section("driver = \"sqlite\"\n[sql.orders.statements]\n\"a.b\" = \"select 1\"\n"), "a statement called `a.b`"},
		"a number":         {section("driver = \"sqlite\"\n[sql.orders.statements]\na = 5\n"), "problem in the statement `a`: must be the text of the statement"},
		"deleting":         {section("driver = \"sqlite\"\n[sql.orders.statements]\nwipe = \"DELETE FROM orders\"\n"), "problem in the statement `wipe`: it begins with DELETE"},
		"two statements":   {section("driver = \"sqlite\"\n[sql.orders.statements]\na = \"select 1; select 2\"\n"), "problem in the statement `a`: it has more than one statement"},
		"a bad result":     {section("driver = \"sqlite\"\n[sql.orders.statements]\na = { sql = \"select 1\", result = \"all\" }\n"), "`result` is `all`, and it has to be rows, row or value"},
		"no sql":           {section("driver = \"sqlite\"\n[sql.orders.statements]\na = { result = \"row\" }\n"), "it is empty"},
		"unknown key":      {section("driver = \"sqlite\"\n[sql.orders.statements]\na = { sql = \"select 1\", write = true }\n"), "I do not know the setting `write`"},
		"sql not text":     {section("driver = \"sqlite\"\n[sql.orders.statements]\na = { sql = 1 }\n"), "`sql` must be a text in quotes"},
		"a bad section":    {"[sql]\ndriver = \"sqlite\"\n", "`driver` in [sql] must be a section of its own"},
		"a bad name of it": {"[sql.\"two words\"]\ndriver = \"sqlite\"\n", "`two words` is not a name for a connection"},
	} {
		err := Default().apply("metagente.toml", c.text)
		shown := problemText(t, err)
		if !strings.Contains(shown, c.want) {
			t.Errorf("%s: missing %q in:\n%s", name, c.want, shown)
		}
		wantLine := "line 4"
		switch name {
		case "a bad section":
			wantLine = "line 2" // the key that is not a section
		case "a bad name of it":
			wantLine = "line 1"
		}
		if !strings.Contains(shown, wantLine) || !strings.Contains(shown, "Fix:") {
			t.Errorf("%s: no line (%s) or no fix in:\n%s", name, wantLine, shown)
		}
	}
}

func TestTheTextOfAStatementIsNeverShownInAnError(t *testing.T) {
	text := "[sql.orders]\ndriver = \"sqlite\"\n[sql.orders.statements]\nsecret_report = \"DELETE FROM salaries WHERE boss = 'Ann Rivers'\"\n"
	shown := problemText(t, Default().apply("metagente.toml", text))
	for _, leaked := range []string{"salaries", "Ann Rivers", "boss"} {
		if strings.Contains(shown, leaked) {
			t.Errorf("%q is in the message:\n%s", leaked, shown)
		}
	}
}

func TestTwoConnectionsAreTwoSections(t *testing.T) {
	text := "[sql.a]\ndriver = \"sqlite\"\nstatements = { x = \"select 1\" }\n\n[sql.b]\ndriver = \"sqlite\"\nstatements = { x = \"select 2\" }\n"
	cfg := Default()
	if err := cfg.apply("metagente.toml", text); err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.SQL["a"].Statements["x"].Parsed.Text != "select 1" || cfg.SQL["b"].Statements["x"].Parsed.Text != "select 2" {
		t.Errorf("the sections were mixed: %+v %+v", cfg.SQL["a"], cfg.SQL["b"])
	}
}

func TestTheCeilingsOfADatabaseAnswerAreSettings(t *testing.T) {
	limits := Default().Limits
	if limits.MaxSQLRows != 10000 || limits.MaxSQLBytes != 5<<20 {
		t.Errorf("defaults = %d, %d", limits.MaxSQLRows, limits.MaxSQLBytes)
	}
	cfg := Default()
	if err := cfg.apply("metagente.toml", "[limits]\nmax_sql_rows = 50\nmax_sql_bytes = 4096\n"); err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.Limits.MaxSQLRows != 50 || cfg.Limits.MaxSQLBytes != 4096 {
		t.Errorf("limits = %+v", cfg.Limits)
	}
	shown := problemText(t, Default().apply("metagente.toml", "[limits]\nmax_sql_rows = 0\n"))
	if !strings.Contains(shown, "`max_sql_rows` in [limits] must be a whole number of at least 1") {
		t.Errorf("shown:\n%s", shown)
	}
}
