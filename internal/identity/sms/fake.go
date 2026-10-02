package sms

import (
	"context"
	"sync"
)

// Fake records what it was asked to send and delivers nothing.
//
// It exists for tests and for the in-memory API world, and **`New` cannot build
// one**: no configuration an operator can save selects it. A provider that
// "delivered" to a list only the process could read would be a phone sign-in that
// nobody can complete, or — if anything ever printed the list — a code in a log.
type Fake struct {
	mu   sync.Mutex
	sent []Message
	// Fail, when set, is returned by every Send.
	Fail error
}

// Send records the message.
func (f *Fake) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return f.Fail
	}
	f.sent = append(f.sent, m)
	return nil
}

// Sent is every message recorded, oldest first.
func (f *Fake) Sent() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.sent...)
}

// Last is the most recent message, or the zero value.
func (f *Fake) Last() Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return Message{}
	}
	return f.sent[len(f.sent)-1]
}
