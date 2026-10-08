package lang

import (
	"strings"
	"testing"
)

func codecAgent(lines string) string {
	return "agent Packer\n  goal \"Pack\"\n  tool codec\n  accepts go start\n  on go\n" + lines + "    reply \"ok\"\n"
}

func TestACodecToolIsDeclaredWithNothingElse(t *testing.T) {
	agents := mustParse(t, codecAgent("    id = codec.uuid\n"))
	if got := agents[0].Tools[0]; got.Kind != ToolCodec || got.Name != "codec" {
		t.Fatalf("tool = %+v", got)
	}
	if res := Check(agents); len(res.Problems) != 0 {
		t.Errorf("problems: %v", res.Problems)
	}
	if msg := parseError(t, "agent A\n  goal \"g\"\n  tool codec readonly\n  accepts go\n  on go\n    reply \"x\"\n"); msg == "" {
		t.Error("`tool codec readonly` must not be accepted")
	}
}

func TestRecordTakesAnyNamesAndTheOtherActionsTakeTheirOwn(t *testing.T) {
	good := codecAgent("    r = codec.record v: 1 job_id: start seq: 4 rows: [1, 2]\n" +
		"    t = codec.table rows: start columns: [\"id\"]\n" +
		"    j = codec.json value: r\n" +
		"    s = codec.sha256 text: j\n" +
		"    p = codec.gzip text: j\n")
	if res := Check(mustParse(t, good)); len(res.Problems) != 0 {
		t.Errorf("problems: %v", res.Problems)
	}
	for line, want := range map[string]string{
		"    x = codec.json value: 1 extra: 2\n": "does not take `extra`",
		"    x = codec.json\n":                   "needs",
		"    x = codec.jsno value: 1\n":          "jsno",
		"    x = codec.gzip\n":                   "needs",
	} {
		res := Check(mustParse(t, codecAgent(line)))
		found := false
		for _, p := range res.Problems {
			found = found || strings.Contains(p.Render(), want)
		}
		if !found {
			t.Errorf("%q: no problem with %q in %v", line, want, res.Problems)
		}
	}
}

func TestCodecIsAReservedNameButDataIsStillAFreeOne(t *testing.T) {
	// `data` is a name that many agents use for a value, and it has to stay free.
	src := "agent A\n  goal \"g\"\n  tool codec\n  accepts go start\n  on go\n    data = codec.uuid\n    reply \"{data}\"\n"
	if res := Check(mustParse(t, src)); len(res.Problems) != 0 {
		t.Errorf("problems: %v", res.Problems)
	}
	if !IsBuiltinName("codec") || IsBuiltinName("data") {
		t.Error("codec must be a built in name and data must not")
	}
}
