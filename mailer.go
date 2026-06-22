package gomailer

import (
	"context"
	"io"
)

// MailerInterface sends a message, optionally with an explicit envelope.
// Sending is synchronous; the SendOption seam below leaves room for a future
// queueing implementation.
type MailerInterface interface {
	// Send delivers msg through the underlying transport. A nil envelope is
	// derived from the message.
	Send(ctx context.Context, msg RawMessage, envelope *Envelope, opts ...SendOption) error
}

// SendOption is a functional option seam for future async/queue behavior.
// No options are defined yet (synchronous send only).
type SendOption func(*sendConfig)

// sendConfig collects SendOption values (unexported; reserved for async seam).
type sendConfig struct{}

// Mailer is the default synchronous MailerInterface implementation.
type Mailer struct {
	transport Transport
}

// NewMailer wraps a Transport. Cross-cutting behavior (mutate-before-send,
// reject, observe) is layered onto the transport with the middleware package
// before it is passed here.
func NewMailer(t Transport) *Mailer {
	return &Mailer{transport: t}
}

// Send implements MailerInterface by delegating to the transport.
//
// A rejected send surfaces here as a nil error: the transport returns (nil, nil)
// in that case (e.g. a middleware.BeforeSend hook returning middleware.ErrReject)
// and Mailer.Send reports success.
func (m *Mailer) Send(ctx context.Context, msg RawMessage, envelope *Envelope, opts ...SendOption) error {
	var cfg sendConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	_, err := m.transport.Send(ctx, msg, envelope)
	return err
}

// Close releases resources held by the underlying transport. If the transport
// implements io.Closer (the SMTP transport and the RoundRobin/Failover/Transports
// composites do), its Close is called so pooled SMTP connections are QUIT and
// closed on shutdown; otherwise Close is a no-op. Safe to call more than once.
func (m *Mailer) Close() error {
	if c, ok := m.transport.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

var _ MailerInterface = (*Mailer)(nil)
