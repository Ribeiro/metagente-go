package llm

import (
	"context"
	"errors"
	"sync"
)

// Reply is one scripted answer: a response, or a failure.
type Reply struct {
	Response *Response
	Err      error
}

// Scripted is a model for tests: it answers with what it was given, in order,
// and remembers what it was asked. It uses no network.
type Scripted struct {
	mu      sync.Mutex
	replies []Reply
	seen    []*Request
}

// NewScripted makes a model that gives the replies one after the other.
func NewScripted(replies ...Reply) *Scripted { return &Scripted{replies: replies} }

// Complete implements Llm.
func (s *Scripted) Complete(_ context.Context, req *Request) (*Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *req
	copied.Messages = append([]Message(nil), req.Messages...)
	s.seen = append(s.seen, &copied)
	if len(s.replies) == 0 {
		return nil, errors.New("the scripted model has no more answers")
	}
	next := s.replies[0]
	s.replies = s.replies[1:]
	return next.Response, next.Err
}

// Requests are the questions the model received.
func (s *Scripted) Requests() []*Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Request(nil), s.seen...)
}

// Say is a final answer in words.
func Say(text string) Reply {
	return Reply{Response: &Response{Parts: []Part{{Kind: PartText, Text: text}}, Stop: StopEnd, Reason: "end_turn", Usage: Usage{Input: 10, Output: 5}}}
}

// Ask is an answer that asks for tools.
func Ask(uses ...Part) Reply {
	return Reply{Response: &Response{Parts: uses, Stop: StopToolUse, Reason: "tool_use", Usage: Usage{Input: 10, Output: 5}}}
}

// Use is a request for one tool.
func Use(id, name string, input map[string]any) Part {
	return Part{Kind: PartToolUse, ID: id, Name: name, Input: input}
}

// Cut is an answer that stopped because it ran out of tokens.
func Cut(text string) Reply {
	return Reply{Response: &Response{Parts: []Part{{Kind: PartText, Text: text}}, Stop: StopLength, Reason: "max_tokens", Usage: Usage{Input: 10, Output: 5}}}
}

// Fail is a failure of the provider.
func Fail(err error) Reply { return Reply{Err: err} }
