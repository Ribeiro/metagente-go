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
		"another driver":   {section("driver = \"oracle\"\n[sql.orders.statements]\na = \"select 1\"\n"), "names the driver `oracle`, which this version does not have"},
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

const networkSQL = `
[sql.pg]
driver = "postgres"
host = "db.example.com"
database = "orders"
user = "reader"
statements = { one = "select 1" }

[sql.my]
driver = "mariadb"
host = "10.0.0.7"
port = 3307
database = "orders"
user = "reader"
tls = "require"
statements = { one = "select 1" }

[sql.verified]
driver = "mysql"
host = "db.example.com"
database = "orders"
user = "reader"
ca_file = "certs/ca.pem"
statements = { one = "select 1" }
`

func TestNetworkDatabasesHaveTheirPortAndTLSFilledIn(t *testing.T) {
	cfg := Default()
	if err := cfg.apply("metagente.toml", networkSQL); err != nil {
		t.Fatal(problemText(t, err))
	}
	pg, my, verified := cfg.SQL["pg"], cfg.SQL["my"], cfg.SQL["verified"]
	if pg.Port != 5432 || pg.TLS != TLSVerify || pg.User != "reader" || pg.Host != "db.example.com" {
		t.Errorf("pg = %+v", pg)
	}
	if my.Port != 3307 || my.TLS != TLSRequire {
		t.Errorf("my = %+v", my)
	}
	if verified.Port != 3306 || verified.TLS != TLSVerify || verified.CAFile != "certs/ca.pem" {
		t.Errorf("verified = %+v", verified)
	}
}

func TestAProblemInANetworkConnectionIsTold(t *testing.T) {
	const statements = "statements = { one = \"select 1\" }\n"
	section := func(body string) string { return "[runtime]\ntimeout_seconds = 30\n\n[sql.db]\n" + body + statements }
	for name, c := range map[string]struct{ text, want string }{
		"no host":          {section("driver = \"postgres\"\ndatabase = \"a\"\nuser = \"u\"\n"), "needs a host, a database and a user"},
		"no user":          {section("driver = \"mysql\"\nhost = \"h\"\ndatabase = \"a\"\n"), "needs a host, a database and a user"},
		"a path":           {section("driver = \"postgres\"\npath = \"x.db\"\nhost = \"h\"\ndatabase = \"a\"\nuser = \"u\"\n"), "has no path"},
		"a port in a host": {section("driver = \"postgres\"\nhost = \"h:5432\"\ndatabase = \"a\"\nuser = \"u\"\n"), "signs that do not belong"},
		"a host as a url":  {section("driver = \"postgres\"\nhost = \"a@h\"\ndatabase = \"a\"\nuser = \"u\"\n"), "signs that do not belong"},
		"a bad tls":        {section("driver = \"postgres\"\nhost = \"h\"\ndatabase = \"a\"\nuser = \"u\"\ntls = \"maybe\"\n"), "has to be verify, require or disable"},
		"ca without tls":   {section("driver = \"postgres\"\nhost = \"h\"\ndatabase = \"a\"\nuser = \"u\"\ntls = \"require\"\nca_file = \"a.pem\"\n"), "only means something with tls"},
		"a bad port":       {section("driver = \"postgres\"\nhost = \"h\"\nport = 70000\ndatabase = \"a\"\nuser = \"u\"\n"), "whole number from 1 to 65535"},
		"a port in text":   {section("driver = \"postgres\"\nhost = \"h\"\nport = \"5432\"\ndatabase = \"a\"\nuser = \"u\"\n"), "whole number from 1 to 65535"},
		"a password":       {section("driver = \"postgres\"\nhost = \"h\"\ndatabase = \"a\"\nuser = \"u\"\npassword = \"x\"\n"), "I do not know the setting `password`"},
		"sqlite with host": {section("driver = \"sqlite\"\npath = \"a.db\"\nhost = \"h\"\n"), "is a SQLite database"},
	} {
		err := Default().apply("metagente.toml", c.text)
		if err == nil {
			t.Errorf("%s: no problem", name)
			continue
		}
		if shown := problemText(t, err); !strings.Contains(shown, c.want) {
			t.Errorf("%s: missing %q in:\n%s", name, c.want, shown)
		}
	}
}

