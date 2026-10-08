//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mariadb"
	"github.com/testcontainers/testcontainers-go/modules/mssql"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The images are pinned by digest, so a run today and a run in a year use the same servers. To move to
// a newer one, change the digest here on purpose. The digest is the one of the image on any registry that
// copies Docker Hub, so METAGENTE_IT_REGISTRY can name a mirror (with the slash at the end, like
// "mirror.gcr.io/"); it is "docker.io/" when it is not set.
const (
	postgresImage = "library/postgres:16-alpine@sha256:721873c34ceb9f8d8fc265984940dc982404c105f19ad51be9fdc5970a6080ea"
	mariadbImage  = "library/mariadb:11.4@sha256:1292844148b311e4ed4300022a996d39083f415a963e970cf47cad1b3b18e3a6"
	mysqlImage    = "library/mysql:8.4@sha256:6ea90827b1100f8f2ae306a539f86d2c264a26ed435a2a9f75551dd5c3aeb242"
	oracleImage   = "gvenzl/oracle-free:23-slim-faststart@sha256:f5ff19033860d662c821cb04eb10483fa94f14f78eae252d054291ea07028093"
)

// SQL Server is on the registry of Microsoft, not on Docker Hub, so a mirror of Docker Hub does not have it.
const sqlserverImage = "mcr.microsoft.com/mssql/server:2022-CU14-ubuntu-22.04@sha256:c1aa8afe9b06eab64c9774a4802dcd032205d1be785b1fd51e1c0151e7586b74"

const (
	dbName   = "orders"
	dbUser   = "reader"
	dbSecret = "s3cret-p4ss"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "metagente-it")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "metagente")
	build := exec.Command("go", "build", "-o", binary, "github.com/Ribeiro/metagente-go/cmd/metagente")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the program: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// server is a database in a container.
type server struct {
	driver string
	host   string
	port   int
}

// user and database are what the connection names. SQL Server has only its administrator in the container,
// and Oracle names its database by the service of the pluggable database.
func (s server) user() string {
	if s.driver == "sqlserver" {
		return "sa"
	}
	return dbUser
}

func (s server) database() string {
	if s.driver == "oracle" {
		return "FREEPDB1"
	}
	return dbName
}

// page is the statement that reads a page of orders, written in the dialect of the server. Oracle gives its
// columns in capitals unless they are quoted, and its numbers as it likes, so the columns are named and the
// numbers go as text.
func (s server) page() string {
	switch s.driver {
	case "sqlserver":
		return "SELECT TOP (:size) id, customer, total, big FROM orders WHERE id > :after ORDER BY id"
	case "oracle":
		return `SELECT id AS "id", customer AS "customer", TO_CHAR(total, 'FM999990.00') AS "total", TO_CHAR(big) AS "big" FROM orders WHERE id > :after ORDER BY id FETCH FIRST :size ROWS ONLY`
	}
	return "SELECT id, customer, total, big FROM orders WHERE id > :after ORDER BY id LIMIT :size"
}

func (s server) one() string {
	if s.driver == "oracle" {
		return `SELECT id AS "id", customer AS "customer", TO_CHAR(total, 'FM999990.00') AS "total", TO_CHAR(big) AS "big" FROM orders WHERE id = :id`
	}
	return "SELECT id, customer, total, big FROM orders WHERE id = :id"
}

func (s server) none() string {
	if s.driver == "oracle" {
		return `SELECT id AS "id" FROM orders WHERE id = :id`
	}
	return "SELECT id FROM orders WHERE id = :id"
}

// writing is a statement that reads and still asks the database to change something. Where a function may
// write (PostgreSQL, MySQL, MariaDB) it calls one; Oracle has no such function without a trick, so it locks
// the row, which a transaction that only reads refuses. SQL Server has neither, and has no test of this.
func (s server) writing() string {
	if s.driver == "oracle" {
		return "SELECT id FROM orders WHERE id = 1 FOR UPDATE"
	}
	return "SELECT write_note()"
}

