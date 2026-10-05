package lang

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The port of `integration/perf.rs` of the original project: a file of five hundred lines parses
// in under 50 ms. The best of five runs counts; on this computer one takes about a millisecond.
func TestAFileOfFiveHundredLinesParsesInUnder50ms(t *testing.T) {
	var source strings.Builder
	for n := 0; strings.Count(source.String(), "\n") < 500; n++ {
		fmt.Fprintf(&source, "agent Agent%d\n  goal \"Agent number %d\"\n  tool file\n  tool state\n  accepts one name\n  accepts two\n"+
			"  on one\n    text = file.read path: \"a%d.txt\"\n    state.set key: \"k\" value: text\n"+
			"    if name is \"x\" and not text is \"y\"\n      reply \"first {name}\"\n    otherwise\n"+
			"      for item in [1, 2, 3]\n        state.set key: \"i\" value: item\n      reply \"second\"\n"+
			"  on two\n    reply state.get key: \"k\"\n", n, n, n)
	}
	best := time.Hour
	for i := 0; i < 5; i++ {
		started := time.Now()
		agents, err := ParseFile("big.ag", "", source.String())
		took := time.Since(started)
		if err != nil || len(agents) == 0 {
			t.Fatalf("%d agents, %v", len(agents), err)
		}
		best = min(best, took)
	}
	if best > 50*time.Millisecond {
		t.Errorf("parsing %d lines took %s", strings.Count(source.String(), "\n"), best)
	}
}
