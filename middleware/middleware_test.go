package middleware_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/middleware"
)

// --- fakes ---

// fakeTransport is a leaf transport whose Send result is configurable. It also
// records the contexts it is invoked with so tests can assert span propagation.
type fakeTransport struct {
	name string
	sm   *gomailer.SentMessage
	err  error

	calls   int
	lastCtx context.Context
	lastMsg gomailer.RawMessage
	// onSend, if set, is invoked with the envelope the transport receives, so
	// tests can assert that upstream middleware mutations reached the leaf.
	onSend func(env *gomailer.Envelope)
}

func (f *fakeTransport) String() string { return f.name }

func (f *fakeTransport) Send(ctx context.Context, msg gomailer.RawMessage, env *gomailer.Envelope) (*gomailer.SentMessage, error) {
	f.calls++
	f.lastCtx = ctx
	f.lastMsg = msg
	if f.onSend != nil {
		f.onSend(env)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.sm, nil
}

// spanCtxKey marks a context as carrying the fake span, so the leaf can prove it
// ran under the span-derived context.
type spanCtxKey struct{}

// fakeSpan records the calls made against it.
type fakeSpan struct {
	mu       sync.Mutex
	attrs    []middleware.Attr
	recorded []error
	errDesc  string
	hadError bool
	ended    bool
	endCount int
}

func (s *fakeSpan) SetAttributes(attrs ...middleware.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs = append(s.attrs, attrs...)
}

func (s *fakeSpan) RecordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorded = append(s.recorded, err)
}

func (s *fakeSpan) SetError(description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hadError = true
	s.errDesc = description
}

func (s *fakeSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	s.endCount++
}

func (s *fakeSpan) attr(key string) (middleware.Attr, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.attrs {
		if a.Key == key {
			return a, true
		}
	}
	return middleware.Attr{}, false
}

// fakeTracer hands out a single fakeSpan and tags the returned context.
type fakeTracer struct {
	span      *fakeSpan
	startName string
	starts    int
}

func (t *fakeTracer) Start(ctx context.Context, name string) (context.Context, middleware.Span) {
	t.starts++
	t.startName = name
	if t.span == nil {
		t.span = &fakeSpan{}
	}
	ctx = context.WithValue(ctx, spanCtxKey{}, t.span)
	return ctx, t.span
}

// fakeCounter records every Add call.
type fakeCounter struct {
	mu     sync.Mutex
	deltas []int64
	attrs  [][]middleware.Attr
}

func (c *fakeCounter) Add(_ context.Context, delta int64, attrs ...middleware.Attr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deltas = append(c.deltas, delta)
	c.attrs = append(c.attrs, attrs)
}

// fakeHistogram records every Record call.
type fakeHistogram struct {
	mu     sync.Mutex
	values []float64
	attrs  [][]middleware.Attr
}

func (h *fakeHistogram) Record(_ context.Context, value float64, attrs ...middleware.Attr) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.values = append(h.values, value)
	h.attrs = append(h.attrs, attrs)
}

// fakeMeter returns the same instruments on every call (stable identity).
type fakeMeter struct {
	counter *fakeCounter
	hist    *fakeHistogram
}

func newFakeMeter() *fakeMeter {
	return &fakeMeter{counter: &fakeCounter{}, hist: &fakeHistogram{}}
}

func (m *fakeMeter) SendCounter() middleware.Counter         { return m.counter }
func (m *fakeMeter) DurationHistogram() middleware.Histogram { return m.hist }

func attrInList(attrs []middleware.Attr, key string) (middleware.Attr, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a, true
		}
	}
	return middleware.Attr{}, false
}

// --- Wrap / Chain ordering ---

// recordingMiddleware appends its label to a shared order slice both on the way
// in (when the transport is built / called) so tests can verify layering.
func recordingMiddleware(order *[]string, label string) middleware.Middleware {
	return func(next gomailer.Transport) gomailer.Transport {
		return &recordingTransport{next: next, order: order, label: label}
	}
}

type recordingTransport struct {
	next  gomailer.Transport
	order *[]string
	label string
}

func (r *recordingTransport) String() string { return r.next.String() }

func (r *recordingTransport) Send(ctx context.Context, msg gomailer.RawMessage, env *gomailer.Envelope) (*gomailer.SentMessage, error) {
	*r.order = append(*r.order, r.label)
	return r.next.Send(ctx, msg, env)
}

