package transport

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	gomailer "github.com/shyim/go-mailer"
)

// RoundRobinTransport delivers each message through one of several underlying
// transports, rotating between them on every send. A transport that fails is
// marked dead and skipped until retryPeriod has elapsed, at which point it is
// retried. When every transport
// is dead the accumulated *gomailer.TransportError is returned.
//
// Concurrency: Send calls are serialized (single-flight) so cursor and
// dead-tracking semantics stay stable and the composite is safe to share across
// goroutines. Note the tradeoff: because the whole Send — including the blocking
// network round-trip — runs under that lock, a composite delivers one message at
// a time across the entire pool, and a slow server stalls other senders for up
// to its IO timeout. If you need concurrent delivery across servers, run several
// independent composites (or wrap leaves and fan out yourself) rather than
// expecting one composite to parallelize.
type RoundRobinTransport struct {
	sendMu      sync.Mutex
	mu          sync.Mutex
	transports  []gomailer.Transport
	dead        map[int]time.Time
	cursor      int  // -1 means "not yet initialized"
	initialized bool // whether the initial cursor has been seeded
	retryPeriod time.Duration

	// initialCursor seeds the first cursor position. It is randomized so a
	// short-lived process still spreads load across transports; it is made
	// injectable for deterministic tests.
	initialCursor func(n int) int

	// now is the clock, overridable in tests.
	now func() time.Time

	// logger, when set, receives a debug record each time a transport is marked
	// dead (failover selection visibility). nil disables logging.
	logger *slog.Logger

	// nameSymbol is the prefix used by String ("roundrobin" or "failover").
	nameSymbol string
}

// SetLogger sets an optional *slog.Logger that records when an underlying
// transport is marked dead, giving operators visibility into failover behavior
// ("why did mail go out the backup MX"). nil (the default) disables logging.
func (t *RoundRobinTransport) SetLogger(l *slog.Logger) *RoundRobinTransport {
	t.logger = l
	return t
}

// DefaultRetryPeriod is the period after which a dead transport is retried.
const DefaultRetryPeriod = 60 * time.Second

// NewRoundRobinTransport builds a RoundRobinTransport over the given transports.
// retryPeriod <= 0 selects DefaultRetryPeriod. It returns ErrTransport (via a
// *gomailer.TransportError) if no transports are supplied.
func NewRoundRobinTransport(transports []gomailer.Transport, retryPeriod time.Duration) (*RoundRobinTransport, error) {
	if len(transports) == 0 {
		return nil, gomailer.NewTransportError("RoundRobinTransport must have at least one transport configured")
	}
	if retryPeriod <= 0 {
		retryPeriod = DefaultRetryPeriod
	}
	return &RoundRobinTransport{
		transports:    transports,
		dead:          make(map[int]time.Time),
		cursor:        -1,
		retryPeriod:   retryPeriod,
		initialCursor: randomInitialCursor,
		now:           time.Now,
		nameSymbol:    "roundrobin",
	}, nil
}

