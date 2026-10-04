// Package llm talks to language models. Agents never name a model or a
// provider: the choice lives in metagente.toml, and this package turns it into
// a client for Anthropic or for any server that speaks the OpenAI chat format.
//
// The two providers are written here, on top of net/http, because what matters
// most is under our control: the key goes only to the address the person
// approved (T3), never follows a redirect, never shows up in an error (L8), and
// a cut answer is an error and not a quietly short one (L1).
package llm

import (
	"context"
	"strings"

	"metagente/internal/secret"
)

// Role tells who wrote a message.
type Role string

// The two roles of a conversation. The instructions travel apart, in
// Request.System.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// PartKind tells what a part of a message is.
type PartKind int

// The kinds of part.
const (
	PartText       PartKind = iota // text written by the person or by the model
	PartToolUse                    // the model asks for a tool
	PartToolResult                 // what the tool answered
)

// Part is a piece of a message.
type Part struct {
	Kind PartKind
	// Text is the text, or the content of a tool result.
	Text string
	// ID ties a tool result to the tool use it answers.
	ID string
	// Name is the tool the model asks for.
	Name string
	// Input are the values the model gave the tool.
	Input map[string]any
	// BadInput is true when the model's values were not a JSON object.
	BadInput bool
	// IsError marks a tool result that tells of a failure.
	IsError bool
}

// Message is one turn of the conversation.
type Message struct {
	Role  Role
	Parts []Part
}

// Tool is something the model may ask for.
type Tool struct {
	Name        string
	Description string
	// Schema is a JSON schema of type object.
	Schema map[string]any
}

// Request is one question to the model.
type Request struct {
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
	// Cache asks the provider to reuse the start of the conversation between
	// steps, when it can (requirement L4).
	Cache bool
}

// Stop tells why the model stopped.
type Stop int

// The reasons to stop, the same for every provider.
const (
	StopEnd     Stop = iota // it finished its answer
	StopToolUse             // it wants a tool
	StopLength              // it hit the limit of tokens: the answer is cut
	StopRefusal             // it declined to answer
	StopOther               // anything else
)

// Usage counts tokens.
type Usage struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
}

// Total is what the answer cost against a budget of tokens.
func (u Usage) Total() int { return u.Input + u.Output }

// Response is the answer of the model.
type Response struct {
	Parts []Part
	Stop  Stop
	// Reason is the word the provider used for Stop, for messages.
	Reason string
	Usage  Usage
}

// Text is the text of the answer.
func (r *Response) Text() string {
	var b strings.Builder
	for _, p := range r.Parts {
		if p.Kind == PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// ToolUses are the tools the model asked for, in order.
func (r *Response) ToolUses() []Part {
	var uses []Part
	for _, p := range r.Parts {
		if p.Kind == PartToolUse {
			uses = append(uses, p)
		}
	}
	return uses
}

// Llm is a language model.
type Llm interface {
	Complete(ctx context.Context, req *Request) (*Response, error)
}

// Error is a failure of the provider, already written for a person to read and
// with every secret taken out.
type Error struct {
	Message string
	// Status is the HTTP status, or 0.
	Status int
	// Retry says that trying again may work.
	Retry bool
	// Hint says what to do about it, when the provider's answer makes that clear.
	Hint string
}

func (e *Error) Error() string { return e.Message }

// Redact hides secrets in a text. A short secret is left alone, because hiding
// a word that happens to look like it would make messages unreadable.
func Redact(text string, secrets ...string) string { return secret.Redact(text, secrets...) }
