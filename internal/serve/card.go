package serve

import (
	"encoding/json"
	"net/http"
	"strings"
)

// mediaJSON is the type of everything this server sends and takes in JSON-RPC.
const mediaJSON = "application/json"

// card is the Agent Card of an agent, in the shape of A2A 1.0.
//
// There are two. The full one says what the agent is for and what each message
// takes, and is given only with the token. The minimal one, given to anyone when
// the person chose `--public-card`, says only the name and the names of the
// messages: enough to find the agent, not enough to learn how to talk to it.
// Both say that a token is needed, and that nothing is streamed or pushed.
func card(agent Agent, endpoint, version string, full bool) map[string]any {
	skills := make([]map[string]any, 0, len(agent.Skills()))
	for _, skill := range agent.Skills() {
		description := "Message `" + skill.ID + "`."
		if full {
			description = describe(skill)
		}
		skills = append(skills, map[string]any{
			"id":          skill.ID,
			"name":        skill.ID,
			"description": description,
			"tags":        []string{skill.ID}, // A2A 1.0 asks for the tags of a skill; its name is the honest one
		})
	}
	description := "A Metagente agent."
	if full && agent.Goal() != "" {
		description = agent.Goal()
	}
	return map[string]any{
		"name":        agent.Name(),
		"description": description,
		"version":     version,
		"supportedInterfaces": []map[string]any{{
			"url":             endpoint,
			"protocolBinding": "JSONRPC",
			"protocolVersion": "1.0",
		}},
		"capabilities":         map[string]any{"streaming": false, "pushNotifications": false},
		"securitySchemes":      map[string]any{"bearer": map[string]any{"httpAuthSecurityScheme": map[string]any{"scheme": "Bearer"}}},
		"securityRequirements": []map[string]any{{"schemes": map[string]any{"bearer": map[string]any{"list": []string{}}}}},
		"defaultInputModes":    []string{mediaJSON, "text/plain"},
		"defaultOutputModes":   []string{mediaJSON, "text/plain"},
		"skills":               skills,
	}
}

// describe says what a message does and which values it takes, in words a person
// or a model can use: a card names the messages, not their values.
func describe(skill Skill) string {
	text := strings.TrimSpace(skill.Description)
	if text == "" {
		text = "Message `" + skill.ID + "`."
	}
	if len(skill.Params) > 0 {
		text += " Takes: " + strings.Join(skill.Params, ", ") + "."
	}
	return text
}

func writeJSON(w http.ResponseWriter, status int, document any) {
	raw, err := json.Marshal(document)
	if err != nil {
		refuse(w, http.StatusInternalServerError, "something went wrong inside the server")
		return
	}
	w.Header().Set("Content-Type", mediaJSON)
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
