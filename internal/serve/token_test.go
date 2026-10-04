package serve

import (
	"strings"
	"testing"
)

func TestAGeneratedTokenIsLongRandomAndFitForAHeader(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		token := GenerateToken()
		if len(token) < 43 {
			t.Fatalf("token of %d characters", len(token))
		}
		if err := CheckToken(token); err != nil {
			t.Fatalf("a generated token is not accepted: %v", err)
		}
		if seen[token] {
			t.Fatal("the same token twice")
		}
		seen[token] = true
	}
}

// req: S1
func TestOnlyALongPlainTokenIsAccepted(t *testing.T) {
	long := strings.Repeat("a", 32)
	for _, good := range []string{long, strings.Repeat("Ab3-_", 10), "A" + long} {
		if err := CheckToken(good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
	for name, bad := range map[string]string{
		"short":       "tooshort",
		"31":          strings.Repeat("a", 31),
		"space":       long + " x",
		"tab":         long + "\t",
		"newline":     long + "\n",
		"quote":       long + `"`,
		"colon":       long + ":",
		"non ascii":   long + "é",
		"control":     long + "\x01",
		"empty":       "",
		"only spaces": strings.Repeat(" ", 40),
	} {
		err := CheckToken(bad)
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		// A token that is refused is never repeated in the reason.
		if strings.Contains(err.Error(), "tooshort") || (len(bad) > 8 && strings.Contains(err.Error(), bad)) {
			t.Errorf("%s: the reason repeats the token: %v", name, err)
		}
	}
}

// req: S1
func TestTheTokenComesFromTheEnvironmentOrIsGeneratedOnlyWhereItIsSafeToShowIt(t *testing.T) {
	env := func(value string) func(string) string {
		return func(name string) string {
			if name == "METAGENTE_TOKEN" {
				return value
			}
			return ""
		}
	}
	good := strings.Repeat("k", 40)
	for name, tt := range map[string]struct {
		env         string
		terminal    bool
		loopback    bool
		wantToken   string // "" means a generated one
		wantGen     bool
		wantProblem string
	}{
		"given":                  {env: good, wantToken: good},
		"given and public":       {env: good, terminal: false, loopback: false, wantToken: good},
		"generated for a person": {terminal: true, loopback: true, wantGen: true},
		"no terminal":            {terminal: false, loopback: true, wantProblem: "METAGENTE_TOKEN"},
		"not loopback":           {terminal: true, loopback: false, wantProblem: "METAGENTE_TOKEN"},
		"neither":                {wantProblem: "METAGENTE_TOKEN"},
		"too short":              {env: "short", terminal: true, loopback: true, wantProblem: "at least 32"},
	} {
		token, generated, err := ResolveToken(env(tt.env), tt.terminal, tt.loopback)
		switch {
		case tt.wantProblem != "":
			if err == nil || !strings.Contains(err.Error(), tt.wantProblem) {
				t.Errorf("%s: err = %v", name, err)
			}
			if token != "" {
				t.Errorf("%s: a token came with the problem", name)
			}
			if tt.env != "" && strings.Contains(err.Error(), tt.env) && len(tt.env) > 8 {
				t.Errorf("%s: the problem repeats the token", name)
			}
		case err != nil:
			t.Errorf("%s: unexpected problem: %v", name, err)
		case tt.wantGen && (!generated || CheckToken(token) != nil):
			t.Errorf("%s: token %q, generated %v", name, token, generated)
		case !tt.wantGen && (generated || token != tt.wantToken):
			t.Errorf("%s: token %q, generated %v", name, token, generated)
		}
	}
}

func TestAnEmptyOrSpacedVariableIsTreatedAsNotSet(t *testing.T) {
	_, _, err := ResolveToken(func(string) string { return "   " }, false, false)
	if err == nil || !strings.Contains(err.Error(), "METAGENTE_TOKEN") {
		t.Errorf("err = %v", err)
	}
}
