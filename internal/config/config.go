// Package config holds the settings that live outside agent code: the
// metagente.toml file and a few environment variables. Agents never mention
// these, so they stay simple.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"metagente/internal/diag"
	"metagente/internal/secret"
)

// FileName is the name of the configuration file.
const FileName = "metagente.toml"

// LLM is the [llm] section.
type LLM struct {
	Provider  string
	Model     string
	APIKeyEnv string
	BaseURL   string
	MaxTokens int // L1
	// MaxTokensField is the name of the limit in requests to an OpenAI compatible
	// server: "max_tokens" or "max_completion_tokens". Empty chooses by address.
	MaxTokensField string
	// WorkspaceID is the Anthropic workspace a request acts in. Only keys that
	// are not tied to one workspace need it; it is an identifier, not a secret.
	WorkspaceID string
	PromptCache bool // L4
}

// Runtime is the [runtime] section.
type Runtime struct {
	TimeoutSeconds      int // how long a tool call may take
	ThinkMaxSteps       int
	ThinkMaxParallel    int // L2
	ThinkMaxTotalTokens int // L5
	ThinkTimeoutSeconds int // L5
	MaxCallDepth        int // D2
	MaxWaitSeconds      int // D1
}

// Serve is the [serve] section.
type Serve struct {
	A2APort              int
	Bind                 string
	PublicURL            string   // S8
	AllowedHosts         []string // S3, S5
	AllowedOrigins       []string // S4
	TaskRetentionSeconds int      // S9
	MaxConnections       int      // S6
	MaxRunningTasks      int      // S6
	MaxRetainedTasks     int      // S6
	MaxBodyBytes         int64    // S6
	// The timeouts and the limits of the listener, and how many wrong tokens a place may try.
	ReadHeaderTimeoutSeconds int // S6
	ReadTimeoutSeconds       int // S6
	IdleTimeoutSeconds       int // S6
	MaxHeaderBytes           int // S6
	AuthFailuresPerMinute    int // S6
}

// Limits is the [limits] section: ceilings that keep one agent from using all
// the memory of the machine.
type Limits struct {
	MaxFileBytes       int64 // F2
	MaxHTTPBytes       int64 // H3
	MaxToolResultBytes int64 // L3
	MaxMCPCalls        int   // E3
	MaxMCPResultBytes  int64 // what one tool server answer may hold
	MaxStateEntries    int   // D3
	MaxStateBytes      int64 // D3
}

// Config is everything configurable.
type Config struct {
	LLM     LLM
	Runtime Runtime
	Serve   Serve
	Limits  Limits
	// Root is the project folder: where metagente.toml was found, or where
	// Metagente was started.
	Root string
	// File is the path of the metagente.toml that was read, if any.
	File string
	// Credentials say, for each remote agent or tool server by the name the agent
	// file gives it, the NAME of the environment variable that holds its bearer
	// token. The token itself is never in a file (requirement E5).
	Credentials map[string]string
	// Warnings are plain-language notes, for example about unknown settings.
	Warnings []string
}

// Default returns the configuration used when nothing is set.
func Default() *Config {
	return &Config{
		Credentials: map[string]string{},
		LLM:         LLM{MaxTokens: 4096, PromptCache: true},
		Runtime: Runtime{
			TimeoutSeconds:      30,
			ThinkMaxSteps:       10,
			ThinkMaxParallel:    4,
			ThinkMaxTotalTokens: 200000,
			ThinkTimeoutSeconds: 300,
			MaxCallDepth:        8,
			MaxWaitSeconds:      3600,
		},
		Serve: Serve{
			A2APort:                  8080,
			Bind:                     "127.0.0.1",
			TaskRetentionSeconds:     600,
			MaxConnections:           256,
			MaxRunningTasks:          64,
			MaxRetainedTasks:         1000,
			MaxBodyBytes:             1 << 20,
			ReadHeaderTimeoutSeconds: 5,
			ReadTimeoutSeconds:       30,
			IdleTimeoutSeconds:       60,
			MaxHeaderBytes:           16 << 10,
			AuthFailuresPerMinute:    10,
		},
		Limits: Limits{
			MaxFileBytes:       1 << 20,
			MaxHTTPBytes:       5 << 20,
			MaxToolResultBytes: 32 << 10,
			MaxMCPCalls:        8,
			MaxMCPResultBytes:  5 << 20,
			MaxStateEntries:    1000,
			MaxStateBytes:      256 << 10, // with 1000 conversations, about 250 MiB at most
		},
	}
}

