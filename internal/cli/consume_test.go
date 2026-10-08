package cli

import (
	"strings"
	"testing"
	"time"
)

const workerAgentSource = "agent Worker\n  goal \"Load a batch\"\n  accepts batch n\n  accepts other\n  on batch\n    reply \"ok\"\n  on other\n    reply \"ok\"\n"

const memoryBrokerToml = "[broker.main]\ndriver = \"memory\"\n"

func consumeProject(t *testing.T) string {
	dir := project(t)
	writeFile(t, dir, "worker.ag", workerAgentSource)
	writeFile(t, dir, "metagente.toml", memoryBrokerToml)
	return dir
}

var consumeWords = []string{"consume", "worker.ag", "--from", "main", "--subject", "etl.orders.batch", "--dead", "etl.orders.dead", "--message", "batch"}

func TestConsumeReadsItsOptionsWithAValueAfterASpaceOrAnEquals(t *testing.T) {
	c, err := parseConsumeArgs([]string{"worker.ag", "--from=main", "--subject", "etl.>", "--dead=etl.dead", "--in-flight", "4", "--max-deliver=7",
		"--ack-wait", "90", "--idle-exit=1.5", "--max-events", "10", "--breaker-after", "2", "--backoff", "1s, 2m", "--durable", "w", "--stream=ETL", "--agent", "Worker", "--config", "x.toml", "--quiet"})
	if err != nil {
		t.Fatal(err)
	}
	if c.file != "worker.ag" || c.from != "main" || c.subject != "etl.>" || c.dead != "etl.dead" || c.inFlight != 4 || c.maxDeliver != 7 ||
		c.ackWait != 90*time.Second || c.idleExit != 1500*time.Millisecond || c.maxEvents != 10 || c.breakerAfter != 2 || !c.quiet ||
		len(c.backoff) != 2 || c.backoff[1] != 2*time.Minute || c.durable != "w" || c.stream != "ETL" || c.agent != "Worker" || c.configPath != "x.toml" {
		t.Errorf("args = %+v", c)
	}
}

func TestAMistakeInTheWordsOfConsumeIsExplained(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no file":        {[]string{"--from", "main", "--subject", "a", "--dead", "b"}, "needs the name of a file"},
		"two files":      {[]string{"a.ag", "b.ag"}, "takes one file"},
		"no from":        {[]string{"a.ag", "--subject", "a", "--dead", "b"}, "needs --from"},
		"no dead":        {[]string{"a.ag", "--from", "m", "--subject", "a"}, "needs --from"},
		"unknown option": {[]string{"a.ag", "--force"}, "does not take the option `--force`"},
		"no value":       {[]string{"a.ag", "--from"}, "`--from` needs a value"},
		"not a number":   {[]string{"a.ag", "--in-flight", "many"}, "whole number of 1 or more"},
		"zero":           {[]string{"a.ag", "--max-deliver", "0"}, "whole number of 1 or more"},
		"bad seconds":    {[]string{"a.ag", "--ack-wait", "soon"}, "number of seconds"},
		"bad backoff":    {[]string{"a.ag", "--backoff", "10s,soon"}, "list of waits"},
	} {
		_, err := parseConsumeArgs(c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	code, _, stderr := run(t, "consume", "--force")
	if code != 2 || !strings.Contains(stderr, "Problem:") {
		t.Errorf("exit code %d:\n%s", code, stderr)
	}
}

func TestConsumeAsksForTheApprovalOfTheBrokerItReadsAndSaysHowToGiveIt(t *testing.T) {
	consumeProject(t)
	nobodyThere(t)
	code, _, stderr := run(t, consumeWords...)
	if code != 1 {
		t.Fatalf("exit code %d:\n%s", code, stderr)
	}
	assertContains(t, stderr, "uses the message broker: memory", "subjects read etl.orders.batch, write etl.orders.dead",
		"metagente trust worker.ag --from main --subject etl.orders.batch --dead etl.orders.dead")
}

func TestConsumeRunsAfterTheApprovalAndTellsWhatHappened(t *testing.T) {
	consumeProject(t)
	nobodyThere(t)
	code, stdout, stderr := run(t, "trust", "worker.ag", "--from", "main", "--subject", "etl.orders.batch", "--dead", "etl.orders.dead", "--yes")
	if code != 0 {
		t.Fatalf("trust: %d\n%s", code, stderr)
	}
	assertContains(t, stdout, "[NEW] uses the message broker: memory", "Approved 1 item")
	words := append(append([]string{}, consumeWords...), "--idle-exit", "0.05")
	code, stdout, stderr = run(t, words...)
	if code != 0 {
		t.Fatalf("consume: %d\n%s", code, stderr)
	}
	assertContains(t, stdout, "Taken 0: done 0, asked for again 0, dead letters 0, given back 0.")
	assertContains(t, stderr, "Reading etl.orders.batch from the broker main for Worker.batch")
	// Another subject is not what was approved.
	other := append([]string{}, words...)
	other[5] = "etl.orders.control"
	if code, _, stderr = run(t, other...); code != 1 || !strings.Contains(stderr, "have not approved") {
		t.Errorf("another subject: %d\n%s", code, stderr)
	}
	// --quiet keeps the lines of what happens to the events off the screen.
	if code, _, stderr = run(t, append(words, "--quiet")...); code != 0 || strings.Contains(stderr, "Reading") {
		t.Errorf("quiet: %d\n%s", code, stderr)
	}
}

func TestConsumeNeedsToKnowWhichMessageTheEventsAre(t *testing.T) {
	consumeProject(t)
	nobodyThere(t)
	words := []string{"consume", "worker.ag", "--from", "main", "--subject", "etl.orders.batch", "--dead", "etl.orders.dead"}
	code, _, stderr := run(t, words...)
	assertContains(t, stderr, "needs to be told which message the events are", "it accepts: batch, other")
	if code != 1 {
		t.Errorf("exit code %d", code)
	}
	_, _, stderr = run(t, append(words, "--message", "nope")...)
	assertContains(t, stderr, "does not accept the message `nope`")
}

func TestConsumeRefusesWhatCannotBeASubjectOrABroker(t *testing.T) {
	consumeProject(t)
	nobodyThere(t)
	for name, c := range map[string]struct {
		change func(w []string)
		want   string
	}{
		"a subject to read": {func(w []string) { w[5] = "etl..batch" }, "is not a subject to read"},
		"a dead subject":    {func(w []string) { w[7] = "etl.*" }, "is not a subject for the dead letters"},
		"a missing broker":  {func(w []string) { w[3] = "nowhere" }, "no section [broker.nowhere]"},
	} {
		words := append([]string{}, consumeWords...)
		c.change(words)
		code, _, stderr := run(t, words...)
		if code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: %d\n%s", name, code, stderr)
		}
	}
	code, _, stderr := run(t, "trust", "worker.ag", "--from", "main")
	if code != 2 || !strings.Contains(stderr, "--from, --subject and --dead go together") {
		t.Errorf("trust with a part: %d\n%s", code, stderr)
	}
}

func TestTheNameOfADurableConsumerIsMadeOfSafeCharacters(t *testing.T) {
	if got := sanitizeName("metagente-Worker-my message/1"); got != "metagente-Worker-my_message_1" {
		t.Errorf("name = %q", got)
	}
	waits, err := parseWaits("10s,1m")
	if err != nil || len(waits) != 2 || waits[0] != 10*time.Second {
		t.Errorf("waits = %v, %v", waits, err)
	}
}
