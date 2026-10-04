package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type openAI struct {
	settings Settings
	address  string
	field    string
	tr       *transport
}

// NewOpenAI returns a client of the OpenAI chat format, which many servers
// speak: OpenAI itself, and also the ones you run yourself.
func NewOpenAI(s Settings) (Llm, error) {
	u, err := parseBase(s.BaseURL)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(u.String(), "/")
	address := base + "/v1/chat/completions"
	if strings.HasSuffix(u.Path, "/v1") {
		address = base + "/chat/completions"
	}
	return &openAI{settings: s, address: address, field: maxTokensField(s.MaxTokensField, u), tr: newTransport(s, u)}, nil
}

// maxTokensField chooses the name of the limit. OpenAI retired `max_tokens` and
// its newer models refuse it; the servers people run themselves still expect it.
func maxTokensField(configured string, u *url.URL) string {
	if configured != "" {
		return configured
	}
	host := u.Hostname()
	if host == "api.openai.com" || strings.HasSuffix(host, ".openai.azure.com") {
		return "max_completion_tokens"
	}
	return "max_tokens"
}

func (o *openAI) Complete(ctx context.Context, req *Request) (*Response, error) {
	body, err := json.Marshal(o.body(req))
	if err != nil {
		return nil, &Error{Message: "I could not write the question for the language model"}
	}
	headers := map[string]string{}
	if o.settings.APIKey != "" {
		headers["Authorization"] = "Bearer " + o.settings.APIKey
	}
	raw, err := o.tr.post(ctx, o.address, headers, body)
	if err != nil {
		return nil, err
	}
	return o.parse(raw)
}

func (o *openAI) body(req *Request) map[string]any {
	var messages []map[string]any
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		messages = append(messages, openAIMessages(m)...)
	}
	body := map[string]any{"model": o.settings.Model, "messages": messages}
	if req.MaxTokens > 0 {
		body[o.field] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Schema,
			}}
		}
		body["tools"] = tools
	}
	return body
}

// openAIMessages turns one message into the messages of the chat format. A tool
// result is a message of its own, and the format has no place to say that it is
// an error, so the content says it.
func openAIMessages(m Message) []map[string]any {
	if m.Role == RoleAssistant {
		return []map[string]any{openAIAssistant(m)}
	}
	return openAIUser(m)
}

// openAIAssistant is a message of the model: its text, and the tools it asked for.
func openAIAssistant(m Message) map[string]any {
	var text strings.Builder
	var calls []map[string]any
	for _, p := range m.Parts {
		switch p.Kind {
		case PartText:
			text.WriteString(p.Text)
		case PartToolUse:
			calls = append(calls, openAIToolCall(p))
		}
	}
	message := map[string]any{"role": "assistant", "content": text.String()}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	return message
}

func openAIToolCall(p Part) map[string]any {
	input := p.Input
	if input == nil {
		input = map[string]any{}
	}
	arguments, _ := json.Marshal(input)
	return map[string]any{"id": p.ID, "type": "function",
		"function": map[string]any{"name": p.Name, "arguments": string(arguments)}}
}

// openAIUser is what the person says, and the results of the tools, each one in a message of its own.
func openAIUser(m Message) []map[string]any {
	var out []map[string]any
	var text strings.Builder
	for _, p := range m.Parts {
		switch p.Kind {
		case PartText:
			text.WriteString(p.Text)
		case PartToolResult:
			content := p.Text
			if p.IsError {
				content = "error: " + content
			}
			out = append(out, map[string]any{"role": "tool", "tool_call_id": p.ID, "content": content})
		}
	}
	if text.Len() > 0 {
		out = append(out, map[string]any{"role": "user", "content": text.String()})
	}
	return out
}

// openAIAnswer is the part of an answer of the chat format that is used.
type openAIAnswer struct {
	Choices []struct {
		Message struct {
			Content   json.RawMessage      `json:"content"`
			Refusal   string               `json:"refusal"`
			ToolCalls []openAIToolCallData `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		Prompt     int `json:"prompt_tokens"`
		Completion int `json:"completion_tokens"`
		Details    struct {
			Cached int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

type openAIToolCallData struct {
	ID       string `json:"id"`
	Function struct {
		Name string `json:"name"`
		// The format says a text holding JSON; some servers send the object.
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func (o *openAI) parse(raw []byte) (*Response, error) {
	var out openAIAnswer
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Message: fmt.Sprintf("%s did not answer with readable data", o.tr.host)}
	}
	if len(out.Choices) == 0 {
		return nil, &Error{Message: fmt.Sprintf("%s answered without a message", o.tr.host)}
	}
	choice := out.Choices[0]
	resp := &Response{
		Reason: choice.FinishReason,
		Usage:  Usage{Input: out.Usage.Prompt, Output: out.Usage.Completion, CacheRead: out.Usage.Details.Cached},
	}
	if text := contentText(choice.Message.Content); text != "" {
		resp.Parts = append(resp.Parts, Part{Kind: PartText, Text: text})
	}
	for _, call := range choice.Message.ToolCalls {
		resp.Parts = append(resp.Parts, toolUseOf(call))
	}
	resp.Stop = stopOf(choice.FinishReason)
	if choice.Message.Refusal != "" && len(resp.Parts) == 0 {
		resp.Stop = StopRefusal
	}
	// Some servers say "stop" with tool calls in the answer. The calls are what counts.
	if len(resp.ToolUses()) > 0 && resp.Stop == StopEnd {
		resp.Stop = StopToolUse
	}
	return resp, nil
}

// toolUseOf is a request for a tool. Its arguments come as a text that holds JSON or, from some
// servers, as the object itself; arguments that are neither an object nor empty are marked, so the
// model is told what it got wrong instead of the tool being run with nothing.
func toolUseOf(call openAIToolCallData) Part {
	part := Part{Kind: PartToolUse, ID: call.ID, Name: call.Function.Name}
	var input map[string]any
	arguments := strings.TrimSpace(string(call.Function.Arguments))
	var asText string
	if json.Unmarshal(call.Function.Arguments, &asText) == nil {
		arguments = strings.TrimSpace(asText)
	}
	if arguments == "" || arguments == "null" {
		arguments = "{}"
	}
	if json.Unmarshal([]byte(arguments), &input) != nil || input == nil {
		part.BadInput = true
		input = nil
	}
	part.Input = input
	return part
}

// stopOf is the reason to stop that a server gave, in the words of this package.
func stopOf(finishReason string) Stop {
	switch finishReason {
	case "stop":
		return StopEnd
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopLength
	case "content_filter":
		return StopRefusal
	}
	return StopOther
}

// contentText reads the content of a message, which is a text, null, or a list
// of parts depending on the server.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}