// setter stores one value in the configuration, or says what it expected.
type setter func(c *Config, v any) error

func setString(field func(*Config) *string) setter {
	return func(c *Config, v any) error {
		s, ok := v.(string)
		if !ok {
			return errors.New("a text in quotes")
		}
		*field(c) = s
		return nil
	}
}

func setStrings(field func(*Config) *[]string) setter {
	return func(c *Config, v any) error {
		list, ok := v.([]string)
		if !ok {
			return errors.New("a list of texts, like [\"a\", \"b\"]")
		}
		*field(c) = list
		return nil
	}
}

func setBool(field func(*Config) *bool) setter {
	return func(c *Config, v any) error {
		b, ok := v.(bool)
		if !ok {
			return errors.New("true or false")
		}
		*field(c) = b
		return nil
	}
}

func atLeast(n int64, least int64) error {
	if n < least {
		return fmt.Errorf("a whole number of at least %d", least)
	}
	return nil
}

func setInt(field func(*Config) *int, least int) setter {
	return func(c *Config, v any) error {
		n, ok := v.(int64)
		if !ok {
			return errors.New("a whole number")
		}
		if err := atLeast(n, int64(least)); err != nil {
			return err
		}
		*field(c) = int(n)
		return nil
	}
}

func setInt64(field func(*Config) *int64, least int64) setter {
	return func(c *Config, v any) error {
		n, ok := v.(int64)
		if !ok {
			return errors.New("a whole number")
		}
		if err := atLeast(n, least); err != nil {
			return err
		}
		*field(c) = n
		return nil
	}
}

