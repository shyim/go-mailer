# Middleware & hooks

gomailer composes cross-cutting send behavior with a small **transport-decorator**
pipeline. A middleware wraps one `gomailer.Transport` and returns another, so it
can mutate, reject, or observe each send without the leaf transport knowing.

Two ready-made decorators cover the common hook points:
[`BeforeSend`](#beforesend-mutate-reject) runs before delivery to mutate or
reject a message, and [`AfterSend`](#aftersend-observe) runs after delivery to
observe the result.

## The `Middleware` type

```go
type Middleware func(gomailer.Transport) gomailer.Transport
```

A `Middleware` takes a transport and returns one that wraps it. Both helpers that
combine middlewares live in the `middleware` package.

### `Wrap` — apply to one transport

`Wrap(t, mws...)` applies the listed middlewares to `t`. The **first listed
middleware is the outermost layer** — it sees the call first and the result last,
matching reading order:

```go
import "github.com/shyim/go-mailer/middleware"

// request flows A -> B -> C -> leaf
t := middleware.Wrap(leaf, A, B, C)
```

`Wrap(t)` with no middlewares returns `t` unchanged, and `nil` entries are
skipped — so you can include layers conditionally.

### `Chain` — reuse a stack across leaves

`Chain(mws...)` composes the same middlewares into a single `Middleware` you can
apply to several transports. Same ordering rule: first listed is outermost.

```go
stack := middleware.Chain(A, B, C)
t1 := stack(leaf1)
t2 := stack(leaf2)
```

This is the recommended pattern for composites: wrap each *leaf* so every
delivery attempt (including each failover retry) flows through the stack. See
[Transports](./transports.md) for the composite routers.

## `BeforeSend` — mutate / reject

```go
func BeforeSend(
    fn func(ctx context.Context, msg *gomailer.Message, envelope *gomailer.Envelope) error,
) middleware.Middleware
```

`BeforeSend` runs `fn` before delivery. Inside `fn` you can:

- **Mutate** the `*gomailer.Message` and/or `*gomailer.Envelope` before they are
  serialized and sent.
- **Reject** the send by returning `middleware.ErrReject` (or an error wrapping
  it). The wrapped transport is never called; this layer returns `(nil, nil)` and
  `Mailer.Send` reports **success** — there is no error for the caller.
- **Abort** by returning any other non-nil error, which is returned to the caller
  **unchanged** (so `errors.Is` / `errors.As` keep working in outer layers).
- **Proceed** by returning `nil`.

!!! note "Mutation is in place"
    `fn` mutates the `*Message` / `*Envelope` it is handed directly — there is no
    "return a new message" step. The values are **send-local clones**, so your
    edits apply to this delivery only and never alter the caller's retained
    objects.

!!! warning "Guard against a nil message"
    Only a `*gomailer.Message` is mutable. A pre-serialized `RawMessage` (from
    `gomailer.NewRawMessage`) carries no addressing, so `fn` receives a **nil**
    `*Message` in that case. The `*Envelope` is **always non-nil and mutable** —
    if the caller passed `nil`, it is derived from the message first. Make
    addressing changes through the envelope and nil-check the message.

## `AfterSend` — observe

```go
func AfterSend(
    fn func(ctx context.Context, sm *gomailer.SentMessage, err error),
) middleware.Middleware
```

`AfterSend` runs `fn` after the wrapped transport's `Send` and hands it the
result. It is **observe-only**: it never alters the returned `(*SentMessage,
error)`, so error classification keeps working for outer layers.

| Outcome | `fn` receives |
|---|---|
| Success | `(sm, nil)` |
| Failure | `(nil, err)` |
| Rejected (`ErrReject`) | `(nil, nil)` |

Guard against a nil `*SentMessage`, since both rejection and failure pass `nil`.

!!! tip
    A nil `fn` in either `BeforeSend` or `AfterSend` yields a pass-through
    (identity) middleware, so you can wire layers unconditionally.

## Example: reject + mutate + observe

```go
import (
    "context"
    "log"

    "github.com/shyim/go-mailer"
    "github.com/shyim/go-mailer/middleware"
)

t := middleware.Wrap(leaf, // first argument is the OUTERMOST layer
    middleware.AfterSend(func(_ context.Context, sm *gomailer.SentMessage, err error) {
        switch {
        case err != nil:
            log.Printf("send failed: %v", err)
        case sm != nil:
            log.Printf("sent %s", sm.MessageID())
        default:
            log.Print("send rejected")
        }
    }),
    middleware.BeforeSend(func(_ context.Context, msg *gomailer.Message, env *gomailer.Envelope) error {
        if blocked(env.Recipients()[0].Email()) {
            return middleware.ErrReject // skip + report success
        }
        if msg != nil {
            msg.SetHeader("X-Audited", "true") // mutate the send-local clone
        }
        return nil
    }),
)

mailer := gomailer.NewMailer(t)
```

A runnable version lives in `middleware/hooks_example_test.go`
(`Example_hooks`).

## OpenTelemetry

For traces and metrics, use the provider-agnostic `Observability` middleware
wired to a real OpenTelemetry provider through `middleware/otelmw`. See the
[Observability guide](../guides/observability.md).
