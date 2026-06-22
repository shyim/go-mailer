// Package mailertest provides an in-memory Transport that records every
// message it is asked to send, together with assertion helpers for use with
// the standard testing package. Pair it with the AssertEmailCount and
// AssertQueuedEmailCount helpers to verify what your code sent.
package mailertest

import (
	"context"
	"sync"

	gomailer "github.com/shyim/go-mailer"
)

// RecordingTransport is a gomailer.Transport that delivers nothing but records
// each send for later inspection. It is safe for concurrent use.
//
// Unlike the production transports it does not embed gomailer.BaseTransport, so
// it dispatches no events and applies no throttling: it captures the envelope
// (deriving one from the message when none is supplied) and the resulting
// SentMessage. Sends are synchronous, so every recorded message is "sent" and
// none is ever "queued"; tests asserting a queued count must expect 0 (see
// AssertQueuedEmailCount).
type RecordingTransport struct {
	mu       sync.Mutex
	name     string
	sent     []*gomailer.SentMessage
	failNext error
}

// NewRecordingTransport returns an empty RecordingTransport identified by the
// given name in String() (defaults to "test://" when empty).
func NewRecordingTransport(name string) *RecordingTransport {
	if name == "" {
		name = "test://"
	}
	return &RecordingTransport{name: name}
}

// String returns the transport's identity and satisfies fmt.Stringer.
func (t *RecordingTransport) String() string {
	return t.name
}

// FailNext makes the next Send return err without recording the message. A nil
// err clears any pending failure. It exists so tests can exercise their own
// error-handling paths.
func (t *RecordingTransport) FailNext(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failNext = err
}

// Send records msg and returns the resulting SentMessage. A nil envelope is
// derived from the message via gomailer.EnvelopeFromMessage.
func (t *RecordingTransport) Send(ctx context.Context, msg gomailer.RawMessage, envelope *gomailer.Envelope) (*gomailer.SentMessage, error) {
	t.mu.Lock()
	if t.failNext != nil {
		err := t.failNext
		t.failNext = nil
		t.mu.Unlock()
		return nil, err
	}
	t.mu.Unlock()

	if envelope == nil {
		var err error
		envelope, err = gomailer.EnvelopeFromMessage(msg)
		if err != nil {
			return nil, err
		}
	} else {
		envelope = envelope.Clone()
	}

	sm, err := gomailer.NewSentMessage(msg, envelope)
	if err != nil {
		return nil, err
	}

	t.mu.Lock()
	t.sent = append(t.sent, sm)
	t.mu.Unlock()
	return sm, nil
}

// Messages returns a snapshot slice of every SentMessage recorded so far, in
// send order.
func (t *RecordingTransport) Messages() []*gomailer.SentMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*gomailer.SentMessage, len(t.sent))
	copy(out, t.sent)
	return out
}

// Last returns the most recently recorded SentMessage and true, or nil and
// false if nothing has been sent.
func (t *RecordingTransport) Last() (*gomailer.SentMessage, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.sent) == 0 {
		return nil, false
	}
	return t.sent[len(t.sent)-1], true
}

// Count returns the number of messages recorded so far.
func (t *RecordingTransport) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sent)
}

// Reset discards all recorded messages.
func (t *RecordingTransport) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sent = nil
}
