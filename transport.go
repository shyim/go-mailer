package gomailer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Transport sends a single message and returns the SentMessage. Every concrete
// transport, decorator and router satisfies this. Implements fmt.Stringer so a
// transport can identify itself (used in logs and the RoundRobin/Failover name).
type Transport interface {
	// Send delivers msg using envelope. If envelope is nil it is derived from
	// the message via EnvelopeFromMessage. A nil *SentMessage with a nil error
	// means the send was rejected by a middleware (see middleware.BeforeSend /
	// middleware.ErrReject).
	Send(ctx context.Context, msg RawMessage, envelope *Envelope) (*SentMessage, error)
	// String returns the transport's DSN-like identity, e.g. "smtp://host".
	String() string
}

// SenderFunc is the per-transport delivery hook. A concrete transport embeds
// BaseTransport and supplies a SenderFunc that performs the actual delivery,
// mutating sm (message id, debug) as needed.
type SenderFunc func(ctx context.Context, sm *SentMessage) error

// BaseTransport implements the shared Send pipeline: derive/clone the envelope,
// build the SentMessage, invoke the DoSend hook (wrapping any failure so it is
// classifiable via errors.Is(err, ErrTransport)), and apply rate throttling.
// Concrete transports embed it by value and set DoSend + Name.
//
// Pre-send mutation, rejection and post-send observation are no longer part of
// this pipeline; compose them with the middleware package
// (middleware.BeforeSend / middleware.AfterSend) around the transport instead.
//
//	type NullTransport struct{ gomailer.BaseTransport }
//	func NewNullTransport() *NullTransport {
//	    t := &NullTransport{}
//	    t.Name = "null://"
//	    t.DoSend = func(ctx context.Context, sm *gomailer.SentMessage) error { return nil }
//	    return t
//	}
type BaseTransport struct {
	// Name is returned by String(); set by the embedding transport.
	Name string
	// DoSend performs the actual delivery. Must be set before Send is called.
	DoSend SenderFunc
	// throttleMu guards maxPerSecond/lastSent so that concurrent Send calls on
	// the same transport do not race on the throttle clock. Send uses a pointer
	// receiver on a value-embedded BaseTransport that may be shared across
	// goroutines, so the throttle state needs synchronization.
	throttleMu sync.Mutex
	// maxPerSecond and lastSent implement throttling (0 disables).
	maxPerSecond float64
	lastSent     time.Time
}

// Send derives or clones the envelope, builds the SentMessage, calls DoSend and
// throttles. A delivery failure is wrapped so it is classifiable via
// errors.Is(err, ErrTransport).
//
// Mutation note: pre-send message/envelope mutation now lives in
// middleware.BeforeSend. BaseTransport clones *Message and explicit Envelope
// inputs before snapshotting them into SentMessage, so delivery does not mutate
// caller-owned values.
func (b *BaseTransport) Send(ctx context.Context, msg RawMessage, envelope *Envelope) (*SentMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if isNilRawMessage(msg) {
		return nil, fmt.Errorf("%w: message must not be nil", ErrInvalidArgument)
	}
	if m, ok := msg.(*Message); ok {
		msg = m.Clone()
	}
	if envelope == nil {
		derived, err := EnvelopeFromMessage(msg)
		if err != nil {
			return nil, err
		}
		envelope = derived
	} else {
		envelope = envelope.Clone()
	}

	sm, err := NewSentMessage(msg, envelope)
	if err != nil {
		return nil, err
	}
	if b.DoSend == nil {
		return nil, fmt.Errorf("%w: BaseTransport.DoSend is nil", ErrLogic)
	}

	if err := b.checkThrottling(ctx); err != nil {
		return nil, err
	}
	if err := b.DoSend(ctx, sm); err != nil {
		return nil, wrapTransportError(err)
	}
	return sm, nil
}

// wrapTransportError ensures every delivery failure is classifiable via
// errors.Is(err, ErrTransport). Errors that already satisfy that membership
// (e.g. *TransportError) are returned unchanged.
func wrapTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var te *TransportError
	if errors.As(err, &te) {
		return err
	}
	if errors.Is(err, ErrTransport) {
		return err
	}
	// Use a distinct wrapping message rather than err.Error(); TransportError's
	// Error() appends the Cause, so reusing the cause text here would double it.
	wrapped := NewTransportError("send failed")
	wrapped.Cause = err
	return wrapped
}

// String returns b.Name and satisfies fmt.Stringer.
func (b *BaseTransport) String() string {
	return b.Name
}

// SetMaxPerSecond sets the throttle rate (messages/second; <=0 disables).
func (b *BaseTransport) SetMaxPerSecond(rate float64) {
	if rate <= 0 {
		rate = 0
	}
	b.throttleMu.Lock()
	b.maxPerSecond = rate
	b.lastSent = time.Time{}
	b.throttleMu.Unlock()
}

// checkThrottling sleeps so that no more than maxPerSecond messages are sent
// per second, while honoring context cancellation. If the context is cancelled
// mid-sleep the throttle clock is left unchanged so the rate window is not
// advanced by an interrupted wait.
func (b *BaseTransport) checkThrottling(ctx context.Context) error {
	b.throttleMu.Lock()
	defer b.throttleMu.Unlock()

	if b.maxPerSecond == 0 {
		return ctx.Err()
	}

	interval := time.Duration(float64(time.Second) / b.maxPerSecond)
	if !b.lastSent.IsZero() {
		sleep := interval - time.Since(b.lastSent)
		if sleep > 0 {
			timer := time.NewTimer(sleep)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
	}
	b.lastSent = time.Now()
	return ctx.Err()
}

var _ fmt.Stringer = (*BaseTransport)(nil)
