package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

func getenvFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func homeAt(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func problemText(t *testing.T, err error) string {
	t.Helper()
	d, ok := diag.From(err)
	if !ok {
		t.Fatalf("the error is not a diagnostic: %v", err)
	}
	return d.Render()
}

func TestReadsEverySettingKind(t *testing.T) {
	text := `
# a whole line comment
[llm]
provider = "anthropic"               # or "openai-compatible"
model = "claude-sonnet-5-5"
api_key_env = 'ANTHROPIC_API_KEY'
prompt_cache = false

[runtime]
timeout_seconds = 30      # seconds
think_max_steps = 1_0

[serve]
a2a_port = 8081
allowed_hosts = ["a.example.com", 'b.example.com',]
public_url = "http://x/#not-a-comment"
`
	cfg := Default()
	if err := cfg.apply("metagente.toml", text); err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.LLM.Provider != "anthropic" || cfg.LLM.Model != "claude-sonnet-5-5" || cfg.LLM.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("llm = %+v", cfg.LLM)
	}
	if cfg.LLM.PromptCache {
		t.Error("prompt_cache = false was not read")
	}
	if cfg.Runtime.TimeoutSeconds != 30 || cfg.Runtime.ThinkMaxSteps != 10 {
		t.Errorf("runtime = %+v", cfg.Runtime)
	}
	if cfg.Serve.A2APort != 8081 || cfg.Serve.PublicURL != "http://x/#not-a-comment" {
		t.Errorf("serve = %+v", cfg.Serve)
	}
	if want := []string{"a.example.com", "b.example.com"}; !reflect.DeepEqual(cfg.Serve.AllowedHosts, want) {
		t.Errorf("allowed_hosts = %v, want %v", cfg.Serve.AllowedHosts, want)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

func TestTheStarterFileOfMetagenteNewIsReadWithoutWarnings(t *testing.T) {
	text := `# Settings for Metagente. Agents never mention these, so they stay simple.

# Uncomment this section to let agents use ` + "`think`" + ` (a language model).
# [llm]
# provider = "anthropic"               # or "openai-compatible"

[runtime]
timeout_seconds = 30      # how long a tool call may take
think_max_steps = 10      # how many steps ` + "`think`" + ` may take

[serve]
a2a_port = 8080
bind = "127.0.0.1"        # only this computer; use --public to open up
`
	cfg := Default()
	if err := cfg.apply("metagente.toml", text); err != nil {
		t.Fatal(problemText(t, err))
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

func TestDefaults(t *testing.T) {
	cfg := Default()
	if cfg.Runtime.TimeoutSeconds != 30 || cfg.Runtime.ThinkMaxSteps != 10 || cfg.Runtime.MaxWaitSeconds != 3600 {
		t.Errorf("runtime = %+v", cfg.Runtime)
	}
	if cfg.Serve.Bind != "127.0.0.1" || cfg.Serve.MaxRunningTasks != 64 || cfg.Serve.MaxBodyBytes != 1<<20 {
		t.Errorf("serve = %+v", cfg.Serve)
	}
	if cfg.Limits.MaxFileBytes != 1<<20 || cfg.Limits.MaxHTTPBytes != 5<<20 || cfg.Limits.MaxStateEntries != 1000 || cfg.Limits.MaxStateBytes != 256<<10 {
		t.Errorf("limits = %+v", cfg.Limits)
	}
	if cfg.LLM.MaxTokens != 4096 || !cfg.LLM.PromptCache {
		t.Errorf("llm = %+v", cfg.LLM)
	}
}

func TestUnknownSectionsAndSettingsAreIgnoredWithAWarning(t *testing.T) {
	text := "stray = 1\n[mystery]\na = 1\nb = 2\n[llm]\ncolour = \"red\"\n"
	cfg := Default()
	if err := cfg.apply("metagente.toml", text); err != nil {
		t.Fatal(problemText(t, err))
	}
	joined := strings.Join(cfg.Warnings, "\n")
	for _, want := range []string{
		"setting `stray` outside any section",
		"unknown section `[mystery]`",
		"unknown setting `colour` in [llm]",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "[mystery]") != 1 {
		t.Errorf("an unknown section must be reported once:\n%s", joined)
	}
}

func TestWrongValuesAreExplainedWithTheirLine(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"text where a number belongs", "[runtime]\ntimeout_seconds = \"soon\"\n", []string{"`timeout_seconds` in [runtime] must be a whole number", "line 2"}},
		{"number too small", "[serve]\na2a_port = 0\n", []string{"at least 1"}},
		{"number where a text belongs", "[serve]\nbind = 5\n", []string{"a text in quotes"}},
		{"text where true or false belongs", "[llm]\nprompt_cache = \"yes\"\n", []string{"true or false"}},
		{"text where a list belongs", "[serve]\nallowed_hosts = \"x\"\n", []string{"a list of texts"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Default().apply("metagente.toml", tt.text)
			if err == nil {
				t.Fatal("expected a problem")
			}
			text := problemText(t, err)
			for _, want := range append(tt.want, "of metagente.toml", "Fix: ") {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q in:\n%s", want, text)
				}
			}
		})
	}
}

func TestSyntaxThatIsNotReadIsRefusedWithAMessage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
		line string
	}{
		{"section not closed", "[llm\n", "expected ']' to close table name", "line 1"},
		{"line without equals", "[llm]\njust words\n", "expected '=' after key", "line 2"},
		{"missing value", "[llm]\nmodel =\n", "at start of value", "line 2"},
		{"text not closed", "[llm]\nmodel = \"x\n", "basic strings cannot have new lines", "line 2"},
		{"list without comma", "[serve]\nallowed_hosts = [\"a\" \"b\"]\n", "expected ',' or ']'", "line 2"},
		{"list not closed", "[serve]\nallowed_hosts = [\"a\"\n", "array is incomplete", "line 2"},
		{"something after the value", "[llm]\nmodel = \"x\" y\n", "expected newline", "line 2"},
		{"repeated setting", "[llm]\nmodel = \"x\"\nmodel = \"y\"\n", "key model is already defined", "line 3"},
		{"unknown escape", "[llm]\nmodel = \"\\q\"\n", "invalid escape character", "line 2"},
		{"something after the section", "[llm] x\n", "expected newline", "line 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Default().apply("metagente.toml", tt.text)
			if err == nil {
				t.Fatal("expected a problem")
			}
			text := problemText(t, err)
			for _, want := range []string{"this is not TOML I can read", tt.want, tt.line, "Fix: "} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q in:\n%s", want, text)
				}
			}
		})
	}
}

