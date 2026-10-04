package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicVersion is the version of the Messages API this client speaks.
const anthropicVersion = "2023-06-01"

type anthropic struct {
	settings Settings
	address  string
	tr       *transport
}

// NewAnthropic returns a client of the Anthropic Messages API.
func NewAnthropic(s Settings) (Llm, error) {
	u, err := parseBase(s.BaseURL)
	if err != nil {
		return nil, err
	}
	address := strings.TrimRight(u.String(), "/")
	if strings.HasSuffix(u.Path, "/v1") {
		address += "/messages"
	} else {
		address += "/v1/messages"
	}
	return &anthropic{settings: s, address: address, tr: newTransport(s, u)}, nil
}

func (a *anthropic) Complete(ctx context.Context, req *Request) (*Response, error) {
	body, err := json.Marshal(a.body(req))
	if err != nil {
		return nil, &Error{Message: "I could not write the question for the language model"}
	}
	raw, err := a.tr.post(ctx, a.address, map[string]string{
		"x-api-key":              a.settings.APIKey,
		"anthropic-version":      anthropicVersion,
		"anthropic-workspace-id": a.settings.WorkspaceID, // empty: not sent
	}, body)
	if err != nil {
		return nil, err
	}
	return a.parse(raw)
}

func (a *anthropic) body(req *Request) map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, map[string]any{"role": string(m.Role), "content": anthropicBlocks(m.Parts)})
	}
	if req.Cache && len(messages) > 0 {
		// The start of the conversation is the same at every step, so the
		// provider can keep it. A mark on the last block keeps all that comes before.
		last := messages[len(messages)-1]["content"].([]map[string]any)
		if len(last) > 0 {
			last[len(last)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
		}
	}
	body := map[string]any{
		"model":      a.settings.Model,
		"max_tokens": req.MaxTokens,
		"messages":   messages,
	}
	if req.System != "" {
		system := map[string]any{"type": "text", "text": req.System}
		if req.Cache {
			system["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		body["system"] = []map[string]any{system}
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Schema}
		}
		body["tools"] = tools
	}
	return body
}

func anthropicBlocks(parts []Part) []map[string]any {
	blocks := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case PartText:
			if p.Text == "" {
				continue // the API refuses a text block with nothing in it
			}
			blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
		case PartToolUse:
			input := p.Input
			if input == nil {
				input = map[string]any{}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": p.ID, "name": p.Name, "input": input})
		case PartToolResult:
			block := map[string]any{"type": "tool_result", "tool_use_id": p.ID, "content": p.Text}
			if p.IsError {
				block["is_error"] = true
			}
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func (a *anthropic) parse(raw []byte) (*Response, error) {
	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			Input      int `json:"input_tokens"`
			Output     int `json:"output_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Message: fmt.Sprintf("%s did not answer with readable data", a.tr.host)}
	}
	resp := &Response{
		Reason: out.StopReason,
		Usage:  Usage{Input: out.Usage.Input, Output: out.Usage.Output, CacheRead: out.Usage.CacheRead, CacheWrite: out.Usage.CacheWrite},
	}
	for _, block := range out.Content {
		switch block.Type {
		case "text":
			resp.Parts = append(resp.Parts, Part{Kind: PartText, Text: block.Text})
		case "tool_use":
			part := Part{Kind: PartToolUse, ID: block.ID, Name: block.Name}
			var input map[string]any
			if len(block.Input) == 0 || json.Unmarshal(block.Input, &input) != nil {
				part.BadInput = len(block.Input) != 0
			}
			part.Input = input
			resp.Parts = append(resp.Parts, part)
		}
	}
	switch out.StopReason {
	case "end_turn", "stop_sequence":
		resp.Stop = StopEnd
	case "tool_use":
		resp.Stop = StopToolUse
	case "max_tokens", "model_context_window_exceeded":
		resp.Stop = StopLength
	case "refusal":
		resp.Stop = StopRefusal
	default:
		resp.Stop = StopOther
	}
	return resp, nil
}
