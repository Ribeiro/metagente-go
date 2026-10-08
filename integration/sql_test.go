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
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// The images are pinned by digest, so a run today and a run in a year use the same servers. To move to
// a newer one, change the digest here on purpose. The digest is the one of the image on any registry that
// copies Docker Hub, so METAGENTE_IT_REGISTRY can name a mirror (with the slash at the end, like
// "mirror.gcr.io/"); it is "docker.io/" when it is not set.
const (
	postgresImage = "library/postgres:16-alpine@sha256:721873c34ceb9f8d8fc265984940dc982404c105f19ad51be9fdc5970a6080ea"
	mariadbImage  = "library/mariadb:11.4@sha256:1292844148b311e4ed4300022a996d39083f415a963e970cf47cad1b3b18e3a6"
	mysqlImage    = "library/mysql:8.4@sha256:6ea90827b1100f8f2ae306a539f86d2c264a26ed435a2a9f75551dd5c3aeb242"
)

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

// seed is the same orders for every database: 25 rows, and a number that a number of the language cannot
// hold. It also has a function that writes a line in a table: a statement that calls it is a SELECT, and
// the only thing that stops it is that the transaction is read only (the user may write: it owns the database).
func seed(t *testing.T, driver string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("CREATE TABLE orders (id BIGINT PRIMARY KEY, customer VARCHAR(50), total DECIMAL(10,2), big BIGINT);\n")
	sb.WriteString("CREATE TABLE journal (note VARCHAR(50));\n")
	if driver == "postgres" {
		sb.WriteString("CREATE FUNCTION write_note() RETURNS int LANGUAGE plpgsql AS $$ BEGIN INSERT INTO journal VALUES ('written'); RETURN 1; END $$;\n")
	} else {
		sb.WriteString("SET GLOBAL log_bin_trust_function_creators = 1;\nDELIMITER //\nCREATE FUNCTION write_note() RETURNS INT MODIFIES SQL DATA BEGIN INSERT INTO journal VALUES ('written'); RETURN 1; END//\nDELIMITER ;\n")
	}
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&sb, "INSERT INTO orders VALUES (%d, 'Customer %d', %d.50, 9007199254740993);\n", i, i, i)
	}
	path := filepath.Join(t.TempDir(), "seed.sql")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
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
	script := seed(t, driver)
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
			postgres.WithInitScripts(script), postgres.BasicWaitStrategies())
		ctr, port = c, "5432/tcp"
	case "mariadb":
		var c *mariadb.MariaDBContainer
		c, err = mariadb.Run(ctx, image(mariadbImage),
			mariadb.WithDatabase(dbName), mariadb.WithUsername(dbUser), mariadb.WithPassword(dbSecret),
			mariadb.WithScripts(script))
		ctr, port = c, "3306/tcp"
	case "mysql":
		var c *mysql.MySQLContainer
		c, err = mysql.Run(ctx, image(mysqlImage),
			mysql.WithDatabase(dbName), mysql.WithUsername(dbUser), mysql.WithPassword(dbSecret),
			mysql.WithScripts(script))
		ctr, port = c, "3306/tcp"
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
page = "SELECT id, customer, total, big FROM orders WHERE id > :after ORDER BY id LIMIT :size"
count = { sql = "SELECT count(*) FROM orders", result = "value" }
one = { sql = "SELECT id, customer, total, big FROM orders WHERE id = :id", result = "row" }
none = { sql = "SELECT id FROM orders WHERE id = :id", result = "row" }
write = { sql = "SELECT write_note()", result = "value" }
written = { sql = "SELECT count(*) FROM journal", result = "value" }
`, s.driver, s.host, s.port, dbName, dbUser)
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
	cmd.Env = append(os.Environ(), "DB_PASSWORD="+password, "METAGENTE_CONFIG_DIR="+approvals(t, dir))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAnAgentReadsEveryRowInPages(t *testing.T) {
	for _, driver := range []string{"postgres", "mariadb", "mysql"} {
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
	for _, driver := range []string{"postgres", "mariadb", "mysql"} {
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
	for _, driver := range []string{"postgres", "mariadb"} {
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
