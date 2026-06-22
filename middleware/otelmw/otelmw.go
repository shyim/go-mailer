// Package otelmw adapts an OpenTelemetry TracerProvider and MeterProvider to
// the provider-agnostic observability interfaces defined in the core
// "middleware" package.
//
// It is the ONLY package in this module that imports go.opentelemetry.io/otel.
// Importing otelmw is what pulls OpenTelemetry into a user's dependency graph;
// a user who only imports "middleware" gets zero OTel dependencies.
//
// Typical wiring:
//
//	mw := otelmw.New(tracerProvider, meterProvider)
//	t := middleware.Wrap(leafTransport, mw)
//
// New returns a middleware.Middleware, so it composes with middleware.Wrap
// exactly like any other middleware. Because the wrap point is each leaf
// transport, every Send attempt (including failover retries) produces its own
// span and metric sample.
package otelmw

import (
	"context"

	"github.com/shyim/go-mailer/middleware"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName is the instrumentation scope name reported to the
// Tracer/Meter providers. It identifies spans and metrics as originating from
// this adapter.
const instrumentationName = "github.com/shyim/go-mailer/middleware/otelmw"

// config holds the resolved providers and options used to build the adapter.
type config struct {
	tp trace.TracerProvider
	mp metric.MeterProvider
}

// Option configures New.
type Option func(*config)

// WithTracerProvider sets the OpenTelemetry TracerProvider explicitly. It takes
// precedence over the provider passed positionally to New. A nil provider is
// ignored (the positional/global fallback applies).
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) {
		if tp != nil {
			c.tp = tp
		}
	}
}

// WithMeterProvider sets the OpenTelemetry MeterProvider explicitly. It takes
// precedence over the provider passed positionally to New. A nil provider is
// ignored (the positional/global fallback applies).
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(c *config) {
		if mp != nil {
			c.mp = mp
		}
	}
}

// New builds a middleware.Middleware that records one span and one metric
// sample per leaf Send using the given OpenTelemetry providers.
//
// Either provider may be nil: a nil tp falls back to otel.GetTracerProvider()
// and a nil mp falls back to otel.GetMeterProvider(), so callers relying on the
// OTel globals can pass New(nil, nil). Options may override the providers.
//
// The returned Middleware always supplies both a Tracer and a Meter to the core
// Observability middleware; whether spans/metrics are actually exported depends
// on the resolved providers (e.g. a no-op provider yields no-op instruments).
func New(tp trace.TracerProvider, mp metric.MeterProvider, opts ...Option) middleware.Middleware {
	cfg := config{tp: tp, mp: mp}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.tp == nil {
		cfg.tp = otel.GetTracerProvider()
	}
	if cfg.mp == nil {
		cfg.mp = otel.GetMeterProvider()
	}

	tracer := &otelTracer{tracer: cfg.tp.Tracer(instrumentationName)}
	meter := newOtelMeter(cfg.mp.Meter(instrumentationName))

	return middleware.Observability(
		middleware.WithTracer(tracer),
		middleware.WithMeter(meter),
	)
}

// --- attribute translation ---

// toKeyValues converts core middleware.Attr values into OTel attribute.KeyValue
// values, selecting the active field by Kind.
func toKeyValues(attrs []middleware.Attr) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	kvs := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		switch a.Kind {
		case middleware.KindInt:
			kvs = append(kvs, attribute.Int64(a.Key, a.Int))
		case middleware.KindBool:
			kvs = append(kvs, attribute.Bool(a.Key, a.Bool))
		default: // middleware.KindString
			kvs = append(kvs, attribute.String(a.Key, a.Str))
		}
	}
	return kvs
}

// --- tracer adapter ---

// otelTracer adapts a trace.Tracer to middleware.Tracer. The trace.Tracer is
// obtained once from the provider at construction.
type otelTracer struct {
	tracer trace.Tracer
}

// Start begins an OTel span and returns the span-carrying context plus an
// otelSpan handle. It never returns a nil Span, satisfying the core contract.
func (t *otelTracer) Start(ctx context.Context, name string) (context.Context, middleware.Span) {
	ctx, span := t.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient))
	return ctx, &otelSpan{span: span}
}

// otelSpan adapts a trace.Span to middleware.Span.
type otelSpan struct {
	span trace.Span
}

// SetAttributes attaches translated attributes to the underlying span.
func (s *otelSpan) SetAttributes(attrs ...middleware.Attr) {
	if len(attrs) == 0 {
		return
	}
	s.span.SetAttributes(toKeyValues(attrs)...)
}

// RecordError records err as a span event without changing the span status.
func (s *otelSpan) RecordError(err error) {
	s.span.RecordError(err)
}

// SetError marks the span status as Error with the given description.
func (s *otelSpan) SetError(description string) {
	s.span.SetStatus(codes.Error, description)
}

// End finishes the underlying span. Safe to call exactly once.
func (s *otelSpan) End() {
	s.span.End()
}

// --- meter adapter ---

// otelMeter adapts a metric.Meter to middleware.Meter. Both instruments are
// created eagerly once at construction so SendCounter/DurationHistogram return
// the same instance on every call, giving stable instrument identity.
type otelMeter struct {
	counter   middleware.Counter
	histogram middleware.Histogram
}

// newOtelMeter creates the counter and histogram instruments from m. If
// instrument creation fails, a no-op instrument is substituted so recording
// never panics and the middleware stays side-effect free.
func newOtelMeter(m metric.Meter) *otelMeter {
	counter, err := m.Int64Counter(
		"gomailer.send.count",
		metric.WithUnit("{message}"),
		metric.WithDescription("Number of Send attempts, partitioned by transport and outcome."),
	)
	if err != nil {
		counter = nil
	}

	hist, err := m.Float64Histogram(
		"gomailer.send.duration",
		metric.WithUnit("ms"),
		metric.WithDescription("Send latency in milliseconds, partitioned by transport and outcome."),
	)
	if err != nil {
		hist = nil
	}

	return &otelMeter{
		counter:   &otelCounter{counter: counter},
		histogram: &otelHistogram{hist: hist},
	}
}

// SendCounter returns the cached send counter instrument.
func (m *otelMeter) SendCounter() middleware.Counter { return m.counter }

// DurationHistogram returns the cached duration histogram instrument.
func (m *otelMeter) DurationHistogram() middleware.Histogram { return m.histogram }

// otelCounter adapts a metric.Int64Counter to middleware.Counter. A nil counter
// (creation failed) makes Add a no-op.
type otelCounter struct {
	counter metric.Int64Counter
}

// Add increments the counter by delta with the translated attributes.
func (c *otelCounter) Add(ctx context.Context, delta int64, attrs ...middleware.Attr) {
	if c.counter == nil {
		return
	}
	c.counter.Add(ctx, delta, metric.WithAttributes(toKeyValues(attrs)...))
}

// otelHistogram adapts a metric.Float64Histogram to middleware.Histogram. A nil
// histogram (creation failed) makes Record a no-op.
type otelHistogram struct {
	hist metric.Float64Histogram
}

// Record records value with the translated attributes.
func (h *otelHistogram) Record(ctx context.Context, value float64, attrs ...middleware.Attr) {
	if h.hist == nil {
		return
	}
	h.hist.Record(ctx, value, metric.WithAttributes(toKeyValues(attrs)...))
}
