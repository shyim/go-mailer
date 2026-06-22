// Package transport contains the concrete mailer transports and the DSN-based
// factory/registry that builds them. This file implements the Null transport,
// which silently discards every message.
package transport

import (
	"context"
	"fmt"

	gomailer "github.com/shyim/go-mailer"
)

// NullTransport pretends messages have been sent but simply discards them.
// It is identified by "null://".
type NullTransport struct {
	gomailer.BaseTransport
}

// NewNullTransport returns a NullTransport.
func NewNullTransport() *NullTransport {
	t := &NullTransport{}
	t.Name = "null://"
	t.DoSend = func(ctx context.Context, sm *gomailer.SentMessage) error {
		return nil
	}
	return t
}

func init() {
	RegisterDefaultFactory(func() Factory { return NullFactory{} })
}

// NullFactory builds a NullTransport from a DSN with the "null" scheme. Its
// zero value is ready to use.
type NullFactory struct{}

// Create returns a NullTransport for a "null" scheme DSN, otherwise an error
// wrapping gomailer.ErrUnsupportedScheme.
func (f NullFactory) Create(d *DSN, _ Deps) (gomailer.Transport, error) {
	if !f.Supports(d) {
		return nil, fmt.Errorf("%w: the %q scheme is not supported; supported schemes are: %q", gomailer.ErrUnsupportedScheme, d.Scheme(), []string{"null"})
	}
	return NewNullTransport(), nil
}

// Supports reports whether the DSN scheme is "null".
func (f NullFactory) Supports(d *DSN) bool {
	return d.Scheme() == "null"
}

// SupportedSchemes lists the schemes this factory handles. It lets the registry
// include "null" in the unsupported-scheme error message (satisfies the
// schemeLister interface used by Registry.supportedSchemes).
func (f NullFactory) SupportedSchemes() []string {
	return []string{"null"}
}