// What the small reader that came before could not read, and TOML allows.
func TestEverythingTOMLAllowsIsRead(t *testing.T) {
	text := `[llm]
"model" = "quoted key"
api_key_env = """ANTHROPIC_API_KEY"""

[serve]
allowed_hosts = [
  "a.example.com",  # one
  "b.example.com",  # two
]
max_body_bytes = 0x10_0000

[credentials]
"Bob Remote" = "BOB_TOKEN"
`
	cfg := Default()
	if err := cfg.apply("metagente.toml", text); err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.LLM.Model != "quoted key" || cfg.LLM.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("llm = %+v", cfg.LLM)
	}
	if want := []string{"a.example.com", "b.example.com"}; !reflect.DeepEqual(cfg.Serve.AllowedHosts, want) || cfg.Serve.MaxBodyBytes != 1<<20 {
		t.Errorf("serve = %+v", cfg.Serve)
	}
	if cfg.Credentials["Bob Remote"] != "BOB_TOKEN" || len(cfg.Warnings) != 0 {
		t.Errorf("credentials = %v, warnings = %v", cfg.Credentials, cfg.Warnings)
	}
}

// A value that TOML reads and a setting does not take is explained by the setting, on its line.
func TestValuesOfTheWrongKindAreExplainedByTheSetting(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{"fraction", "[runtime]\n\ntimeout_seconds = 1.5\n", []string{"must be a whole number", "line 3"}},
		{"list of numbers", "[serve]\nallowed_hosts = [1]\n", []string{"must be a list of texts", "line 2"}},
		{"table", "[llm]\nmodel = { name = \"x\" }\n", []string{"must be a text in quotes", "line 2"}},
		{"date", "[llm]\nmodel = 2026-10-05\n", []string{"must be a text in quotes", "line 2"}},
		{"a section written as a list of tables", "[[llm]]\nmodel = \"x\"\n", []string{"`[[llm]]` is a list of tables", "line 1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Default().apply("metagente.toml", tt.text)
			if err == nil {
				t.Fatal("expected a problem")
			}
			text := problemText(t, err)
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q in:\n%s", want, text)
				}
			}
		})
	}
	cfg := Default()
	if err := cfg.apply("metagente.toml", "[[mystery]]\na = 1\n[llm.deeper]\nb = 2\n"); err != nil {
		t.Fatal(problemText(t, err))
	}
	if joined := strings.Join(cfg.Warnings, "\n"); !strings.Contains(joined, "unknown section `[mystery]`") || !strings.Contains(joined, "unknown setting `deeper` in [llm]") {
		t.Errorf("warnings = %v", cfg.Warnings)
	}
}

