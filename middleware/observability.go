package middleware

import (
	"context"
	"time"

	gomailer "github.com/shyim/go-mailer"
)

// --- Provider-agnostic observability interfaces ---

// Tracer starts spans for individual Send attempts. It is satisfied by an
// adapter over an OpenTelemetry tracer; the core never references OTel.
type Tracer interface {
	// Start begins a span named by the caller and returns a context carrying
	// the span plus the Span handle. The returned context MUST be used for the
	// wrapped Send so downstream work nests under the span. Implementations
	// must never return a nil Span.
	Start(ctx context.Context, name string) (context.Context, Span)
}

// Span is the minimal span surface the middleware needs. All methods must be
// safe to call on the value returned by Tracer.Start, including after End.
type Span interface {
	// SetAttributes attaches key/value attributes to the span. Values are
	// limited to the kinds carried by Attr (string, int64, bool).
	SetAttributes(attrs ...Attr)
	// RecordError records err as a span event (no status change).
	RecordError(err error)
	// SetError marks the span status as Error with the given description.
	SetError(description string)
	// End finishes the span. It must be safe to call exactly once.
	End()
}

// Meter yields the instruments the middleware records into. Returned
// instruments must be non-nil and safe for concurrent use. An adapter is
// expected to create each instrument once (lazily or eagerly) and return the
// same instance on repeated calls so instrument identity is stable.
type Meter interface {
	// SendCounter counts Send attempts, partitioned by the outcome attribute.
	SendCounter() Counter
	// DurationHistogram records Send latency in MILLISECONDS.
	DurationHistogram() Histogram
}

// Counter records monotonic integer increments with attributes.
type Counter interface {
	// Add adds delta (typically 1) with the given attributes.
	Add(ctx context.Context, delta int64, attrs ...Attr)
}

// Histogram records a distribution of float64 values (here: milliseconds).
type Histogram interface {
	// Record records value with the given attributes.
	Record(ctx context.Context, value float64, attrs ...Attr)
}

// Attr is a single observability attribute. Exactly one of the typed fields is
// meaningful, selected by Kind. Helper constructors (String/Int/Bool) build
// these; adapters translate them into provider-native attributes.
type Attr struct {
	Key  string
	Kind AttrKind
	Str  string
	Int  int64
	Bool bool
}

// AttrKind discriminates the active field of an Attr.
type AttrKind uint8

const (
	// KindString selects Attr.Str.
	KindString AttrKind = iota
	// KindInt selects Attr.Int.
	KindInt
	// KindBool selects Attr.Bool.
	KindBool
)

// String builds a string-valued Attr.
func String(key, value string) Attr { return Attr{Key: key, Kind: KindString, Str: value} }

// Int builds an int64-valued Attr.
func Int(key string, value int64) Attr { return Attr{Key: key, Kind: KindInt, Int: value} }

// Bool builds a bool-valued Attr.
func Bool(key string, value bool) Attr { return Attr{Key: key, Kind: KindBool, Bool: value} }

// --- Observability middleware constructor + options ---

// Option configures the Observability middleware.
type Option func(*obsConfig)

type obsConfig struct {
	tracer   Tracer
	meter    Meter
	spanName string
}

// WithTracer installs the span-producing Tracer. A nil tracer disables spans.
func WithTracer(t Tracer) Option { return func(c *obsConfig) { c.tracer = t } }

// WithMeter installs the metric instruments source. A nil meter disables metrics.
func WithMeter(m Meter) Option { return func(c *obsConfig) { c.meter = m } }

// WithSpanName overrides the default span name ("gomailer.send").
func WithSpanName(name string) Option { return func(c *obsConfig) { c.spanName = name } }

// Observability returns a Middleware that wraps each Send with a span (if a
// Tracer is configured) and metrics (if a Meter is configured). It is fully
// nil-safe: with neither a Tracer nor a Meter it returns a cheap pass-through
// Middleware (identity), so callers can wire it unconditionally.
func Observability(opts ...Option) Middleware {
	cfg := obsConfig{spanName: "gomailer.send"}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.tracer == nil && cfg.meter == nil {
		// Cheap pass-through: nothing to observe.
		return func(t gomailer.Transport) gomailer.Transport { return t }
	}
	return func(next gomailer.Transport) gomailer.Transport {
		return &obsTransport{next: next, cfg: cfg}
	}
}

// obsTransport is the decorating transport built by Observability. It records
// one span and one metric sample per leaf Send (every failover retry, being a
// separate leaf Send, is observed independently).
type obsTransport struct {
	next gomailer.Transport
	cfg  obsConfig
}

// String delegates to the wrapped transport so the decorator is transparent to
// RoundRobin/Failover identity logic.
func (o *obsTransport) String() string { return o.next.String() }

// Send wraps next.Send with a span and metrics. Attribute derivation:
//   - messaging.system          = "gomailer" (constant)
//   - messaging.destination.name = transport name from o.next.String()
//   - messaging.recipient_count  from envelope (when non-nil at call time)
//   - messaging.message.id       set AFTER a successful Send (SentMessage.MessageID)
//   - messaging.gomailer.outcome ("success"|"error") on metrics and as a span attribute
//
// On failure the span records the error and is marked Error; the duration
// histogram and send counter are still recorded with outcome="error". The error
// is returned UNCHANGED so errors.Is(err, gomailer.ErrTransport) and
// errors.As to *gomailer.TransportError keep working for callers and outer
// middleware. A nil *SentMessage with nil error (listener rejection) is treated
// as a success outcome with no message-id and passes through untouched.
func (o *obsTransport) Send(ctx context.Context, msg gomailer.RawMessage, envelope *gomailer.Envelope) (*gomailer.SentMessage, error) {
	transportName := o.next.String()

	var span Span
	if o.cfg.tracer != nil {
		ctx, span = o.cfg.tracer.Start(ctx, o.cfg.spanName)
		span.SetAttributes(
			String("messaging.system", "gomailer"),
			String("messaging.destination.name", transportName),
		)
		if envelope != nil {
			span.SetAttributes(Int("messaging.recipient_count", int64(len(envelope.Recipients()))))
		}
		defer span.End()
	}

	start := time.Now()
	sm, err := o.next.Send(ctx, msg, envelope)
	elapsedMs := float64(time.Since(start)) / float64(time.Millisecond)

	outcome := "success"
	if err != nil {
		outcome = "error"
	}

	if span != nil {
		span.SetAttributes(String("messaging.gomailer.outcome", outcome))
		if envelope == nil && sm != nil && sm.Envelope() != nil {
			span.SetAttributes(Int("messaging.recipient_count", int64(len(sm.Envelope().Recipients()))))
		}
		if err != nil {
			span.RecordError(err)
			span.SetError(err.Error())
		} else if sm != nil && sm.MessageID() != "" {
			span.SetAttributes(String("messaging.message.id", sm.MessageID()))
		}
	}

	if o.cfg.meter != nil {
		attrs := []Attr{
			String("messaging.destination.name", transportName),
			String("messaging.gomailer.outcome", outcome),
		}
		o.cfg.meter.SendCounter().Add(ctx, 1, attrs...)
		o.cfg.meter.DurationHistogram().Record(ctx, elapsedMs, attrs...)
	}

	return sm, err
}

// Compile-time assertion that obsTransport satisfies gomailer.Transport.
var _ gomailer.Transport = (*obsTransport)(nil)