func randomInitialCursor(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// SetInitialCursor overrides how the first cursor position is chosen. fn is
// called once with the number of transports and must return an index in
// [0, n). It replaces the default randomized seeding and makes rotation
// deterministic for tests.
func (t *RoundRobinTransport) SetInitialCursor(fn func(n int) int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if fn != nil {
		t.initialCursor = fn
	}
}

// Send delivers msg through the next live transport, rotating on every call and
// failing over to the next transport on a transport error. See the type docs.
func (t *RoundRobinTransport) Send(ctx context.Context, msg gomailer.RawMessage, envelope *gomailer.Envelope) (*gomailer.SentMessage, error) {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()

	var aggErr *gomailer.TransportError

	for {
		idx, transport := t.nextTransport()
		if transport == nil {
			break
		}

		// Clone the message before each attempt so that header mutations made
		// by a failing transport do not persist into the next transport or back
		// into the caller's message.
		sent, err := transport.Send(ctx, cloneMessage(msg), cloneEnvelope(envelope))
		if err == nil {
			return sent, nil
		}

		// Only transport-level errors trigger failover; anything else is
		// returned immediately (e.g. context cancellation, validation).
		if !isTransportError(err) {
			return nil, err
		}

		if aggErr == nil {
			aggErr = gomailer.NewTransportError("All transports failed.")
		}
		aggErr.AppendDebug(fmt.Sprintf("Transport \"%s\": %s\n", transport.String(), debugOf(err)))
		// Surface the most recent underlying SMTP code on the aggregate so a
		// caller doing errors.As(err, &te) can branch on te.Code (4xx retryable
		// vs 5xx permanent); also chain the cause for full-chain inspection.
		// errors.As stops at the aggregate (same type), so lifting Code is what
		// actually makes the code reachable.
		aggErr.Cause = err
		if c := smtpCode(err); c != 0 {
			aggErr.Code = c
		}
		t.markDead(idx)
	}

	if aggErr != nil {
		return nil, aggErr
	}
	return nil, gomailer.NewTransportError("No transports found.")
}

// cloneMessage returns a copy of msg suitable for handing to one underlying
// transport. Only a *gomailer.Message carries mutable headers worth isolating;
// a header-less RawMessage is returned as-is (it has nothing to leak).
func cloneMessage(msg gomailer.RawMessage) gomailer.RawMessage {
	if m, ok := msg.(*gomailer.Message); ok {
		return m.Clone()
	}
	return msg
}

func cloneEnvelope(envelope *gomailer.Envelope) *gomailer.Envelope {
	if envelope == nil {
		return nil
	}
	return envelope.Clone()
}

// String returns the composite identity, e.g. "roundrobin(smtp://a smtp://b)".
func (t *RoundRobinTransport) String() string {
	names := make([]string, len(t.transports))
	for i, tr := range t.transports {
		names[i] = tr.String()
	}
	return t.nameSymbol + "(" + strings.Join(names, " ") + ")"
}

// Close closes every underlying transport that implements io.Closer, joining
// any errors. It lets a caller release pooled SMTP connections on shutdown.
func (t *RoundRobinTransport) Close() error {
	return closeAll(t.transports)
}

// nextTransport rotates the transport list and returns the first live instance,
// or nil if all transports are currently dead.
func (t *RoundRobinTransport) nextTransport() (int, gomailer.Transport) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.nextTransportLocked()
}

func (t *RoundRobinTransport) nextTransportLocked() (int, gomailer.Transport) {
	if !t.initialized {
		t.cursor = t.initialCursor(len(t.transports))
		if t.cursor < 0 || t.cursor >= len(t.transports) {
			t.cursor = 0
		}
		t.initialized = true
	}

	start := t.cursor
	cursor := t.cursor
	for {
		transport := t.transports[cursor]

		if !t.isDeadLocked(cursor) {
			t.cursor = t.moveCursor(cursor)
			return cursor, transport
		}

		if t.now().Sub(t.dead[cursor]) > t.retryPeriod {
			delete(t.dead, cursor)
			t.cursor = t.moveCursor(cursor)
			return cursor, transport
		}

		cursor = t.moveCursor(cursor)
		if cursor == start {
			return -1, nil
		}
	}
}

// isDeadLocked reports whether a transport is currently marked dead.
func (t *RoundRobinTransport) isDeadLocked(index int) bool {
	_, ok := t.dead[index]
	return ok
}

// markDead records that a transport just failed.
func (t *RoundRobinTransport) markDead(index int) {
	t.mu.Lock()
	t.dead[index] = t.now()
	t.mu.Unlock()
	if t.logger != nil {
		t.logger.Debug("gomailer: transport marked dead",
			"transport", t.transports[index].String(),
			"retry_after", t.retryPeriod)
	}
}

// moveCursor advances the cursor, wrapping around to 0.
func (t *RoundRobinTransport) moveCursor(cursor int) int {
	cursor++
	if cursor >= len(t.transports) {
		return 0
	}
	return cursor
}

// isTransportError reports whether err is a transport-level failure that should
// trigger failover to another transport.
func isTransportError(err error) bool {
	return errors.Is(err, gomailer.ErrTransport)
}

// debugOf extracts the debug transcript from a *gomailer.TransportError, or
// falls back to the error string.
func debugOf(err error) string {
	var te *gomailer.TransportError
	if errors.As(err, &te) {
		return te.Debug()
	}
	return err.Error()
}

// smtpCode extracts the SMTP response code from a *gomailer.TransportError in
// err's chain, or 0 if none carries one.
func smtpCode(err error) int {
	var te *gomailer.TransportError
	if errors.As(err, &te) {
		return te.Code
	}
	return 0
}
