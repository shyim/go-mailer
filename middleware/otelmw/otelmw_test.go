package otelmw_test

import (
	"context"
	"errors"
	"testing"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/middleware"
	"github.com/shyim/go-mailer/middleware/otelmw"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// fakeTransport is a leaf transport whose Send result is configurable.
type fakeTransport struct {
	name      string
	messageID string
	err       error
}

func (f *fakeTransport) String() string { return f.name }

func (f *fakeTransport) Send(ctx context.Context, msg gomailer.RawMessage, env *gomailer.Envelope) (*gomailer.SentMessage, error) {
	if f.err != nil {
		return nil, f.err
	}
	sm := &gomailer.SentMessage{}
	sm.SetMessageID(f.messageID)
	return sm, nil
}

// findAttr returns the value of the named attribute from a span's attributes.
func findAttr(attrs []attribute.KeyValue, key string) (attribute.Value, bool) {
	for _, kv := range attrs {
		if string(kv.Key) == key {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestNew_SuccessSpanAndMetrics(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(sr))

	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))

	mw := otelmw.New(tp, mp)
	leaf := &fakeTransport{name: "smtp://host", messageID: "<abc@host>"}
	wrapped := middleware.Wrap(leaf, mw)

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
		t.Fatalf("Send returned error: %v", sendErr)
	}
	if sm == nil || sm.MessageID() != "<abc@host>" {
		t.Fatalf("unexpected SentMessage: %v", sm)
	}

	// --- span assertions ---
	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	span := spans[0]
	if span.Name() != "gomailer.send" {
		t.Errorf("span name = %q, want gomailer.send", span.Name())
	}
	if span.Status().Code != codes.Unset {
		t.Errorf("status code = %v, want Unset on success", span.Status().Code)
	}
	attrs := span.Attributes()
	if v, ok := findAttr(attrs, "messaging.system"); !ok || v.AsString() != "gomailer" {
		t.Errorf("messaging.system = %v (ok=%v), want gomailer", v, ok)
	}
	if v, ok := findAttr(attrs, "messaging.destination.name"); !ok || v.AsString() != "smtp://host" {
		t.Errorf("messaging.destination.name = %v (ok=%v), want smtp://host", v, ok)
	}
	if v, ok := findAttr(attrs, "messaging.recipient_count"); !ok || v.AsInt64() != 2 {
		t.Errorf("messaging.recipient_count = %v (ok=%v), want 2", v, ok)
	}
	if v, ok := findAttr(attrs, "messaging.gomailer.outcome"); !ok || v.AsString() != "success" {
		t.Errorf("outcome = %v (ok=%v), want success", v, ok)
	}
	if v, ok := findAttr(attrs, "messaging.message.id"); !ok || v.AsString() != "<abc@host>" {
		t.Errorf("messaging.message.id = %v (ok=%v), want <abc@host>", v, ok)
	}

	// --- metric assertions ---
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	gotCount, gotHist := false, false
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch m.Name {
			case "gomailer.send.count":
				gotCount = true
				if m.Unit != "{message}" {
					t.Errorf("count unit = %q, want {message}", m.Unit)
				}
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("count data type = %T, want Sum[int64]", m.Data)
				}
				if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
					t.Errorf("count datapoints = %+v, want single value 1", sum.DataPoints)
				}
			case "gomailer.send.duration":
				gotHist = true
				if m.Unit != "ms" {
					t.Errorf("duration unit = %q, want ms", m.Unit)
				}
				h, ok := m.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("duration data type = %T, want Histogram[float64]", m.Data)
				}
				if len(h.DataPoints) != 1 || h.DataPoints[0].Count != 1 {
					t.Errorf("duration datapoints = %+v, want single count 1", h.DataPoints)
				}
			}
		}
	}
	if !gotCount || !gotHist {
		t.Errorf("missing instruments: count=%v hist=%v", gotCount, gotHist)
	}
}

func TestNew_FailureMarksSpanError(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(sr))
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))

	sendErr := errors.New("boom")
	mw := otelmw.New(tp, mp)
	leaf := &fakeTransport{name: "smtp://host", err: sendErr}
	wrapped := middleware.Wrap(leaf, mw)

	_, err := wrapped.Send(context.Background(), nil, nil)
	if !errors.Is(err, sendErr) {
		t.Fatalf("error not propagated unchanged: %v", err)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	span := spans[0]
	if span.Status().Code != codes.Error {
		t.Errorf("status code = %v, want Error", span.Status().Code)
	}
	if span.Status().Description != "boom" {
		t.Errorf("status description = %q, want boom", span.Status().Description)
	}
	if v, ok := findAttr(span.Attributes(), "messaging.gomailer.outcome"); !ok || v.AsString() != "error" {
		t.Errorf("outcome = %v (ok=%v), want error", v, ok)
	}
	if _, ok := findAttr(span.Attributes(), "messaging.message.id"); ok {
		t.Error("message.id should be absent on failure")
	}
	if len(span.Events()) == 0 {
		t.Error("expected a recorded error event")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "gomailer.send.count" {
				continue
			}
			sum := m.Data.(metricdata.Sum[int64])
			if len(sum.DataPoints) != 1 {
				t.Fatalf("want 1 datapoint, got %d", len(sum.DataPoints))
			}
			v, ok := sum.DataPoints[0].Attributes.Value("messaging.gomailer.outcome")
			if !ok || v.AsString() != "error" {
				t.Errorf("counter outcome attr = %v (ok=%v), want error", v, ok)
			}
		}
	}
}

// TestNew_NilProviders verifies New tolerates nil providers by falling back to
// the OTel globals (no-op by default) without panicking.
func TestNew_NilProviders(t *testing.T) {
	mw := otelmw.New(nil, nil)
	leaf := &fakeTransport{name: "null://", messageID: "<x@y>"}
	wrapped := middleware.Wrap(leaf, mw)
	if _, err := wrapped.Send(context.Background(), nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if wrapped.String() != "null://" {
		t.Errorf("String() = %q, want null:// (delegated)", wrapped.String())
	}
}
