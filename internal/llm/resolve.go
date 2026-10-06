package llm

import (
	"strings"

	"metagente/internal/config"
	"metagente/internal/diag"
	"metagente/internal/secret"
)

// The address each provider uses when metagente.toml does not name another one.
const (
	AnthropicBase = "https://api.anthropic.com"
	OpenAIBase    = "https://api.openai.com"
)

// Resolved is the configuration with every default filled in and checked. It
// reads no environment variable and sends nothing.
type Resolved struct {
	Provider string // "anthropic" or "openai-compatible"
	BaseURL  string
	Model    string
	KeyEnv   string
	// WorkspaceID is for the Anthropic keys that are not tied to one workspace.
	WorkspaceID string
}

// Resolve checks the [llm] section.
func Resolve(cfg config.LLM) (*Resolved, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		return nil, diag.New("this agent needs a language model to think, and none is set up").
			Fix("run `metagente new` to create a metagente.toml, or add an [llm] section to it (provider, model and api_key_env)")
	}
	r := &Resolved{BaseURL: strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"), Model: strings.TrimSpace(cfg.Model), KeyEnv: strings.TrimSpace(cfg.APIKeyEnv)}
	// A key written where the name of its variable goes is a common slip. The
	// value is never repeated in the message: it may well be the key itself.
	if r.KeyEnv != "" && !validEnvName(r.KeyEnv) {
		return nil, diag.New("api_key_env must be the NAME of an environment variable, such as ANTHROPIC_API_KEY, not the key itself").
			Fix("put the key in the variable (export ANTHROPIC_API_KEY=...) and write only its name in the [llm] section of metagente.toml")
	}
	if err := r.fillProvider(provider, cfg.Provider); err != nil {
		return nil, err
	}
	if err := r.takeWorkspace(cfg.WorkspaceID); err != nil {
		return nil, err
	}
	if err := checkTokensField(cfg.MaxTokensField); err != nil {
		return nil, err
	}
	if _, err := parseBase(r.BaseURL); err != nil {
		return nil, err
	}
	return r, nil
}

// fillProvider gives the settings that each provider has by itself: its address, the variable of its
// key and, for Anthropic, a model. name is the provider as it was written.
func (r *Resolved) fillProvider(provider, name string) error {
	switch provider {
	case "anthropic":
		r.Provider = "anthropic"
		r.BaseURL = orText(r.BaseURL, AnthropicBase)
		r.KeyEnv = orText(r.KeyEnv, "ANTHROPIC_API_KEY")
		r.Model = orText(r.Model, "claude-sonnet-5-5")
	case "openai", "openai-compatible":
		r.Provider = "openai-compatible"
		r.BaseURL = orText(r.BaseURL, OpenAIBase)
		r.KeyEnv = orText(r.KeyEnv, "OPENAI_API_KEY")
		if r.Model == "" {
			return diag.Newf("the provider `%s` needs a model name", name).
				Fix("add model = \"...\" to the [llm] section of metagente.toml")
		}
	default:
		return diag.Newf("I do not know the language model provider `%s`", name).
			Fix("in metagente.toml set provider to \"anthropic\" or \"openai-compatible\"")
	}
	return nil
}

func orText(text, fallback string) string {
	if text == "" {
		return fallback
	}
	return text
}

// takeWorkspace accepts the workspace of an Anthropic key that is not tied to one.
func (r *Resolved) takeWorkspace(raw string) error {
	id := strings.TrimSpace(raw)
	if id == "" {
		return nil
	}
	switch {
	case r.Provider != "anthropic":
		return diag.New("workspace_id is only used with the provider \"anthropic\"").
			Fix("remove it from the [llm] section of metagente.toml")
	case !validWorkspaceID(id):
		return diag.New("workspace_id is not a workspace identifier").
			Fix("write it like: workspace_id = \"wrkspc_...\" (copy it from the Console, under Settings, Workspaces)")
	}
	r.WorkspaceID = id
	return nil
}

// checkTokensField accepts the two names that the limit of tokens has in the providers, or none.
func checkTokensField(name string) error {
	switch name {
	case "", "max_tokens", "max_completion_tokens":
		return nil
	}
	return diag.Newf("`%s` is not a name the limit of tokens can have", name).
		Fix("in the [llm] section use max_tokens_field = \"max_tokens\" or \"max_completion_tokens\"")
}

// DefaultBase is the address a provider uses by itself.
func DefaultBase(provider string) string {
	if strings.EqualFold(provider, "anthropic") {
		return AnthropicBase
	}
	return OpenAIBase
}

// NonDefaultBase returns the address that the key will go to when it is not the
// one the provider uses by itself. Such an address needs the approval of the
// person (requirements T1 and T3).
func NonDefaultBase(cfg config.LLM) (string, bool) {
	r, err := Resolve(cfg)
	if err != nil {
		return "", false
	}
	if strings.EqualFold(r.BaseURL, DefaultBase(r.Provider)) {
		return "", false
	}
	return r.BaseURL, true
}

// SendsNoKey is true when the requests to the model go without a key: none is set, and the address is of
// this same computer. It is the one case in which FromConfig lets a model be used with no key.
func SendsNoKey(cfg config.LLM, getenv func(string) string) bool {
	r, err := Resolve(cfg)
	if err != nil || r.Provider == "anthropic" {
		return false
	}
	if strings.TrimSpace(getenv(r.KeyEnv)) != "" {
		return false
	}
	u, _ := parseBase(r.BaseURL)
	return u != nil && isLoopback(u.Hostname())
}

// FromConfig builds the model. The key is read now, from the variable whose
// name is in the configuration; it is never written anywhere.
func FromConfig(cfg config.LLM, getenv func(string) string) (Llm, error) {
	r, err := Resolve(cfg)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(getenv(r.KeyEnv))
	// A server on this same computer often needs no key.
	if key == "" && !SendsNoKey(cfg, getenv) {
		return nil, diag.Newf("the language model could not answer: the variable %s is not set", r.KeyEnv).
			Fixf("set it in the terminal that runs Metagente, for example: export %s=...", r.KeyEnv)
	}
	if what := notInAKey(key); what != "" {
		// Sent as it is, the key would be refused by the HTTP library before any connection,
		// and the person would be told that the model could not be reached.
		return nil, diag.Newf("the language model could not answer: the variable %s holds %s, which cannot be part of a key", r.KeyEnv, what).
			Fix("put in it only the key, one word with no spaces or line breaks; if it came from a file or the clipboard, more than the key may have been copied")
	}
	settings := Settings{BaseURL: r.BaseURL, Model: r.Model, APIKey: key, MaxTokensField: cfg.MaxTokensField, WorkspaceID: r.WorkspaceID}
	if r.Provider == "anthropic" {
		return NewAnthropic(settings)
	}
	return NewOpenAI(settings)
}

// notInAKey names the first character of key that no key of a provider has, or is "" when there is
// none. It never says which character it was, or where, so that nothing of the key is told.
func notInAKey(key string) string {
	for _, c := range key {
		switch {
		case c == '\n' || c == '\r':
			return "a line break"
		case c == ' ' || c == '\t':
			return "a space"
		case c < 0x20 || c == 0x7f:
			return "a control character"
		case c > 0x7e:
			return "a character that is not ASCII"
		}
	}
	return ""
}

// validEnvName reports whether text can be the name of an environment variable.
func validEnvName(text string) bool { return secret.ValidEnvName(text) }

// validWorkspaceID accepts the characters an identifier has, and nothing that
// could end a header line.
func validWorkspaceID(text string) bool {
	if text == "" || len(text) > 120 {
		return false
	}
	for _, c := range text {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}
