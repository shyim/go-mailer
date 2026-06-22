package middleware

import gomailer "github.com/shyim/go-mailer"

// Middleware decorates a gomailer.Transport, returning a transport that wraps
// the given one. Implementations should call the wrapped transport's Send and
// may observe or alter behavior around it. A Middleware must be nil-safe in the
// sense that it always returns a non-nil transport.
type Middleware func(gomailer.Transport) gomailer.Transport

// Wrap applies mws to t and returns the resulting transport. The FIRST listed
// middleware becomes the OUTERMOST layer (it sees the call first and the result
// last), matching the intuitive reading order:
//
//	Wrap(t, A, B, C) // request flows A -> B -> C -> t
//
// Wrap(t) with no middlewares returns t unchanged. nil entries in mws are
// skipped so callers can conditionally include layers.
func Wrap(t gomailer.Transport, mws ...Middleware) gomailer.Transport {
	// Apply in reverse so the first listed ends up outermost.
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] == nil {
			continue
		}
		t = mws[i](t)
	}
	return t
}

// Chain composes mws into a single Middleware that, when applied to a transport,
// is equivalent to calling Wrap with the same middlewares: the first listed
// becomes the outermost layer. It is convenient when the same stack must be
// applied to several leaf transports:
//
//	stack := middleware.Chain(A, B, C)
//	t1 := stack(leaf1)
//	t2 := stack(leaf2)
//
// Chain with no middlewares (or only nil entries) returns an identity
// Middleware that leaves the transport unchanged.
func Chain(mws ...Middleware) Middleware {
	return func(t gomailer.Transport) gomailer.Transport {
		return Wrap(t, mws...)
	}
}
