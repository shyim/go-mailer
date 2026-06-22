package middleware_test

import (
	"context"
	"fmt"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/middleware"
)

// stdoutTracer is a tiny, dependency-free middleware.Tracer that prints span
// lifecycle to stdout. A real deployment uses middleware/otelmw instead; this
// keeps the example free of any OpenTelemetry dependency.
type stdoutTracer struct{}

func (stdoutTracer) Start(ctx context.Context, name string) (context.Context, middleware.Span) {
	fmt.Printf("span start: %s\n", name)
	return ctx, &stdoutSpan{}
}

type stdoutSpan struct{}

func (*stdoutSpan) SetAttributes(attrs ...middleware.Attr) {
	for _, a := range attrs {
		switch a.Kind {
		case middleware.KindInt:
			fmt.Printf("  attr %s=%d\n", a.Key, a.Int)
		case middleware.KindBool:
			fmt.Printf("  attr %s=%t\n", a.Key, a.Bool)
		default:
			fmt.Printf("  attr %s=%s\n", a.Key, a.Str)
		}
	}
}

func (*stdoutSpan) RecordError(err error)       { fmt.Printf("  error: %v\n", err) }
func (*stdoutSpan) SetError(description string) { fmt.Printf("  status=error: %s\n", description) }
func (*stdoutSpan) End()                        { fmt.Println("span end") }

// exampleTransport is a stand-in leaf transport. In real code this is an
// smtp/sendmail/null transport (or a leaf under RoundRobin/Failover).
type exampleTransport struct{}

func (exampleTransport) String() string { return "null://" }

func (exampleTransport) Send(ctx context.Context, msg gomailer.RawMessage, env *gomailer.Envelope) (*gomailer.SentMessage, error) {
	sm := &gomailer.SentMessage{}
	sm.SetMessageID("<demo@host>")
	return sm, nil
}

// Example shows wiring the Observability middleware with a dependency-free
// Tracer. Production code substitutes otelmw.New(tracerProvider, meterProvider)
// for the WithTracer/WithMeter options and wraps each leaf transport so every
// delivery attempt (including failover retries) is observed independently.
func Example() {
	leaf := exampleTransport{}

	t := middleware.Wrap(leaf, middleware.Observability(
		middleware.WithTracer(stdoutTracer{}),
		// middleware.WithMeter(...) // omitted to keep output deterministic.
	))

	from, _ := gomailer.NewAddress("from@example.com", "")
	to, _ := gomailer.NewAddress("to@example.com", "")
	env, _ := gomailer.NewEnvelope(from, []gomailer.Address{to})

	sm, err := t.Send(context.Background(), nil, env)
	if err != nil {
		fmt.Println("send failed:", err)
		return
	}
	fmt.Println("sent:", sm.MessageID())

	// Output:
	// span start: gomailer.send
	//   attr messaging.system=gomailer
	//   attr messaging.destination.name=null://
	//   attr messaging.recipient_count=1
	//   attr messaging.gomailer.outcome=success
	//   attr messaging.message.id=<demo@host>
	// span end
	// sent: <demo@host>
}
