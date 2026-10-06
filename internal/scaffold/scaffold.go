// Package scaffold implements `metagente new`: a starter agent and a starter
// configuration.
package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

const config = `# Settings for Metagente. Agents never mention these, so they stay simple.

# Uncomment this section to let agents use ` + "`think`" + ` (a language model).
# [llm]
# provider = "anthropic"               # or "openai-compatible"
# model = "claude-sonnet-5-5"          # the model name your provider gives you
# api_key_env = "ANTHROPIC_API_KEY"    # the NAME of the variable that holds your key
# workspace_id = "wrkspc_..."           # only for Anthropic keys that are not tied to one workspace

# Tokens for the remote agents and the tool servers (addresses) that need one.
# On the left, the name the .ag file gives them; on the right, the NAME of the
# variable that holds the token, never the token itself.
# [credentials]
# Bob = "BOB_TOKEN"

[runtime]
timeout_seconds = 30      # how long a tool call may take
think_max_steps = 10      # how many steps ` + "`think`" + ` may take

[serve]
a2a_port = 8080
bind = "127.0.0.1"        # only this computer; use --public to open up
`

// agentName turns what the person typed into a valid agent name: letters,
// digits and underscores, starting with an upper case letter. Anything that
// cannot start a name becomes "Helper".
func agentName(name string) string {
	var cleaned []rune
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
			cleaned = append(cleaned, r)
		}
	}
	if len(cleaned) == 0 || !unicode.IsLetter(cleaned[0]) {
		return "Helper"
	}
	return strings.ToUpper(string(cleaned[:1])) + string(cleaned[1:])
}

// Create writes `<name>.ag` and, when it is missing, `metagente.toml` in dir.
// It returns the lines to show the person.
func Create(dir, name string) ([]string, error) {
	agent := agentName(name)
	fileName := strings.ToLower(agent) + ".ag"
	target := filepath.Join(dir, fileName)
	if _, err := os.Stat(target); err == nil {
		return nil, diag.Newf("%s already exists", fileName).
			Fix("choose another name, or delete the old file first")
	}
	source := "agent " + agent + "\n" +
		"  goal \"Say hello to someone\"\n" +
		"  accepts greet name\n" +
		"  on greet\n" +
		"    reply \"Hello, {name}!\"\n"
	if err := os.WriteFile(target, []byte(source), 0o644); err != nil {
		return nil, diag.Newf("I could not create %s: %v", fileName, err).
			Fix("check that you may write in this folder")
	}
	messages := []string{"Created " + fileName}

	configPath := filepath.Join(dir, "metagente.toml")
	if _, err := os.Stat(configPath); err != nil {
		if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
			return nil, diag.Newf("I could not create metagente.toml: %v", err).
				Fix("check that you may write in this folder")
		}
		messages = append(messages, "Created metagente.toml")
	}
	messages = append(messages, "Try it: metagente run "+fileName+" greet name=World")
	return messages, nil
}
