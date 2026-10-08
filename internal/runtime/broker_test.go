package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/trust"
)

const brokerToml = "[broker.main]\ndriver = \"memory\"\n"

const senderSource = `agent Sender
  goal "Send the batches"
  tool events from broker "main" publish "etl.orders.batch" "etl.orders.control"
  accepts go start
  on go
    for n in [1, 2, 3]
      events.publish subject: "etl.orders.batch" id: "job7:{n}" data: [n, "x"]
    sent = events.publish subject: "etl.orders.control" id: "job7:end" data: "done"
    again = events.publish subject: "etl.orders.control" id: "job7:end" data: "done"
    reply "last {sent.seq}; again is a copy: {again.duplicate}"
`

func yes([]trust.Item) bool { return true }

func brokerRuntime(t *testing.T, dir, toml string) *Runtime {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(errText(t, err))
	}
	rt := New(cfg)
	rt.Trust = trust.NewRegistry(filepath.Join(t.TempDir(), "approvals"))
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func brokerProject(t *testing.T, toml string) (rt *Runtime, file string) {
	t.Helper()
	dir := t.TempDir()
	rt = brokerRuntime(t, dir, toml)
	file = filepath.Join(dir, "sender.ag")
	if err := os.WriteFile(file, []byte(senderSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return rt, file
}

func TestAnAgentPublishesBatchesAndACopyIsRecognized(t *testing.T) {
	rt, file := brokerProject(t, brokerToml)
	got, err := RunFile(t.Context(), rt, Options{File: file, Message: "go", Params: []string{"start=x"}, Confirm: yes})
	if err != nil || got.Text != "last 4; again is a copy: yes" {
		t.Fatalf("got %q, %v", got.Display(), err)
	}
	msgs := rt.Broker.Memory("main").Messages()
	if len(msgs) != 4 || msgs[0].ID != "job7:1" || string(msgs[2].Data) != `[3,"x"]` || msgs[3].Subject != "etl.orders.control" {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestABrokerNobodyApprovedIsNotReached(t *testing.T) {
	rt, file := brokerProject(t, brokerToml)
	_, err := RunFile(t.Context(), rt, Options{File: file, Message: "go", Params: []string{"start=x"}, Confirm: func(missing []trust.Item) bool {
		if len(missing) != 1 || missing[0].Kind != trust.KindBroker {
			t.Errorf("the person was asked about %v", missing)
		}
		return false
	}})
	mustContain(t, errText(t, err), "uses the message broker: memory in the memory of this process", "subjects etl.orders.batch, etl.orders.control", "metagente trust")
	if n := len(rt.Broker.Memory("main").Messages()); n != 0 {
		t.Errorf("%d messages were published", n)
	}
}

func TestChangingTheSubjectsOfTheToolAsksForTheApprovalAgain(t *testing.T) {
	rt, _ := brokerProject(t, brokerToml)
	agents, err := lang.ParseFile("sender.ag", "", senderSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Trust.Approve(rt.Config.Root, rt.Needs(agents)); err != nil {
		t.Fatal(err)
	}
	wider, err := lang.ParseFile("sender.ag", "", strings.Replace(senderSource, `"etl.orders.control"`, `"etl.orders.control" "other.>"`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if missing, _ := rt.Trust.Missing(rt.Config.Root, rt.Needs(agents)); len(missing) != 0 {
		t.Errorf("asked again for nothing: %v", missing)
	}
	if missing, _ := rt.Trust.Missing(rt.Config.Root, rt.Needs(wider)); len(missing) != 1 {
		t.Errorf("a wider tool was not asked about: %v", missing)
	}
}

func TestAnAgentWhoseBrokerIsFullFailsWithAFailureThatMayPass(t *testing.T) {
	rt, file := brokerProject(t, brokerToml)
	rt.Broker.Memory("main").MaxMessages = 1
	_, err := RunFile(t.Context(), rt, Options{File: file, Message: "go", Params: []string{"start=x"}, Confirm: yes})
	if _, ok := diag.RetryOf(err); !ok {
		t.Fatalf("a full broker is a failure that may pass: %v", err)
	}
	mustContain(t, errText(t, err), "the broker is full", "This may pass")
}

func TestABrokerThatIsNotInTheConfigurationStopsTheAgentWithAnExplanation(t *testing.T) {
	rt, file := brokerProject(t, "[runtime]\ntimeout_seconds = 30\n")
	_, err := RunFile(t.Context(), rt, Options{File: file, Message: "go", Params: []string{"start=x"}, Confirm: yes})
	mustContain(t, errText(t, err), "the tool `events` uses the broker `main`", "no section [broker.main]")
}

func TestPublishingToASubjectTheToolDidNotDeclareIsRefusedByCheckAndByTheRun(t *testing.T) {
	rt, _ := brokerProject(t, brokerToml)
	source := strings.Replace(senderSource, `subject: "etl.orders.control" id: "job7:end" data: "done"
    again`, `subject: "elsewhere.x" id: "job7:end" data: "done"
    again`, 1)
	file := filepath.Join(rt.Config.Root, "wrong.ag")
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RunFile(t.Context(), rt, Options{File: file, Message: "go", Params: []string{"start=x"}, Confirm: yes})
	mustContain(t, errText(t, err), "may not publish to `elsewhere.x`", "it declared: etl.orders.batch, etl.orders.control")
}