const brokerTOML = `
[credentials]
events = "BROKER_PASSWORD"

[broker.main]
driver = "jetstream"
url = "tls://broker.example.com:4222"
user = "etl"
stream = "ETL"
ca_file = "certs/ca.pem"

[broker.local]
driver = "jetstream"
url = "nats://127.0.0.1:4222"
tls = "disable"

[broker.memory]
driver = "memory"
`

func TestBrokersAreReadWithTheirTLSFilledIn(t *testing.T) {
	cfg := Default()
	if err := cfg.apply("metagente.toml", brokerTOML+"\n[limits]\nmax_broker_bytes = 4096\n"); err != nil {
		t.Fatal(problemText(t, err))
	}
	main, local, memory := cfg.Broker["main"], cfg.Broker["local"], cfg.Broker["memory"]
	if main.TLS != TLSVerify || main.User != "etl" || main.Stream != "ETL" || main.CAFile != "certs/ca.pem" || main.URL != "tls://broker.example.com:4222" {
		t.Errorf("main = %+v", main)
	}
	if local.TLS != TLSDisable || memory == nil || memory.Driver != "memory" || cfg.Limits.MaxBrokerBytes != 4096 {
		t.Errorf("local = %+v, memory = %+v, limit = %d", local, memory, cfg.Limits.MaxBrokerBytes)
	}
	if Default().Limits.MaxBrokerBytes != 1<<20 {
		t.Errorf("default limit = %d", Default().Limits.MaxBrokerBytes)
	}
}

func TestAProblemInABrokerIsTold(t *testing.T) {
	section := func(body string) string { return "[runtime]\ntimeout_seconds = 30\n\n[broker.main]\n" + body }
	for name, c := range map[string]struct{ text, want string }{
		"no driver":       {section("url = \"nats://h:4222\"\n"), "needs a driver"},
		"another driver":  {section("driver = \"kafka\"\nurl = \"nats://h:4222\"\n"), "names the driver `kafka`"},
		"no url":          {section("driver = \"jetstream\"\n"), "needs a url"},
		"not a url":       {section("driver = \"jetstream\"\nurl = \"broker\"\n"), "not an address of a NATS server"},
		"a http url":      {section("driver = \"jetstream\"\nurl = \"http://h:4222\"\n"), "not an address of a NATS server"},
		"a password":      {section("driver = \"jetstream\"\nurl = \"nats://ana:secret@h:4222\"\n"), "has a user or a password in it"},
		"a path":          {section("driver = \"jetstream\"\nurl = \"nats://h:4222/x\"\n"), "more than the address"},
		"a bad stream":    {section("driver = \"jetstream\"\nurl = \"nats://h:4222\"\nstream = \"a.b\"\n"), "not the name of a stream"},
		"a bad tls":       {section("driver = \"jetstream\"\nurl = \"nats://h:4222\"\ntls = \"maybe\"\n"), "has to be verify, require or disable"},
		"ca without tls":  {section("driver = \"jetstream\"\nurl = \"nats://h:4222\"\ntls = \"require\"\nca_file = \"a.pem\"\n"), "only means something"},
		"a password key":  {section("driver = \"jetstream\"\nurl = \"nats://h:4222\"\npassword = \"x\"\n"), "I do not know the setting `password`"},
		"memory with url": {section("driver = \"memory\"\nurl = \"nats://h:4222\"\n"), "is a broker in memory"},
		"not a section":   {"[broker]\ndriver = \"jetstream\"\n", "`driver` in [broker] must be a section of its own"},
		"a bad name":      {"[broker.\"two words\"]\ndriver = \"memory\"\n", "is not a name for a connection"},
		"url not text":    {section("driver = \"jetstream\"\nurl = 7\n"), "`url` in [broker.main] must be a text"},
	} {
		err := Default().apply("metagente.toml", c.text)
		if err == nil {
			t.Errorf("%s: no problem", name)
			continue
		}
		if shown := problemText(t, err); !strings.Contains(shown, c.want) {
			t.Errorf("%s: missing %q in:\n%s", name, c.want, shown)
		}
	}
}

const writingSQL = `
[sql.warehouse]
driver = "sqlite"
path = "warehouse.db"
mode = "write"

[sql.warehouse.statements]
mark = "INSERT INTO batches (job, seq, state) VALUES (:job, :seq, 'landed')"
land = { sql = "INSERT INTO stg (job, seq, id, name) VALUES (:job, :seq, :id, :name)", each = "rows", columns = ["id", "name"], description = "Put the rows in staging" }
finish = "UPDATE batches SET state = 'done' WHERE job = :job AND seq = :seq"
purge = "DELETE FROM stg WHERE job = :job"
total = { sql = "SELECT count(*) FROM stg WHERE job = :job", result = "value" }

[sql.warehouse.transactions]
land_batch = ["land", "mark"]
close = { steps = ["finish", "purge"], description = "Close a batch" }
`