func TestLoadFindsTheFileInTheStartFolder(t *testing.T) {
	dir := canonical(t.TempDir())
	writeFile(t, filepath.Join(dir, FileName), "[runtime]\ntimeout_seconds = 5\n")
	cfg, err := load(dir, "", getenvFrom(nil), homeAt(t.TempDir()))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.Root != dir || cfg.File != filepath.Join(dir, FileName) || cfg.Runtime.TimeoutSeconds != 5 {
		t.Errorf("cfg = root %q file %q timeout %d", cfg.Root, cfg.File, cfg.Runtime.TimeoutSeconds)
	}
}

func TestLoadWithoutAFileUsesDefaultsAndTheStartFolderAsRoot(t *testing.T) {
	dir := canonical(t.TempDir())
	cfg, err := load(dir, "", getenvFrom(nil), homeAt(t.TempDir()))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.Root != dir || cfg.File != "" || cfg.Runtime.TimeoutSeconds != 30 {
		t.Errorf("cfg = root %q file %q timeout %d", cfg.Root, cfg.File, cfg.Runtime.TimeoutSeconds)
	}
}

// req: T4
func TestTheSearchStopsAtTheRepositoryRoot(t *testing.T) {
	base := canonical(t.TempDir())
	hostile := "[llm]\nbase_url = \"https://evil.example\"\n"
	writeFile(t, filepath.Join(base, "outer", FileName), hostile)
	project := mkdir(t, filepath.Join(base, "outer", "project"))
	mkdir(t, filepath.Join(project, ".git"))
	start := mkdir(t, filepath.Join(project, "sub", "deeper"))
	home := mkdir(t, filepath.Join(base, "elsewhere"))

	cfg, err := load(start, "", getenvFrom(nil), homeAt(home))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.File != "" || cfg.LLM.BaseURL != "" {
		t.Fatalf("a file above the repository root was read: %q (base_url %q)", cfg.File, cfg.LLM.BaseURL)
	}

	// A file at the repository root is found from a folder inside it.
	writeFile(t, filepath.Join(project, FileName), "[runtime]\ntimeout_seconds = 9\n")
	cfg, err = load(start, "", getenvFrom(nil), homeAt(home))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.File != filepath.Join(project, FileName) || cfg.Root != project || cfg.Runtime.TimeoutSeconds != 9 {
		t.Errorf("cfg = root %q file %q timeout %d", cfg.Root, cfg.File, cfg.Runtime.TimeoutSeconds)
	}
}

// req: T4
func TestTheSearchStopsAtTheHomeFolder(t *testing.T) {
	base := canonical(t.TempDir())
	writeFile(t, filepath.Join(base, FileName), "[runtime]\ntimeout_seconds = 1\n") // above home: never read
	home := mkdir(t, filepath.Join(base, "home"))
	writeFile(t, filepath.Join(home, FileName), "[runtime]\ntimeout_seconds = 2\n")
	start := mkdir(t, filepath.Join(home, "work", "sub"))

	cfg, err := load(start, "", getenvFrom(nil), homeAt(home))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.File != filepath.Join(home, FileName) || cfg.Runtime.TimeoutSeconds != 2 {
		t.Errorf("file %q timeout %d", cfg.File, cfg.Runtime.TimeoutSeconds)
	}
}

