package middleware

import (
	"context"
	"errors"

	gomailer "github.com/shyim/go-mailer"
)

// ErrReject is the sentinel a BeforeSend hook returns to silently skip delivery
// and report success. When a BeforeSend hook returns ErrReject (tested with
// errors.Is), the BeforeSend middleware returns (nil, nil) WITHOUT calling the
// wrapped transport. gomailer.Mailer.Send treats a (nil, nil) result as success,
// so the caller observes no error. This lets a hook drop a message (for example
// after a policy check) without surfacing an error. Any other (non-nil,
// non-ErrReject) error aborts the send and is returned to the caller unchanged
// (so errors.Is/As keep working).
var ErrReject = errors.New("gomailer/middleware: send rejected")

// BeforeSend returns a Middleware that runs fn before the wrapped transport's
// Send. It gives fn a chance to inspect, mutate, or reject each message just
// before delivery:
//
//   - fn may mutate the send-local *gomailer.Message and/or *gomailer.Envelope
//     before delivery.
//   - returning ErrReject (or an error wrapping it) skips the send and reports
//     success: this layer returns (nil, nil) and the wrapped transport is never
//     called.
//   - returning any other non-nil error aborts the send; that error is returned
//     to the caller unchanged.
//   - returning nil proceeds to the wrapped transport.
//
// Mutability caveat: only *gomailer.Message is mutable through this hook. A
// RawMessage produced by gomailer.NewRawMessage (pre-serialized bytes) carries
// no addressing and cannot be mutated, so fn receives a nil *gomailer.Message in
// that case; fn must guard against a nil message and rely on the envelope (which
// is always non-nil) for addressing changes. The values passed to fn are
// send-local clones for *gomailer.Message and explicit *gomailer.Envelope, so
// hook mutations do not alter the caller's retained objects.
//
// The envelope passed to fn is the one that will be used for delivery: if the
// caller passed a nil envelope it is derived from the message first (via
// gomailer.EnvelopeFromMessage) so fn always has a non-nil, mutable envelope,
// and the derived/mutated envelope is forwarded to the wrapped transport.
//
// A nil fn yields a pass-through (identity) Middleware so callers can wire it
// unconditionally.
func BeforeSend(fn func(ctx context.Context, msg *gomailer.Message, envelope *gomailer.Envelope) error) Middleware {
	if fn == nil {
		return func(t gomailer.Transport) gomailer.Transport { return t }
	}
	return func(next gomailer.Transport) gomailer.Transport {
		return &beforeSendTransport{next: next, fn: fn}
	}
}

// beforeSendTransport is the decorating transport built by BeforeSend.
type beforeSendTransport struct {
	next gomailer.Transport
	fn   func(ctx context.Context, msg *gomailer.Message, envelope *gomailer.Envelope) error
}

// String delegates to the wrapped transport so the decorator stays transparent
// to RoundRobin/Failover identity logic.
func (b *beforeSendTransport) String() string { return b.next.String() }

// Send derives the envelope when needed, runs the hook, then forwards to next.
func (b *beforeSendTransport) Send(ctx context.Context, msg gomailer.RawMessage, envelope *gomailer.Envelope) (*gomailer.SentMessage, error) {
	if m, ok := msg.(*gomailer.Message); ok {
		msg = m.Clone()
	}
	if envelope == nil {
		derived, err := gomailer.EnvelopeFromMessage(msg)
		if err != nil {
			return nil, err
		}
		envelope = derived
	} else {
		envelope = envelope.Clone()
	}

	// Only *gomailer.Message is mutable; a pre-serialized RawMessage yields nil.
	mutable, _ := msg.(*gomailer.Message)

	if err := b.fn(ctx, mutable, envelope); err != nil {
		if errors.Is(err, ErrReject) {
			return nil, nil
		}
		return nil, err
	}

	return b.next.Send(ctx, msg, envelope)
}

// AfterSend returns a Middleware that runs fn after the wrapped transport's
// Send, observing its result. Use it to react to deliveries and failures (for
// example logging or metrics): fn receives the (*gomailer.SentMessage, error)
// returned by the wrapped Send.
//
//   - On success fn receives (sm, nil) where sm is the delivered message.
//   - On failure fn receives (nil, err).
//   - On a rejected send (sm == nil && err == nil) fn is still called with
//     (nil, nil); fn should guard against a nil SentMessage.
//
// AfterSend is observe-only: it never alters the returned (*SentMessage, error),
// so errors.Is(err, gomailer.ErrTransport) and errors.As to
// *gomailer.TransportError keep working for outer layers. A nil fn yields a
// pass-through (identity) Middleware.
func AfterSend(fn func(ctx context.Context, sm *gomailer.SentMessage, err error)) Middleware {
	if fn == nil {
		return func(t gomailer.Transport) gomailer.Transport { return t }
	}
	return func(next gomailer.Transport) gomailer.Transport {
		return &afterSendTransport{next: next, fn: fn}
	}
}

// afterSendTransport is the decorating transport built by AfterSend.
type afterSendTransport struct {
	next gomailer.Transport
	fn   func(ctx context.Context, sm *gomailer.SentMessage, err error)
}

// String delegates to the wrapped transport.
func (a *afterSendTransport) String() string { return a.next.String() }

// Send forwards to next, then hands the result to fn before returning it.
func (a *afterSendTransport) Send(ctx context.Context, msg gomailer.RawMessage, envelope *gomailer.Envelope) (*gomailer.SentMessage, error) {
	sm, err := a.next.Send(ctx, msg, envelope)
	a.fn(ctx, sm, err)
	return sm, err
}

// Compile-time assertions that the decorators satisfy gomailer.Transport.
var (
	_ gomailer.Transport = (*beforeSendTransport)(nil)
	_ gomailer.Transport = (*afterSendTransport)(nil)
)
