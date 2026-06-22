// Package middleware provides a Transport-decorator middleware system for
// gomailer plus provider-agnostic observability (tracing + metrics) hooks.
//
// This package is STDLIB-ONLY by design. The observability interfaces here
// (Tracer/Span and Meter/Counter/Histogram) are deliberately minimal and
// provider-neutral so that an adapter package (e.g. middleware/otelmw) can
// satisfy them against a real OpenTelemetry provider WITHOUT leaking any OTel
// type into this package or into the dependency graph of a user who only
// imports "middleware".
//
// The wrap point is the leaf transport: a Middleware decorates a single
// gomailer.Transport. Because RoundRobin/Failover routers loop over their leaf
// transports and call leaf.Send, wrapping each leaf means every delivery
// attempt — including failover retries — is observed independently with its own
// span and metric sample. Apply middlewares per leaf with Wrap:
//
//	t := middleware.Wrap(leaf, middleware.Observability(
//		middleware.WithTracer(tracer),
//		middleware.WithMeter(meter),
//	))
package middleware