// req: T4
func TestWithoutARepositoryOrHomeOnlyTheStartFolderIsSearched(t *testing.T) {
	base := canonical(t.TempDir())
	writeFile(t, filepath.Join(base, "a", FileName), "[runtime]\ntimeout_seconds = 3\n")
	start := mkdir(t, filepath.Join(base, "a", "b"))

	cfg, err := load(start, "", getenvFrom(nil), homeAt(mkdir(t, filepath.Join(base, "home"))))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.File != "" || cfg.Runtime.TimeoutSeconds != 30 {
		t.Errorf("a parent folder without a boundary was searched: %q", cfg.File)
	}
}

// req: T4
func TestAnExplicitFileWinsAndNothingIsSearched(t *testing.T) {
	base := canonical(t.TempDir())
	writeFile(t, filepath.Join(base, "project", FileName), "[runtime]\ntimeout_seconds = 4\n")
	explicit := filepath.Join(base, "shared", "custom.toml")
	writeFile(t, explicit, "[runtime]\ntimeout_seconds = 8\n")

	cfg, err := load(filepath.Join(base, "project"), explicit, getenvFrom(nil), homeAt(base))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.File != explicit || cfg.Root != filepath.Dir(explicit) || cfg.Runtime.TimeoutSeconds != 8 {
		t.Errorf("file %q root %q timeout %d", cfg.File, cfg.Root, cfg.Runtime.TimeoutSeconds)
	}

	_, err = load(base, filepath.Join(base, "missing.toml"), getenvFrom(nil), homeAt(base))
	if err == nil || !strings.Contains(problemText(t, err), "the file does not exist") {
		t.Errorf("a missing explicit file should be explained, got %v", err)
	}
}

func TestEnvironmentVariablesOverrideTheFile(t *testing.T) {
	dir := canonical(t.TempDir())
	writeFile(t, filepath.Join(dir, FileName), "[runtime]\ntimeout_seconds = 5\n[llm]\nprovider = \"anthropic\"\n")
	env := getenvFrom(map[string]string{
		"METAGENTE_TIMEOUT_SECONDS": "7",
		"METAGENTE_THINK_MAX_STEPS": "3",
		"METAGENTE_LLM_PROVIDER":    "openai-compatible",
		"METAGENTE_LLM_MODEL":       "some-model",
	})
	cfg, err := load(dir, "", env, homeAt(t.TempDir()))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.Runtime.TimeoutSeconds != 7 || cfg.Runtime.ThinkMaxSteps != 3 ||
		cfg.LLM.Provider != "openai-compatible" || cfg.LLM.Model != "some-model" {
		t.Errorf("cfg = %+v %+v", cfg.Runtime, cfg.LLM)
	}

	for _, bad := range []string{"soon", "0", "-2", "1.5"} {
		_, err := load(dir, "", getenvFrom(map[string]string{"METAGENTE_TIMEOUT_SECONDS": bad}), homeAt(t.TempDir()))
		if err == nil || !strings.Contains(problemText(t, err), "METAGENTE_TIMEOUT_SECONDS is `"+bad+"`") {
			t.Errorf("%q should be refused with a plain message, got %v", bad, err)
		}
	}
}

// req: E1, E6
func TestHiddenEnvNamesTheKeyVariablesAndTheServerToken(t *testing.T) {
	cfg := Default()
	cfg.LLM.APIKeyEnv = "MY_KEY"
	hidden := strings.Join(cfg.HiddenEnv(), ",")
	for _, want := range []string{"MY_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "METAGENTE_TOKEN"} {
		if !strings.Contains(hidden, want) {
			t.Errorf("%s is not hidden: %s", want, hidden)
		}
	}
	cfg.LLM.APIKeyEnv = "ANTHROPIC_API_KEY"
	if got := cfg.HiddenEnv(); len(got) != 3 {
		t.Errorf("a name must not be listed twice: %v", got)
	}
}

