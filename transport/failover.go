package transport

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	gomailer "github.com/shyim/go-mailer"
)

// FailoverTransport delivers messages through several transports using a
// failover algorithm: it sticks to the current transport until that transport
// fails (is marked dead), then moves on to the next live one. The first
// transport is always tried first (the initial cursor is fixed at 0).
//
// FailoverTransport reuses RoundRobinTransport's rotation and dead-tracking
// internals directly and layers the sticky "current transport" selection on
// top, so failover and round-robin share one implementation of liveness
// tracking.
//
// Concurrency: Send calls are serialized so sticky-current selection and
// dead-marking remain stable for shared failover transports.
type FailoverTransport struct {
	sendMu       sync.Mutex
	rr           *RoundRobinTransport
	mu           sync.Mutex
	current      gomailer.Transport
	currentIndex int
}

// NewFailoverTransport builds a FailoverTransport over the given transports.
// retryPeriod <= 0 selects DefaultRetryPeriod. It returns ErrTransport (via a
// *gomailer.TransportError) if no transports are supplied.
func NewFailoverTransport(transports []gomailer.Transport, retryPeriod time.Duration) (*FailoverTransport, error) {
	rr, err := NewRoundRobinTransport(transports, retryPeriod)
	if err != nil {
		return nil, err
	}
	// Failover always tries the first transport first, so fix the initial
	// cursor at 0 instead of randomizing it.
	rr.nameSymbol = "failover"
	rr.SetInitialCursor(func(int) int { return 0 })
	return &FailoverTransport{rr: rr}, nil
}

// Send delivers msg, sticking to the current transport until it dies. See the
// type docs.
func (t *FailoverTransport) Send(ctx context.Context, msg gomailer.RawMessage, envelope *gomailer.Envelope) (*gomailer.SentMessage, error) {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()

	var aggErr *gomailer.TransportError

	for {
		idx, transport := t.nextTransport()
		if transport == nil {
			break
		}

		// Clone the message before each attempt so a failing transport's header
		// mutations do not leak back to the caller or into the next attempt.
		sent, err := transport.Send(ctx, cloneMessage(msg), cloneEnvelope(envelope))
		if err == nil {
			return sent, nil
		}

		if !isTransportError(err) {
			return nil, err
		}

		if aggErr == nil {
			aggErr = gomailer.NewTransportError("All transports failed.")
		}
		aggErr.AppendDebug(fmt.Sprintf("Transport \"%s\": %s\n", transport.String(), debugOf(err)))
		// Surface the most recent underlying SMTP code on the aggregate and
		// chain the cause, so errors.As(err, &te) exposes te.Code (see RoundRobin).
		aggErr.Cause = err
		if c := smtpCode(err); c != 0 {
			aggErr.Code = c
		}
		t.rr.markDead(idx)
	}

	if aggErr != nil {
		return nil, aggErr
	}
	return nil, gomailer.NewTransportError("No transports found.")
}

// String returns the composite identity, e.g. "failover(smtp://a smtp://b)".
func (t *FailoverTransport) String() string {
	return t.rr.String()
}

// Close closes every underlying transport that implements io.Closer (delegating
// to the embedded round-robin pool), joining any errors.
func (t *FailoverTransport) Close() error {
	return t.rr.Close()
}

// SetLogger sets an optional *slog.Logger that records failover (dead-marking)
// events. nil disables logging.
func (t *FailoverTransport) SetLogger(l *slog.Logger) *FailoverTransport {
	t.rr.SetLogger(l)
	return t
}

// SetInitialCursor overrides how the first transport is chosen when the current
// transport must be (re)selected. Mainly useful for test determinism; it
// mirrors RoundRobinTransport.SetInitialCursor and defaults to 0.
func (t *FailoverTransport) SetInitialCursor(fn func(n int) int) {
	t.rr.SetInitialCursor(fn)
}

// nextTransport returns the sticky current transport, only advancing to a new
// one when the current is unset or has been marked dead.
func (t *FailoverTransport) nextTransport() (int, gomailer.Transport) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.current == nil || t.isCurrentDead() {
		t.rr.mu.Lock()
		t.currentIndex, t.current = t.rr.nextTransportLocked()
		t.rr.mu.Unlock()
	}
	return t.currentIndex, t.current
}

// isCurrentDead reports whether the sticky current transport is marked dead.
func (t *FailoverTransport) isCurrentDead() bool {
	t.rr.mu.Lock()
	defer t.rr.mu.Unlock()
	if t.current == nil || t.currentIndex < 0 {
		return false
	}
	return t.rr.isDeadLocked(t.currentIndex)
}
