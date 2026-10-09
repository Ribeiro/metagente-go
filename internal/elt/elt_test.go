package elt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
)

const settings = `
[sql.source]
driver = "sqlite"
path = "source.db"

[sql.outbox]
driver = "sqlite"
path = "outbox.db"
mode = "write"

[sql.dest]
driver = "sqlite"
path = "warehouse.db"
mode = "write"

[broker.main]
driver = "memory"

[elt.orders]
columns = ["id", "customer", "total"]

[elt.orders.source]
connection = "source"
table = "orders"
outbox = "outbox"
broker = "main"
rows = 500
bytes = 100000

[elt.orders.destination]
connection = "dest"
table = "orders_final"
upsert_on = "id"
set = { id = "id", customer = "customer" }
reject = [{ when = "total < 0", code = "TOTAL_NEGATIVE" }]
reject_share = 12.5
`

func load(t *testing.T, text string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, config.FileName)
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir, file)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func parse(t *testing.T, text string) []*lang.AgentDef {
	t.Helper()
	agents, err := lang.ParseFile("agent.ag", "", text)
	if err != nil {
		t.Fatal(err)
	}
	return agents
}

func problemOf(t *testing.T, err error) string {
	t.Helper()
	d, ok := diag.From(err)
	if !ok {
		return err.Error()
	}
	return d.Render()
}

func names(list []string) string { return strings.Join(list, " ") }

func TestAnExtractorLineGivesTheAgentItsToolsItsMessagesAndItsHandlers(t *testing.T) {
	agents := parse(t, "agent Extractor\n  goal \"Copy\"\n  tool orders from elt \"orders\" extract\n")
	if !Uses(agents[0]) {
		t.Fatal("the line was not seen")
	}
	if err := Expand(agents, load(t, settings)); err != nil {
		t.Fatal(problemOf(t, err))
	}
	agent := agents[0]
	var tools, messages []string
	for _, tool := range agent.Tools {
		tools = append(tools, tool.Name)
	}
	for _, accept := range agent.Accepts {
		messages = append(messages, accept.Message)
	}
	if names(tools) != "source outbox events codec clock" || names(messages) != "extract resend" || agent.FindHandler("extract") == nil || agent.FindHandler("resend") == nil {
		t.Fatalf("tools %v, messages %v", tools, messages)
	}
	if got := names(agent.FindTool("events").Allow); got != "etl.*.batch etl.*.control" {
		t.Errorf("the broker may publish to %s", got)
	}
	if got := agent.FindTool("source").ConnectionName() + " " + agent.FindTool("outbox").ConnectionName() + " " + agent.FindTool("events").ConnectionName(); got != "source outbox main" {
		t.Errorf("the connections are %s", got)
	}
	if Uses(agent) {
		t.Error("the line is still there after it was expanded")
	}
	if res := lang.Check(agents); len(res.Problems) != 0 {
		t.Errorf("the agent that was made does not pass the checks: %v", res.Problems)
	}
	// Every place in what was made is the line that asked for it, which is the one a person can read.
	if span := agent.FindHandler("extract").Span; span.Line != 3 {
		t.Errorf("the handler says it comes from line %d", span.Line)
	}
	// Expanding again changes nothing.
	if err := Expand(agents, load(t, settings)); err != nil || len(agent.Tools) != 5 {
		t.Errorf("a second expansion: %v, %d tools", err, len(agent.Tools))
	}
}

func TestAWorkerLineTakesTheLimitsFromTheDescription(t *testing.T) {
	agents := parse(t, "agent Worker\n  goal \"Load\"\n  tool orders from elt \"orders\" load\n")
	if err := Expand(agents, load(t, settings)); err != nil {
		t.Fatal(problemOf(t, err))
	}
	agent := agents[0]
	var messages []string
	for _, accept := range agent.Accepts {
		messages = append(messages, accept.Message)
	}
	if names(messages) != "batch control purge purge_control resume retransform" || agent.FindTool("dest").ConnectionName() != "dest" || agent.FindTool("codec") == nil {
		t.Fatalf("messages %v", messages)
	}
	if res := lang.Check(agents); len(res.Problems) != 0 {
		t.Errorf("the agent that was made does not pass the checks: %v", res.Problems)
	}
	text := render(workerText, load(t, settings).ELT["orders"])
	for _, want := range []string{`columns is not ["id", "customer", "total"]`, "if share is more than 12.5", "more than 12.5 percent of batch"} {
		if !strings.Contains(text, want) {
			t.Errorf("the Worker lacks %q", want)
		}
	}
	extractor := render(extractorText, load(t, settings).ELT["orders"])
	for _, want := range []string{"page_size = outbox.int n: 500", "limit = outbox.int n: 100000", `table: "orders"`, "upto = row.id"} {
		if !strings.Contains(extractor, want) {
			t.Errorf("the Extractor lacks %q", want)
		}
	}
}

func TestAnAgentThatUsesNoDescriptionIsLeftAsItIs(t *testing.T) {
	agents := parse(t, "agent Hello\n  goal \"Say hello\"\n  accepts greet name\n  on greet\n    reply \"Hello, {name}!\"\n")
	if Uses(agents[0]) {
		t.Error("an agent with no description was taken for one that has")
	}
	if err := Expand(agents, load(t, "")); err != nil || len(agents[0].Tools) != 0 || len(agents[0].Accepts) != 1 {
		t.Errorf("%v, %d tools, %d messages", err, len(agents[0].Tools), len(agents[0].Accepts))
	}
}

// onlyTheExtractor and onlyTheWorker are the settings of a machine that has one of the two parts.
func onlyTheExtractor() string {
	text := settings[:strings.Index(settings, "[elt.orders.destination]")]
	return strings.Replace(text, "[sql.dest]\ndriver = \"sqlite\"\npath = \"warehouse.db\"\nmode = \"write\"\n", "", 1)
}

func onlyTheWorker() string {
	text := settings[:strings.Index(settings, "[elt.orders.source]")] + settings[strings.Index(settings, "[elt.orders.destination]"):]
	text = strings.Replace(text, "[sql.source]\ndriver = \"sqlite\"\npath = \"source.db\"\n", "", 1)
	return strings.Replace(text, "[sql.outbox]\ndriver = \"sqlite\"\npath = \"outbox.db\"\nmode = \"write\"\n", "", 1)
}

func TestAProblemWithADescriptionIsToldAtTheLineThatAskedForIt(t *testing.T) {
	cases := map[string]struct{ agent, settings, want string }{
		"no description":             {"tool orders from elt \"other\" extract", settings, "has no section [elt.other]"},
		"no source":                  {"tool orders from elt \"orders\" extract", onlyTheWorker(), "has no source"},
		"no destination":             {"tool orders from elt \"orders\" load", onlyTheExtractor(), "has no destination"},
		"a tool of the same name":    {"tool codec\n  tool orders from elt \"orders\" load", settings, "has a tool called `codec`"},
		"a message of the same name": {"tool orders from elt \"orders\" load\n  accepts purge days\n  on purge\n    reply \"x\"", settings, "has a message called `purge`"},
	}
	for name, c := range cases {
		agents := parse(t, "agent A\n  goal \"x\"\n  "+c.agent+"\n")
		err := Expand(agents, load(t, c.settings))
		if err == nil {
			t.Errorf("%s: no problem was found", name)
			continue
		}
		if got := problemOf(t, err); !strings.Contains(got, c.want) {
			t.Errorf("%s: the problem is\n%s\nwant %q", name, got, c.want)
		}
	}
}