func TestWrap_OrderOutermostFirst(t *testing.T) {
	var order []string
	leaf := &fakeTransport{name: "leaf"}
	wrapped := middleware.Wrap(leaf,
		recordingMiddleware(&order, "A"),
		recordingMiddleware(&order, "B"),
		recordingMiddleware(&order, "C"),
	)
	if _, err := wrapped.Send(context.Background(), nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// First listed (A) must run first => outermost.
	want := []string{"A", "B", "C"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestWrap_NoMiddlewaresReturnsSame(t *testing.T) {
	leaf := &fakeTransport{name: "leaf"}
	if got := middleware.Wrap(leaf); got != gomailer.Transport(leaf) {
		t.Fatalf("Wrap(t) returned a different transport")
	}
}

func TestWrap_SkipsNilEntries(t *testing.T) {
	var order []string
	leaf := &fakeTransport{name: "leaf"}
	wrapped := middleware.Wrap(leaf,
		nil,
		recordingMiddleware(&order, "A"),
		nil,
		recordingMiddleware(&order, "B"),
		nil,
	)
	if _, err := wrapped.Send(context.Background(), nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(order) != 2 || order[0] != "A" || order[1] != "B" {
		t.Fatalf("order = %v, want [A B]", order)
	}
}

func TestChain_EquivalentToWrap(t *testing.T) {
	var order []string
	stack := middleware.Chain(
		recordingMiddleware(&order, "A"),
		recordingMiddleware(&order, "B"),
	)
	leaf := &fakeTransport{name: "leaf"}
	wrapped := stack(leaf)
	if _, err := wrapped.Send(context.Background(), nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(order) != 2 || order[0] != "A" || order[1] != "B" {
		t.Fatalf("order = %v, want [A B]", order)
	}
}

// --- Observability nil-safety: pass-through ---

func TestObservability_NoProvidersPassesThrough(t *testing.T) {
	wantSM := &gomailer.SentMessage{}
	wantSM.SetMessageID("<id@host>")
	leaf := &fakeTransport{name: "leaf", sm: wantSM}

	// No WithTracer / WithMeter => identity middleware.
	wrapped := middleware.Wrap(leaf, middleware.Observability())

	// Identity must return the SAME underlying transport, not a wrapper.
	if wrapped != gomailer.Transport(leaf) {
		t.Fatalf("Observability() with no providers should be identity, got wrapper")
	}

	sm, err := wrapped.Send(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sm != wantSM {
		t.Fatalf("SentMessage not passed through unchanged")
	}
	if leaf.calls != 1 {
		t.Fatalf("leaf calls = %d, want 1", leaf.calls)
	}
}

func TestObservability_NoProvidersPropagatesError(t *testing.T) {
	sendErr := errors.New("boom")
	leaf := &fakeTransport{name: "leaf", err: sendErr}
	wrapped := middleware.Wrap(leaf, middleware.Observability())

	_, err := wrapped.Send(context.Background(), nil, nil)
	if !errors.Is(err, sendErr) {
		t.Fatalf("error = %v, want wrapped %v", err, sendErr)
	}
}

// --- Observability success path with fakes ---

func TestObservability_SuccessRecordsSpanAndMetrics(t *testing.T) {
	wantSM := &gomailer.SentMessage{}
	wantSM.SetMessageID("<abc@host>")
	leaf := &fakeTransport{name: "smtp://host", sm: wantSM}

	tracer := &fakeTracer{}
	meter := newFakeMeter()

	wrapped := middleware.Wrap(leaf, middleware.Observability(
		middleware.WithTracer(tracer),
		middleware.WithMeter(meter),
	))

	from, err := gomailer.NewAddress("from@example.com", "")
	if err != nil {
		t.Fatalf("NewAddress: %v", err)
	}
	to1, err := gomailer.NewAddress("a@example.com", "")
	if err != nil {
		t.Fatalf("NewAddress: %v", err)
	}
	to2, err := gomailer.NewAddress("b@example.com", "")
	if err != nil {
		t.Fatalf("NewAddress: %v", err)
	}
	env, err := gomailer.NewEnvelope(from, []gomailer.Address{to1, to2})
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	sm, sendErr := wrapped.Send(context.Background(), nil, env)
	if sendErr != nil {
		t.Fatalf("Send: %v", sendErr)
	}
	if sm != wantSM {
		t.Fatalf("SentMessage not passed through")
	}

	// span
	if tracer.starts != 1 {
		t.Fatalf("tracer.starts = %d, want 1", tracer.starts)
	}
	if tracer.startName != "gomailer.send" {
		t.Errorf("span name = %q, want gomailer.send", tracer.startName)
	}
	span := tracer.span
	if !span.ended || span.endCount != 1 {
		t.Errorf("span End count = %d, want 1", span.endCount)
	}
	if span.hadError {
		t.Error("span marked Error on success")
	}
	if len(span.recorded) != 0 {
		t.Errorf("recorded errors = %v, want none", span.recorded)
	}
	if a, ok := span.attr("messaging.system"); !ok || a.Str != "gomailer" {
		t.Errorf("messaging.system = %+v (ok=%v)", a, ok)
	}
	if a, ok := span.attr("messaging.destination.name"); !ok || a.Str != "smtp://host" {
		t.Errorf("destination = %+v (ok=%v)", a, ok)
	}
	if a, ok := span.attr("messaging.recipient_count"); !ok || a.Int != 2 {
		t.Errorf("recipient_count = %+v (ok=%v)", a, ok)
	}
	if a, ok := span.attr("messaging.gomailer.outcome"); !ok || a.Str != "success" {
		t.Errorf("outcome = %+v (ok=%v)", a, ok)
	}
	if a, ok := span.attr("messaging.message.id"); !ok || a.Str != "<abc@host>" {
		t.Errorf("message.id = %+v (ok=%v)", a, ok)
	}

	// span context propagation: leaf must run under the span-tagged context.
	if leaf.lastCtx == nil || leaf.lastCtx.Value(spanCtxKey{}) != span {
		t.Error("leaf Send did not run under span-derived context")
	}

	// metrics
	if len(meter.counter.deltas) != 1 || meter.counter.deltas[0] != 1 {
		t.Errorf("counter deltas = %v, want [1]", meter.counter.deltas)
	}
	if a, ok := attrInList(meter.counter.attrs[0], "messaging.gomailer.outcome"); !ok || a.Str != "success" {
		t.Errorf("counter outcome = %+v (ok=%v)", a, ok)
	}
	if len(meter.hist.values) != 1 {
		t.Errorf("histogram records = %v, want 1", meter.hist.values)
	}
	if a, ok := attrInList(meter.hist.attrs[0], "messaging.destination.name"); !ok || a.Str != "smtp://host" {
		t.Errorf("histogram destination = %+v (ok=%v)", a, ok)
	}
}

// --- Observability failure path with fakes ---

func TestObservability_FailureMarksSpanAndCountsError(t *testing.T) {
	sendErr := gomailer.NewTransportError("delivery failed")
	leaf := &fakeTransport{name: "smtp://host", err: sendErr}

	tracer := &fakeTracer{}
	meter := newFakeMeter()

	wrapped := middleware.Wrap(leaf, middleware.Observability(
		middleware.WithTracer(tracer),
		middleware.WithMeter(meter),
	))

	sm, err := wrapped.Send(context.Background(), nil, nil)
	if sm != nil {
		t.Errorf("SentMessage = %v, want nil on failure", sm)
	}
	// Error must propagate UNCHANGED so classification keeps working.
	if !errors.Is(err, gomailer.ErrTransport) {
		t.Fatalf("error no longer satisfies ErrTransport: %v", err)
	}
	var te *gomailer.TransportError
	if !errors.As(err, &te) {
		t.Fatalf("error no longer *TransportError: %v", err)
	}

	span := tracer.span
	if !span.hadError {
		t.Error("span not marked Error on failure")
	}
	if span.errDesc != sendErr.Error() {
		t.Errorf("error description = %q, want %q", span.errDesc, sendErr.Error())
	}
	if len(span.recorded) != 1 || !errors.Is(span.recorded[0], gomailer.ErrTransport) {
		t.Errorf("recorded errors = %v, want one transport error", span.recorded)
	}
	if !span.ended {
		t.Error("span not ended on failure")
	}
	if _, ok := span.attr("messaging.message.id"); ok {
		t.Error("message.id must be absent on failure")
	}
	if a, ok := span.attr("messaging.gomailer.outcome"); !ok || a.Str != "error" {
		t.Errorf("span outcome = %+v (ok=%v), want error", a, ok)
	}

	// failure path still increments the counter, attributed outcome=error.
	if len(meter.counter.deltas) != 1 || meter.counter.deltas[0] != 1 {
		t.Errorf("counter deltas = %v, want [1] on failure", meter.counter.deltas)
	}
	if a, ok := attrInList(meter.counter.attrs[0], "messaging.gomailer.outcome"); !ok || a.Str != "error" {
		t.Errorf("counter outcome = %+v (ok=%v), want error", a, ok)
	}
	if len(meter.hist.values) != 1 {
		t.Errorf("histogram records = %v, want 1 on failure", meter.hist.values)
	}
}

// --- Observability with only one provider (partial wiring) ---

func TestObservability_TracerOnly(t *testing.T) {
	leaf := &fakeTransport{name: "leaf", sm: &gomailer.SentMessage{}}
	tracer := &fakeTracer{}
	wrapped := middleware.Wrap(leaf, middleware.Observability(middleware.WithTracer(tracer)))
	if _, err := wrapped.Send(context.Background(), nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if tracer.starts != 1 {
		t.Errorf("tracer.starts = %d, want 1", tracer.starts)
	}
}

func TestObservability_MeterOnly(t *testing.T) {
	leaf := &fakeTransport{name: "leaf", sm: &gomailer.SentMessage{}}
	meter := newFakeMeter()
	wrapped := middleware.Wrap(leaf, middleware.Observability(middleware.WithMeter(meter)))
	if _, err := wrapped.Send(context.Background(), nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(meter.counter.deltas) != 1 {
		t.Errorf("counter deltas = %v, want one", meter.counter.deltas)
	}
}

// String must delegate to the wrapped transport so RoundRobin/Failover identity
// logic is unaffected by the decorator.
func TestObservability_StringDelegates(t *testing.T) {
	leaf := &fakeTransport{name: "smtp://host"}
	wrapped := middleware.Wrap(leaf, middleware.Observability(middleware.WithTracer(&fakeTracer{})))
	if got := wrapped.String(); got != "smtp://host" {
		t.Errorf("String() = %q, want smtp://host", got)
	}
}