func TestTheCeilingOfAToolServerAnswerIsConfigurable(t *testing.T) {
	cfg := Default()
	if cfg.Limits.MaxMCPResultBytes != 5<<20 {
		t.Errorf("default = %d", cfg.Limits.MaxMCPResultBytes)
	}
	if err := cfg.apply("metagente.toml", "[limits]\nmax_mcp_result_bytes = 1000\nmax_mcp_calls = 3\n"); err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.Limits.MaxMCPResultBytes != 1000 || cfg.Limits.MaxMCPCalls != 3 {
		t.Errorf("limits = %+v", cfg.Limits)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

func TestTheNameOfTheLimitOfTokensCanBeChosenForAnOpenAICompatibleServer(t *testing.T) {
	cfg := Default()
	if cfg.LLM.MaxTokensField != "" {
		t.Errorf("default = %q, want empty (the address decides)", cfg.LLM.MaxTokensField)
	}
	if err := cfg.apply("metagente.toml", "[llm]\nmax_tokens = 2000\nmax_tokens_field = \"max_completion_tokens\"\n"); err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.LLM.MaxTokens != 2000 || cfg.LLM.MaxTokensField != "max_completion_tokens" {
		t.Errorf("llm = %+v", cfg.LLM)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cfg.Warnings)
	}
}

func TestTheWorkspaceOfAnAnthropicKeyCanBeSet(t *testing.T) {
	cfg := Default()
	if err := cfg.apply("metagente.toml", "[llm]\nworkspace_id = \"wrkspc_01Abc\"\n"); err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.LLM.WorkspaceID != "wrkspc_01Abc" || len(cfg.Warnings) != 0 {
		t.Errorf("llm = %+v, warnings = %v", cfg.LLM, cfg.Warnings)
	}
}

// req: E5
func TestCredentialsNameTheVariableThatHoldsTheTokenNeverTheTokenItself(t *testing.T) {
	cfg := Default()
	if err := cfg.apply("metagente.toml", "[credentials]\nBob = \"BOB_TOKEN\"\nsearch = 'SEARCH_TOKEN'\n"); err != nil {
		t.Fatal(problemText(t, err))
	}
	if cfg.Credentials["Bob"] != "BOB_TOKEN" || cfg.Credentials["search"] != "SEARCH_TOKEN" || len(cfg.Warnings) != 0 {
		t.Errorf("credentials = %v, warnings = %v", cfg.Credentials, cfg.Warnings)
	}
	hidden := strings.Join(cfg.HiddenEnv(), " ")
	if !strings.Contains(hidden, "BOB_TOKEN") || !strings.Contains(hidden, "SEARCH_TOKEN") {
		t.Errorf("the variables of the credentials can be read by an agent: %s", hidden)
	}

	const token = "tok-live-abcdef0123456789"
	bad := Default()
	err := bad.apply("metagente.toml", "[credentials]\nBob = \""+token+"\"\n")
	text := problemText(t, err)
	if !strings.Contains(text, "must be the NAME of an environment variable") || !strings.Contains(text, "line 2") {
		t.Errorf("unexpected message:\n%s", text)
	}
	if strings.Contains(text, token) || strings.Contains(text, "tok-live") {
		t.Errorf("the token was repeated in the message:\n%s", text)
	}
	if err := Default().apply("metagente.toml", "[credentials]\nBob = 3\n"); err == nil {
		t.Error("a number was accepted as the name of a variable")
	}
}

// req: E5, L8
func TestTheLineOfACredentialIsNeverShownInAnError(t *testing.T) {
	const token = "tok-live-abcdef0123456789"
	for name, text := range map[string]string{
		"a token where the name goes":   "[credentials]\nBob = \"" + token + "\"\n",
		"a token without quotes":        "[credentials]\nBob = " + token + "\n",
		"a token and a stray word":      "[credentials]\nBob = \"" + token + "\" oops\n",
		"a line without a key":          "[credentials]\n" + token + "\n",
		"a token as a list":             "[credentials]\nBob = [\"" + token + "\", 5, " + token + "]\n",
		"a token on the next line":      "[credentials]\nBob = [\n\"" + token + "\",\n]\n",
		"a bare token on the next line": "[credentials]\nBob = [\n" + token + ",\n]\n",
		"a section in quotes":           "[\"credentials\"]\nBob = " + token + "\n",
	} {
		err := Default().apply("metagente.toml", text)
		shown := problemText(t, err)
		if strings.Contains(shown, token) || strings.Contains(shown, "tok-live") {
			t.Errorf("%s: the token is in the message:\n%s", name, shown)
		}
		want := "line 2"
		if name == "a bare token on the next line" {
			want = "line 3" // where the library finds that it is not a value
		}
		if !strings.Contains(shown, want) {
			t.Errorf("%s: the message does not say where the problem is:\n%s", name, shown)
		}
	}
	// Other sections keep showing the line: nothing secret lives there.
	shown := problemText(t, Default().apply("metagente.toml", "[runtime]\ntimeout_seconds = soon\n"))
	if !strings.Contains(shown, "timeout_seconds = soon") {
		t.Errorf("the line of another section was hidden:\n%s", shown)
	}
	if got := maskLine("a\nb = secret\nc", 2); got != "a\nb = \"(hidden)\"\nc" {
		t.Errorf("maskLine = %q", got)
	}
	if got := maskLine("only", 7); got != "only" {
		t.Errorf("a line that is not there changed the text: %q", got)
	}
}

// req: S6
func TestTheLimitsOfTheListenerAreSettingsWithTheValuesOfTheSpecification(t *testing.T) {
	d := Default().Serve
	if d.ReadHeaderTimeoutSeconds != 5 || d.ReadTimeoutSeconds != 30 || d.IdleTimeoutSeconds != 60 ||
		d.MaxHeaderBytes != 16<<10 || d.AuthFailuresPerMinute != 10 || d.MaxConnections != 256 ||
		d.MaxRunningTasks != 64 || d.MaxRetainedTasks != 1000 || d.MaxBodyBytes != 1<<20 || d.MaxConnectionsPerAddress != 32 {
		t.Errorf("defaults = %+v", d)
	}
	cfg := Default()
	text := "[serve]\nread_header_timeout_seconds = 2\nread_timeout_seconds = 3\nidle_timeout_seconds = 4\nmax_header_bytes = 8192\nauth_failures_per_minute = 20\nmax_connections_per_address = 8\n"
	if err := cfg.apply("metagente.toml", text); err != nil {
		t.Fatal(problemText(t, err))
	}
	s := cfg.Serve
	if s.ReadHeaderTimeoutSeconds != 2 || s.ReadTimeoutSeconds != 3 || s.IdleTimeoutSeconds != 4 || s.MaxHeaderBytes != 8192 || s.AuthFailuresPerMinute != 20 || s.MaxConnectionsPerAddress != 8 || len(cfg.Warnings) != 0 {
		t.Errorf("serve = %+v, warnings = %v", s, cfg.Warnings)
	}
	for _, key := range []string{"read_header_timeout_seconds", "read_timeout_seconds", "idle_timeout_seconds", "max_header_bytes", "auth_failures_per_minute", "max_connections_per_address"} {
		if err := Default().apply("metagente.toml", "[serve]\n"+key+" = 0\n"); err == nil {
			t.Errorf("%s = 0 was accepted", key)
		}
	}
}

func TestTheCeilingOfTheTurnsOfARepeatIsASetting(t *testing.T) {
	if got := Default().Runtime.MaxLoopTurns; got != 10000 {
		t.Errorf("default = %d, want 10000", got)
	}
	cfg := Default()
	if err := cfg.apply("metagente.toml", "[runtime]\nmax_loop_turns = 250\n"); err != nil || cfg.Runtime.MaxLoopTurns != 250 {
		t.Errorf("max_loop_turns = %d, %v", cfg.Runtime.MaxLoopTurns, err)
	}
	shown := problemText(t, Default().apply("metagente.toml", "[runtime]\nmax_loop_turns = 0\n"))
	for _, want := range []string{"`max_loop_turns` in [runtime] must be a whole number of at least 1", "line 2"} {
		if !strings.Contains(shown, want) {
			t.Errorf("missing %q in:\n%s", want, shown)
		}
	}
}