// settings lists every known `section.key`.
var settings = map[string]setter{
	"llm.provider":         setString(func(c *Config) *string { return &c.LLM.Provider }),
	"llm.model":            setString(func(c *Config) *string { return &c.LLM.Model }),
	"llm.api_key_env":      setString(func(c *Config) *string { return &c.LLM.APIKeyEnv }),
	"llm.base_url":         setString(func(c *Config) *string { return &c.LLM.BaseURL }),
	"llm.max_tokens":       setInt(func(c *Config) *int { return &c.LLM.MaxTokens }, 1),
	"llm.max_tokens_field": setString(func(c *Config) *string { return &c.LLM.MaxTokensField }),
	"llm.workspace_id":     setString(func(c *Config) *string { return &c.LLM.WorkspaceID }),
	"llm.prompt_cache":     setBool(func(c *Config) *bool { return &c.LLM.PromptCache }),

	"runtime.timeout_seconds":        setInt(func(c *Config) *int { return &c.Runtime.TimeoutSeconds }, 1),
	"runtime.think_max_steps":        setInt(func(c *Config) *int { return &c.Runtime.ThinkMaxSteps }, 1),
	"runtime.think_max_parallel":     setInt(func(c *Config) *int { return &c.Runtime.ThinkMaxParallel }, 1),
	"runtime.think_max_total_tokens": setInt(func(c *Config) *int { return &c.Runtime.ThinkMaxTotalTokens }, 1),
	"runtime.think_timeout_seconds":  setInt(func(c *Config) *int { return &c.Runtime.ThinkTimeoutSeconds }, 1),
	"runtime.max_call_depth":         setInt(func(c *Config) *int { return &c.Runtime.MaxCallDepth }, 1),
	"runtime.max_wait_seconds":       setInt(func(c *Config) *int { return &c.Runtime.MaxWaitSeconds }, 1),

	"serve.a2a_port":                    setInt(func(c *Config) *int { return &c.Serve.A2APort }, 1),
	"serve.bind":                        setString(func(c *Config) *string { return &c.Serve.Bind }),
	"serve.public_url":                  setString(func(c *Config) *string { return &c.Serve.PublicURL }),
	"serve.allowed_hosts":               setStrings(func(c *Config) *[]string { return &c.Serve.AllowedHosts }),
	"serve.allowed_origins":             setStrings(func(c *Config) *[]string { return &c.Serve.AllowedOrigins }),
	"serve.task_retention_seconds":      setInt(func(c *Config) *int { return &c.Serve.TaskRetentionSeconds }, 1),
	"serve.max_connections":             setInt(func(c *Config) *int { return &c.Serve.MaxConnections }, 1),
	"serve.max_running_tasks":           setInt(func(c *Config) *int { return &c.Serve.MaxRunningTasks }, 1),
	"serve.max_retained_tasks":          setInt(func(c *Config) *int { return &c.Serve.MaxRetainedTasks }, 1),
	"serve.max_body_bytes":              setInt64(func(c *Config) *int64 { return &c.Serve.MaxBodyBytes }, 1),
	"serve.read_header_timeout_seconds": setInt(func(c *Config) *int { return &c.Serve.ReadHeaderTimeoutSeconds }, 1),
	"serve.read_timeout_seconds":        setInt(func(c *Config) *int { return &c.Serve.ReadTimeoutSeconds }, 1),
	"serve.idle_timeout_seconds":        setInt(func(c *Config) *int { return &c.Serve.IdleTimeoutSeconds }, 1),
	"serve.max_header_bytes":            setInt(func(c *Config) *int { return &c.Serve.MaxHeaderBytes }, 1),
	"serve.auth_failures_per_minute":    setInt(func(c *Config) *int { return &c.Serve.AuthFailuresPerMinute }, 1),

	"limits.max_file_bytes":        setInt64(func(c *Config) *int64 { return &c.Limits.MaxFileBytes }, 1),
	"limits.max_http_bytes":        setInt64(func(c *Config) *int64 { return &c.Limits.MaxHTTPBytes }, 1),
	"limits.max_tool_result_bytes": setInt64(func(c *Config) *int64 { return &c.Limits.MaxToolResultBytes }, 1),
	"limits.max_mcp_calls":         setInt(func(c *Config) *int { return &c.Limits.MaxMCPCalls }, 1),
	"limits.max_mcp_result_bytes":  setInt64(func(c *Config) *int64 { return &c.Limits.MaxMCPResultBytes }, 1),
	"limits.max_state_entries":     setInt(func(c *Config) *int { return &c.Limits.MaxStateEntries }, 1),
	"limits.max_state_bytes":       setInt64(func(c *Config) *int64 { return &c.Limits.MaxStateBytes }, 1),
}

var knownSections = map[string]bool{"llm": true, "runtime": true, "serve": true, "limits": true, "credentials": true}

// Load reads the configuration for a project started in the folder start.
// When explicit is not empty, that file is read and nothing is searched.
// Otherwise the search follows requirement T4: metagente.toml is looked for in
// start and in the folders above it, but never above the root of the
// repository (the folder that has `.git`) or the home folder of the user,
// whichever comes first. A hostile file in some parent folder is never read.
func Load(start, explicit string) (*Config, error) {
	return load(start, explicit, os.Getenv, os.UserHomeDir)
}