// seed is the same orders for every database: 25 rows, and a number that a number of the language cannot
// hold. It also has a function that writes a line in a table: a statement that calls it is a SELECT, and
// the only thing that stops it is that the transaction is read only (the user may write: it owns the database).
func seed(t *testing.T, driver string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("CREATE TABLE orders (id BIGINT PRIMARY KEY, customer VARCHAR(50), total DECIMAL(10,2), big BIGINT);\n")
	sb.WriteString("CREATE TABLE journal (note VARCHAR(50));\n")
	sb.WriteString("CREATE TABLE landing (job BIGINT, id BIGINT, customer VARCHAR(50), PRIMARY KEY (job, id));\n")
	sb.WriteString("CREATE TABLE marks (job BIGINT PRIMARY KEY, state VARCHAR(20));\n")
	switch driver {
	case "postgres":
		sb.WriteString("CREATE FUNCTION write_note() RETURNS int LANGUAGE plpgsql AS $$ BEGIN INSERT INTO journal VALUES ('written'); RETURN 1; END $$;\n")
	case "mariadb", "mysql":
		sb.WriteString("SET GLOBAL log_bin_trust_function_creators = 1;\nDELIMITER //\nCREATE FUNCTION write_note() RETURNS INT MODIFIES SQL DATA BEGIN INSERT INTO journal VALUES ('written'); RETURN 1; END//\nDELIMITER ;\n")
	}
	// Neither SQL Server nor Oracle has a function that changes rows when a query calls it.
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&sb, "INSERT INTO orders VALUES (%d, 'Customer %d', %d.50, 9007199254740993);\n", i, i, i)
	}
	if driver == "postgres" || driver == "sqlserver" || driver == "oracle" {
		// The control tables of the Worker of the sample, so that its test needs no second database.
		raw, err := os.ReadFile(filepath.Join("..", "samples", "async-elt", "migrations", "destination."+driver+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(raw)
	}
	text := sb.String()
	switch driver {
	case "sqlserver":
		text = "CREATE DATABASE orders;\nGO\nUSE orders;\nGO\n" + text
	case "oracle":
		// Oracle has no BIGINT.
		text = strings.ReplaceAll(text, "BIGINT", "NUMBER(19)")
		// The script may run as the administrator, so it says who it is: the tables have to be the user's.
		text = "CONNECT " + dbUser + "/" + dbSecret + "@//localhost:1521/FREEPDB1\n" + text
	}
	path := filepath.Join(t.TempDir(), "seed.sql")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func image(name string) string {
	registry := os.Getenv("METAGENTE_IT_REGISTRY")
	if registry == "" {
		registry = "docker.io/"
	}
	return registry + name
}

func start(t *testing.T, driver string) server {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	path := seed(t, driver)
	var (
		ctr  testcontainers.Container
		port string
		err  error
	)
	s := server{driver: driver}
	switch driver {
	case "postgres":
		var c *postgres.PostgresContainer
		c, err = postgres.Run(ctx, image(postgresImage),
			postgres.WithDatabase(dbName), postgres.WithUsername(dbUser), postgres.WithPassword(dbSecret),
			postgres.WithInitScripts(path), postgres.BasicWaitStrategies())
		ctr, port = c, "5432/tcp"
	case "mariadb":
		var c *mariadb.MariaDBContainer
		c, err = mariadb.Run(ctx, image(mariadbImage),
			mariadb.WithDatabase(dbName), mariadb.WithUsername(dbUser), mariadb.WithPassword(dbSecret),
			mariadb.WithScripts(path))
		ctr, port = c, "3306/tcp"
	case "mysql":
		var c *mysql.MySQLContainer
		c, err = mysql.Run(ctx, image(mysqlImage),
			mysql.WithDatabase(dbName), mysql.WithUsername(dbUser), mysql.WithPassword(dbSecret),
			mysql.WithScripts(path))
		ctr, port = c, "3306/tcp"
	case "sqlserver":
		var c *mssql.MSSQLServerContainer
		var script *os.File
		if script, err = os.Open(path); err != nil {
			t.Fatal(err)
		}
		defer script.Close()
		c, err = mssql.Run(ctx, sqlserverImage, mssql.WithAcceptEULA(), mssql.WithPassword(dbSecret), mssql.WithInitSQL(script))
		ctr, port = c, "1433/tcp"
	case "oracle":
		var c testcontainers.Container
		c, err = testcontainers.Run(ctx, image(oracleImage),
			testcontainers.WithExposedPorts("1521/tcp"),
			testcontainers.WithEnv(map[string]string{"ORACLE_PASSWORD": dbSecret, "APP_USER": dbUser, "APP_USER_PASSWORD": dbSecret}),
			testcontainers.WithFiles(testcontainers.ContainerFile{HostFilePath: path, ContainerFilePath: "/container-entrypoint-initdb.d/seed.sql", FileMode: 0o644}),
			testcontainers.WithWaitStrategy(wait.ForLog("DATABASE IS READY TO USE!").WithStartupTimeout(5*time.Minute)))
		ctr, port = c, "1521/tcp"
		// A transaction that only reads is refused (ORA-01466) while the tables are only seconds old.
		time.Sleep(10 * time.Second)
	}
	if ctr != nil {
		testcontainers.CleanupContainer(t, ctr)
	}
	if err != nil {
		t.Fatalf("starting %s: %v", driver, err)
	}
	if s.host, err = ctr.Host(ctx); err != nil {
		t.Fatal(err)
	}
	mapped, err := ctr.MappedPort(ctx, port)
	if err != nil {
		t.Fatal(err)
	}
	s.port, _ = strconv.Atoi(mapped.Port())
	return s
}

// project writes a folder with the configuration and an agent for the server and returns its path.
func (s server) project(t *testing.T, password string) string {
	t.Helper()
	dir := t.TempDir()
	config := fmt.Sprintf(`[credentials]
orders = "DB_PASSWORD"

[sql.db]
driver = %q
host = %q
port = %d
database = %q
user = %q
tls = "disable"

[sql.db.statements]
page = %q
count = { sql = "SELECT count(*) FROM orders", result = "value" }
one = { sql = %q, result = "row" }
none = { sql = %q, result = "row" }
write = { sql = %q, result = "value" }
written = { sql = "SELECT count(*) FROM journal", result = "value" }
`, s.driver, s.host, s.port, s.database(), s.user(), s.page(), s.one(), s.none(), s.writing())
	agent := `agent Pager
  goal "Read the orders in pages"
  tool orders from sql "db"
  accepts go start
  on go
    after = 0
    last = nothing
    repeat while after is not nothing
      page = orders.page after: after size: 10
      after = nothing
      for row in page
        after = row.id
        last = row.id
    total = orders.count
    one = orders.one id: 7
    none = orders.none id: 999
    reply "last {last}; count {total}; one {one.customer} {one.total} {one.big}; none {none}"
  accepts write start
  on write
    orders.write
    reply "written"
  accepts written start
  on written
    n = orders.written
    reply "journal {n}"
`
	write(t, filepath.Join(dir, "metagente.toml"), config)
	write(t, filepath.Join(dir, "pager.ag"), agent)
	return dir
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// approvals is the folder where the approvals of a project are kept: it must be outside the project.
func approvals(t *testing.T, dir string) string {
	t.Helper()
	folder, _ := approvalFolders.LoadOrStore(dir, t.TempDir())
	return folder.(string)
}

var approvalFolders sync.Map

// metagente runs the compiled program in a folder, with the password in the environment.
func metagente(t *testing.T, dir, password string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DB_PASSWORD="+password, "BROKER_PASSWORD="+password, "METAGENTE_CONFIG_DIR="+approvals(t, dir))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAnAgentReadsEveryRowInPages(t *testing.T) {
	for _, driver := range []string{"postgres", "mariadb", "mysql", "sqlserver", "oracle"} {
		t.Run(driver, func(t *testing.T) {
			s := start(t, driver)
			dir := s.project(t, dbSecret)
			if out, err := metagente(t, dir, dbSecret, "trust", "pager.ag", "--yes"); err != nil {
				t.Fatalf("trust: %v\n%s", err, out)
			}
			out, err := metagente(t, dir, dbSecret, "run", "pager.ag", "go", "start=x")
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			// 9007199254740993 does not fit in a number of the language: it must come with all its digits.
			want := "last 25; count 25; one Customer 7 7.50 9007199254740993; none nothing"
			if !strings.Contains(out, want) {
				t.Errorf("the answer was not what I expected\nwant: %s\ngot:  %s", want, out)
			}
		})
	}
}

// A statement that is a SELECT can still write, through a function. The transaction is read only, so
// the database refuses, whatever the user is allowed to do.
func TestAStatementThatWritesIsRefusedByTheDatabase(t *testing.T) {
	for _, driver := range []string{"postgres", "mariadb", "mysql", "oracle"} {
		t.Run(driver, func(t *testing.T) {
			s := start(t, driver)
			dir := s.project(t, dbSecret)
			if out, err := metagente(t, dir, dbSecret, "trust", "pager.ag", "--yes"); err != nil {
				t.Fatalf("trust: %v\n%s", err, out)
			}
			out, err := metagente(t, dir, dbSecret, "run", "pager.ag", "write", "start=x")
			if err == nil || !strings.Contains(strings.ToLower(out), "read-only") && !strings.Contains(strings.ToLower(out), "read only") {
				t.Fatalf("the write was not refused (err = %v):\n%s", err, out)
			}
			out, err = metagente(t, dir, dbSecret, "run", "pager.ag", "written", "start=x")
			if err != nil || !strings.Contains(out, "journal 0") {
				t.Errorf("something was written (err = %v):\n%s", err, out)
			}
		})
	}
}

func TestAWrongPasswordIsToldWithoutShowingIt(t *testing.T) {
	for _, driver := range []string{"postgres", "mariadb", "sqlserver"} {
		t.Run(driver, func(t *testing.T) {
			s := start(t, driver)
			dir := s.project(t, dbSecret)
			if out, err := metagente(t, dir, "wrong-"+dbSecret, "trust", "pager.ag", "--yes"); err != nil {
				t.Fatalf("trust: %v\n%s", err, out)
			}
			out, err := metagente(t, dir, "wrong-"+dbSecret, "run", "pager.ag", "go", "start=x")
			if err == nil {
				t.Fatalf("a wrong password was accepted:\n%s", out)
			}
			if !strings.Contains(out, "I could not reach the database") {
				t.Errorf("the problem is not explained:\n%s", out)
			}
			if strings.Contains(out, dbSecret) {
				t.Errorf("the password is in the answer:\n%s", out)
			}
		})
	}
}