func TestAConnectionThatWritesHasStatementsThatChangeRowsAndTransactions(t *testing.T) {
	cfg := Default()
	if err := cfg.apply("metagente.toml", writingSQL); err != nil {
		t.Fatal(problemText(t, err))
	}
	conn := cfg.SQL["warehouse"]
	if !conn.Writes() || conn.Mode != ModeWrite {
		t.Fatalf("mode = %q", conn.Mode)
	}
	if conn.Statements["mark"].Result != ResultCount || conn.Statements["total"].Result != ResultValue {
		t.Errorf("results = %s, %s", conn.Statements["mark"].Result, conn.Statements["total"].Result)
	}
	land := conn.Statements["land"]
	if land.Each != "rows" || strings.Join(land.Columns, " ") != "id name" || strings.Join(land.CallParams(), " ") != "job seq" {
		t.Errorf("land = %+v, call params %v", land, land.CallParams())
	}
	if strings.Join(conn.TransactionNames(), " ") != "close land_batch" {
		t.Errorf("transactions = %v", conn.TransactionNames())
	}
	scalars, lists, err := conn.TransactionParams(conn.Transactions["land_batch"])
	if err != nil || strings.Join(scalars, " ") != "job seq" || strings.Join(lists, " ") != "rows" {
		t.Errorf("params = %v %v %v", scalars, lists, err)
	}
	if conn.Transactions["close"].Description != "Close a batch" {
		t.Errorf("description = %q", conn.Transactions["close"].Description)
	}
}

func TestAConnectionReadsUnlessItSaysItWrites(t *testing.T) {
	cfg := Default()
	if err := cfg.apply("metagente.toml", goodSQL); err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.SQL["orders-db"].Writes() || cfg.SQL["orders-db"].Mode != ModeRead {
		t.Errorf("mode = %q", cfg.SQL["orders-db"].Mode)
	}
}

