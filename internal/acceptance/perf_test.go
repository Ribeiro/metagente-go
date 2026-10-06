//go:build acceptance

package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The port of `integration/perf.rs` of the original project: a trivial agent answers, from a cold
// start of the program, in under 100 ms. The best of seven runs counts, so that a computer that
// is busy for a moment does not fail it; on this computer one run takes about 10 ms.
func TestATrivialAgentAnswersFromAColdStartInUnder100ms(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "metagente")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "github.com/Ribeiro/metagente-go/cmd/metagente")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.ag"), []byte("agent A\n  goal \"x\"\n  accepts go\n  on go\n    reply \"ok\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	best := time.Hour
	for i := 0; i < 7; i++ {
		run := exec.Command(binary, "run", "a.ag", "go")
		run.Dir = dir
		run.Env = append(os.Environ(), "METAGENTE_STATE_DIR="+filepath.Join(dir, "state"))
		started := time.Now()
		out, err := run.Output()
		took := time.Since(started)
		if err != nil || strings.TrimSpace(string(out)) != "ok" {
			t.Fatalf("run: %v, %q", err, out)
		}
		best = min(best, took)
	}
	if best > 100*time.Millisecond {
		t.Errorf("the best time from start to answer was %s", best)
	}
}
