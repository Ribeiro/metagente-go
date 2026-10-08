// Package broker is the small interface that Metagente needs from a message broker, and the two
// brokers behind it: JetStream, and a broker in memory for tests and for trying agents without a server.
//
// A message carries an id, and the broker drops a copy of an id it has seen lately: the delivery of a
// broker is at least once, and this is the first of the layers that make the effect happen once (section
// 9 of docs/design-async-elt.md).
package broker

import (
	"context"
	"errors"
	"strings"
)

// Message is what is published.
type Message struct {
	// Subject is where it goes. It has no wildcards.
	Subject string
	// ID makes a copy recognizable: the broker drops a message whose id it has accepted lately.
	ID   string
	Data []byte
}

// PubAck is the broker saying that it keeps the message.
type PubAck struct {
	// Stream is the stream that took the message.
	Stream string
	// Seq is the place of the message in the stream.
	Seq uint64
	// Duplicate is true when the id was already there: the message was dropped, and the answer is the one of
	// the first copy.
	Duplicate bool
}

// Broker is a connection to a broker.
type Broker interface {
	// Publish sends a message and waits until the broker says that it keeps it.
	Publish(ctx context.Context, m Message) (PubAck, error)
	// Close ends the connection.
	Close() error
}

// What can go wrong when a message is published. A broker wraps one of these with its own words, so the
// caller can tell a failure that may pass from one that cannot.
var (
	// ErrUnavailable is a broker that cannot be reached, or that did not answer in time.
	ErrUnavailable = errors.New("the broker cannot be reached")
	// ErrFull is a stream that refuses the message because it holds as much as it may (the stream discards
	// the new ones, so that nothing that was not processed is thrown away). It is the back pressure.
	ErrFull = errors.New("the stream is full")
	// ErrNoStream is a subject that no stream takes.
	ErrNoStream = errors.New("no stream takes this subject")
	// ErrNotInBuild is a broker that this build of Metagente was made without (see the tags nojetstream).
	ErrNotInBuild = errors.New("this build was made without that broker")
	// ErrRefused is a message that the broker will not take, whatever the time.
	ErrRefused = errors.New("the broker refused the message")
)

// MayPass says whether a failure to publish may pass if it is tried again later.
func MayPass(err error) bool {
	return errors.Is(err, ErrUnavailable) || errors.Is(err, ErrFull)
}

// ValidSubject says whether text is a subject a message may be sent to: names made of letters, digits, `_`
// and `-`, joined by dots, with no wildcards.
func ValidSubject(text string) bool {
	return validSubject(text, false)
}

// ValidPattern says whether text is a subject that an agent may declare: like a subject, but a name may be
// `*` (any one name), and the last may be `>` (any names that follow).
func ValidPattern(text string) bool {
	return validSubject(text, true)
}

func validSubject(text string, wildcards bool) bool {
	if text == "" || len(text) > 255 {
		return false
	}
	names := strings.Split(text, ".")
	for i, name := range names {
		switch {
		case name == "":
			return false
		case name == "*" || name == ">":
			if !wildcards || (name == ">" && i != len(names)-1) {
				return false
			}
		case !plainName(name):
			return false
		}
	}
	return true
}

func plainName(name string) bool {
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// Matches says whether a subject is one that the pattern allows.
func Matches(pattern, subject string) bool {
	p, s := strings.Split(pattern, "."), strings.Split(subject, ".")
	for i, name := range p {
		switch {
		case name == ">":
			return len(s) > i
		case i >= len(s):
			return false
		case name != "*" && name != s[i]:
			return false
		}
	}
	return len(p) == len(s)
}