func TestAProblemInAWritingConnectionIsTold(t *testing.T) {
	head := "[sql.w]\ndriver = \"sqlite\"\npath = \"w.db\"\nmode = \"write\"\n"
	read := "[sql.w]\ndriver = \"sqlite\"\npath = \"w.db\"\n"
	statements := "[sql.w.statements]\nmark = \"INSERT INTO t (a) VALUES (:a)\"\nland = { sql = \"INSERT INTO t (a, b) VALUES (:a, :b)\", each = \"rows\", columns = [\"b\"] }\nlook = \"SELECT 1\"\n"
	for name, c := range map[string]struct{ text, want string }{
		"a bad mode":         {"[sql.w]\ndriver = \"sqlite\"\npath = \"w.db\"\nmode = \"both\"\n" + statements, "`mode` in [sql.w] has to be \"read\" or \"write\""},
		"mode not text":      {"[sql.w]\ndriver = \"sqlite\"\npath = \"w.db\"\nmode = 1\n" + statements, "`mode` in [sql.w] must be a text in quotes"},
		"a write on a read":  {read + "[sql.w.statements]\nmark = \"INSERT INTO t (a) VALUES (:a)\"\n", "needs a connection with mode = \"write\""},
		"no where":           {head + "[sql.w.statements]\nwipe = \"DELETE FROM t\"\n", "DELETE with no WHERE"},
		"a bad result":       {head + "[sql.w.statements]\nmark = { sql = \"INSERT INTO t (a) VALUES (:a)\", result = \"rows\" }\n", "only gives count"},
		"count of a read":    {head + "[sql.w.statements]\nlook = { sql = \"SELECT 1\", result = \"count\" }\n", "it has to be rows, row or value"},
		"each alone":         {head + "[sql.w.statements]\nland = { sql = \"INSERT INTO t (a) VALUES (:a)\", each = \"rows\" }\n", "`each` and `columns` go together"},
		"each of a read":     {head + "[sql.w.statements]\nland = { sql = \"SELECT :a\", each = \"rows\", columns = [\"a\"] }\n", "are for a statement that changes rows"},
		"unknown column":     {head + "[sql.w.statements]\nland = { sql = \"INSERT INTO t (a) VALUES (:a)\", each = \"rows\", columns = [\"z\"] }\n", "has no parameter :z"},
		"column twice":       {head + "[sql.w.statements]\nland = { sql = \"INSERT INTO t (a) VALUES (:a)\", each = \"rows\", columns = [\"a\", \"a\"] }\n", "names `a` twice"},
		"each is a param":    {head + "[sql.w.statements]\nland = { sql = \"INSERT INTO t (a, b) VALUES (:a, :b)\", each = \"a\", columns = [\"b\"] }\n", "also a parameter of the statement"},
		"a bad each":         {head + "[sql.w.statements]\nland = { sql = \"INSERT INTO t (a) VALUES (:a)\", each = \"two words\", columns = [\"a\"] }\n", "`each` is `two words`"},
		"columns not a list": {head + "[sql.w.statements]\nland = { sql = \"INSERT INTO t (a) VALUES (:a)\", each = \"rows\", columns = \"a\" }\n", "must be a list of texts"},
		"a text in columns":  {head + "[sql.w.statements]\nland = { sql = \"INSERT INTO t (a) VALUES (:a)\", each = \"rows\", columns = [1] }\n", "must be a text in quotes"},
		"transactions read":  {read + "[sql.w.statements]\nlook = \"SELECT 1\"\n[sql.w.transactions]\nt = [\"look\"]\n", "is for a connection with mode = \"write\""},
		"tx unknown step":    {head + statements + "[sql.w.transactions]\nt = [\"nope\"]\n", "step `nope` is not a statement"},
		"tx reading step":    {head + statements + "[sql.w.transactions]\nt = [\"look\"]\n", "step `look` only reads"},
		"tx same name":       {head + statements + "[sql.w.transactions]\nmark = [\"mark\"]\n", "both called `mark`"},
		"tx empty":           {head + statements + "[sql.w.transactions]\nt = []\n", "must be a list of texts"},
		"tx bad name":        {head + statements + "[sql.w.transactions]\n\"a b\" = [\"mark\"]\n", "a transaction called `a b`"},
		"tx bad setting":     {head + statements + "[sql.w.transactions]\nt = { steps = [\"mark\"], when = 1 }\n", "I do not know the setting `when`"},
		"tx a number":        {head + statements + "[sql.w.transactions]\nt = 5\n", "must be a list of the names"},
		"tx list and scalar": {head + "[sql.w.statements]\na = { sql = \"INSERT INTO t (x) VALUES (:x)\", each = \"xs\", columns = [\"x\"] }\nb = \"INSERT INTO u (xs) VALUES (:xs)\"\n[sql.w.transactions]\nt = [\"a\", \"b\"]\n", "is a list in one step"},
		"tx too long":        {head + statements + "[sql.w.transactions]\nt = [" + strings.Repeat("\"mark\",", MaxTransactionSteps+1) + "]\n", "needs from 1 to"},
	} {
		err := Default().apply("metagente.toml", c.text)
		if err == nil {
			t.Errorf("%s: no problem", name)
			continue
		}
		if shown := problemText(t, err); !strings.Contains(shown, c.want) {
			t.Errorf("%s: missing %q in:\n%s", name, c.want, shown)
		}
	}
}

func TestTheCeilingOfAListIsASetting(t *testing.T) {
	if Default().Limits.MaxSQLWriteRows != 10000 {
		t.Errorf("default = %d", Default().Limits.MaxSQLWriteRows)
	}
	cfg := Default()
	if err := cfg.apply("metagente.toml", "[limits]\nmax_sql_write_rows = 500\n"); err != nil || cfg.Limits.MaxSQLWriteRows != 500 {
		t.Errorf("limit = %d, %v", cfg.Limits.MaxSQLWriteRows, err)
	}
}

func TestTheCeilingOfCodecIsASetting(t *testing.T) {
	cfg := Default()
	if err := cfg.apply("metagente.toml", "[limits]\nmax_data_bytes = 4096\n"); err != nil || cfg.Limits.MaxDataBytes != 4096 {
		t.Errorf("limit = %d, %v", cfg.Limits.MaxDataBytes, err)
	}
	if shown := problemText(t, Default().apply("metagente.toml", "[limits]\nmax_data_bytes = 0\n")); !strings.Contains(shown, "max_data_bytes") {
		t.Errorf("shown:\n%s", shown)
	}
}