func canonical(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func load(start, explicit string, getenv func(string) string, home func() (string, error)) (*Config, error) {
	cfg := Default()
	cfg.Root = canonical(start)

	path := ""
	if explicit != "" {
		path = canonical(explicit)
	} else {
		homeDir := ""
		if h, err := home(); err == nil {
			homeDir = canonical(h)
		}
		path = find(cfg.Root, homeDir)
	}
	if path != "" {
		text, err := os.ReadFile(path)
		if err != nil {
			why := err.Error()
			if os.IsNotExist(err) {
				why = "the file does not exist"
			}
			return nil, diag.Newf("I could not read %s: %s", path, why).
				Fix("check that the file exists and you are allowed to read it")
		}
		cfg.Root = filepath.Dir(path)
		cfg.File = path
		if err := cfg.apply(filepath.Base(path), string(text)); err != nil {
			return nil, err
		}
	}
	if err := cfg.applyEnv(getenv); err != nil {
		return nil, err
	}
	return cfg, nil
}

// find looks for metagente.toml from start up to the boundary of T4.
func find(start, homeDir string) string {
	var chain []string
	for dir := start; ; {
		chain = append(chain, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	boundary := 0
	for i, dir := range chain {
		if dir == homeDir {
			boundary = i
			break
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			boundary = i
			break
		}
	}
	for _, dir := range chain[:boundary+1] {
		candidate := filepath.Join(dir, FileName)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

// apply reads the text of a configuration file into cfg.
func (cfg *Config) apply(name, text string) error {
	entries, err := parseTOML(name, text)
	if err != nil {
		return err
	}
	warnedSection := map[string]bool{}
	for _, e := range entries {
		id := e.section + "." + e.key
		set, known := settings[id]
		switch {
		case e.section == "":
			cfg.Warnings = append(cfg.Warnings,
				fmt.Sprintf("%s has the setting `%s` outside any section; I ignored it", FileName, e.key))
			continue
		case e.section == "credentials":
			// The keys are the names an agent file gives its remote agents and tool
			// servers, so they are not in the table. The value is never repeated: it
			// is probably the token itself.
			variable, _ := e.value.(string)
			if !secret.ValidEnvName(variable) {
				return diag.Newf("`%s` in [credentials] must be the NAME of an environment variable, such as BOB_TOKEN, not the token itself", e.key).
					At(name, e.line, 1).WithSource(maskLine(text, e.line)).
					Fix("put the token in the variable (export BOB_TOKEN=...) and write only its name here")
			}
			cfg.Credentials[e.key] = variable
			continue
		case !knownSections[e.section]:
			if !warnedSection[e.section] {
				warnedSection[e.section] = true
				cfg.Warnings = append(cfg.Warnings,
					fmt.Sprintf("%s has an unknown section `[%s]`; I ignored it", FileName, e.section))
			}
			continue
		case !known:
			cfg.Warnings = append(cfg.Warnings,
				fmt.Sprintf("%s has an unknown setting `%s` in [%s]; I ignored it", FileName, e.key, e.section))
			continue
		}
		if err := set(cfg, e.value); err != nil {
			return diag.Newf("`%s` in [%s] must be %s", e.key, e.section, err.Error()).
				At(name, e.line, 1).WithSource(text).
				Fix("change the value in " + FileName)
		}
	}
	return nil
}

// applyEnv applies the environment variables that override the file.
func (cfg *Config) applyEnv(getenv func(string) string) error {
	if v := getenv("METAGENTE_LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := getenv("METAGENTE_LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	for _, o := range []struct {
		name  string
		field *int
		fix   string
	}{
		{"METAGENTE_TIMEOUT_SECONDS", &cfg.Runtime.TimeoutSeconds, "set it to a number of seconds, for example 30"},
		{"METAGENTE_THINK_MAX_STEPS", &cfg.Runtime.ThinkMaxSteps, "set it to a number, for example 10"},
	} {
		v := getenv(o.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 {
			return diag.Newf("%s is `%s`, which is not a whole number of at least 1", o.name, v).Fix(o.fix)
		}
		*o.field = n
	}
	return nil
}

// HiddenEnv lists the environment variables that agents and tool servers may
// never read: the one that holds the key of the language model, the usual
// names of such keys, and the token of the server (requirements E1 and E6).
func (cfg *Config) HiddenEnv() []string {
	hidden := []string{}
	if cfg.LLM.APIKeyEnv != "" {
		hidden = append(hidden, cfg.LLM.APIKeyEnv)
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "METAGENTE_TOKEN"} {
		if name != cfg.LLM.APIKeyEnv {
			hidden = append(hidden, name)
		}
	}
	// A credential is for the connection it was given to, not for an agent to
	// read and send elsewhere.
	variables := make([]string, 0, len(cfg.Credentials))
	for _, variable := range cfg.Credentials {
		variables = append(variables, variable)
	}
	sort.Strings(variables)
	return append(hidden, variables...)
}
