# Observability (OTLP)

gomailer exposes OpenTelemetry traces and metrics through a transport
decorator. The wiring is deliberately split in two so the core stays
dependency-free:

- **`middleware`** is STDLIB-ONLY. It defines the provider-agnostic
  `Tracer`/`Span` and `Meter`/`Counter`/`Histogram` interfaces and the
  `Observability(...)` middleware. Importing it pulls in **no** third-party
  dependencies.
- **`middleware/otelmw`** is a **separate Go module** that adapts a real
  OpenTelemetry `TracerProvider` / `MeterProvider` to those interfaces. It is the
  *only* package that imports `go.opentelemetry.io/otel`.

!!! note "Zero-dep promise"
    The root module's `go.mod` has no `go.opentelemetry.io/*` requirement. A
    user who imports only the root module never pulls OTel into their dependency
    graph. OpenTelemetry enters exclusively through the `otelmw` submodule.

## Install

OTel only enters your dependency graph when you `go get` the `otelmw` module:

```sh
go get github.com/shyim/go-mailer/middleware/otelmw
```

## Wrap the leaf transport

`otelmw.New(tp, mp)` returns a `middleware.Middleware`. Wrap a **leaf**
transport with it via `middleware.Wrap`. The decorator's `String()` delegates to
the wrapped transport, so composite routers
([RoundRobin / Failover / Transports](../concepts/transports.md)) see unchanged
identities. Because routers loop over their leaves and call `leaf.Send`, wrapping
**each leaf** means every delivery attempt — including each failover retry —
gets its own span and metric sample.

```go
import (
	"github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/middleware"
	"github.com/shyim/go-mailer/middleware/otelmw"
	"github.com/shyim/go-mailer/transport"
)

mw := otelmw.New(tracerProvider, meterProvider) // a middleware.Middleware

// Single transport:
t := middleware.Wrap(leaf, mw)

// Composites — wrap each LEAF, not the composite, so every attempt is its own
// span. Use Chain to reuse the same stack:
stack := middleware.Chain(mw)
fo, _ := transport.NewFailoverTransport([]gomailer.Transport{
	stack(primaryLeaf),
	stack(backupLeaf),
}, 0)
```

!!! warning "Wrap leaves, not composites"
    Wrapping the composite yields a single span covering all retries. Wrapping
    each leaf is the intended ergonomic and gives one span per attempt.

!!! tip "Safe to wire unconditionally"
    `middleware.Observability()` with no providers is a cheap pass-through
    (returns the transport unchanged), and a nil `Tracer` or `Meter` simply
    disables that signal.

## Signals emitted

One span per leaf `Send`, named `gomailer.send` (`SpanKindClient`), with:

| Attribute | Source |
|---|---|
| `messaging.system` | constant `"gomailer"` |
| `messaging.destination.name` | leaf `Transport.String()` |
| `messaging.recipient_count` | `len(envelope.Recipients())` when the envelope is non-nil |
| `messaging.message.id` | `SentMessage.MessageID()`, set only after a successful send |
| `messaging.gomailer.outcome` | `"success"` or `"error"` |

On failure the span records the error event and is marked `codes.Error`. The
original error is returned **unchanged**, so `errors.Is(err, gomailer.ErrTransport)`
and `errors.As(err, &te)` (where `te` is `*gomailer.TransportError`) keep working
for callers and outer middleware.

Two instruments (scope `github.com/shyim/go-mailer/middleware/otelmw`):

- `gomailer.send.count` — `Int64` counter, unit `{message}`, attributed by
  `messaging.destination.name` and `messaging.gomailer.outcome`.
- `gomailer.send.duration` — `Float64` histogram, unit `ms`, same attributes.

Both success and failure increment the counter and record latency.

## OTLP gRPC exporter wiring

The snippet below lives in the **otelmw-module context** — the OTLP exporter
modules are *not* in gomailer's `go.mod`. Add them to **your** module:

```sh
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
go get go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc
go get go.opentelemetry.io/otel/sdk go.opentelemetry.io/otel/sdk/metric
```

```go
package main

import (
	"context"
	"time"

	"github.com/shyim/go-mailer/middleware"
	"github.com/shyim/go-mailer/middleware/otelmw"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func setupObservability(ctx context.Context) (middleware.Middleware, func(context.Context) error, error) {
	traceExp, err := otlptracegrpc.New(ctx) // honors OTEL_EXPORTER_OTLP_ENDPOINT
	if err != nil {
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traceExp))

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, nil, err
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(
		sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(10*time.Second)),
	))

	shutdown := func(ctx context.Context) error {
		_ = tp.Shutdown(ctx)
		return mp.Shutdown(ctx)
	}
	return otelmw.New(tp, mp), shutdown, nil
}
```

Then wrap each leaf transport with the returned middleware as shown in
[Wrap the leaf transport](#wrap-the-leaf-transport), and call `shutdown` on
program exit to flush the batched exporters.

!!! note "Provider fallback"
    Either provider may be nil: `otelmw.New(nil, nil)` falls back to
    `otel.GetTracerProvider()` / `otel.GetMeterProvider()`, so you can rely on
    the OTel globals. The options `otelmw.WithTracerProvider` /
    `otelmw.WithMeterProvider` override the positional providers.

A runnable, dependency-free version (using a stdout `Tracer`) is in
`middleware/example_test.go`.

See also: [Middleware](../concepts/middleware.md) ·
[Transports](../concepts/transports.md).
