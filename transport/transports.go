package transport

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gomailer "github.com/shyim/go-mailer"
)

// transportHeader is the custom header used to route a message to a named
// transport. Setting it on a *gomailer.Message via SetHeader("X-Transport",
// name) directs Transports.Send to the matching named transport. The header is
// stripped from the send-local clone before delivery so the routing control
// header is not put on the wire and concurrent sends of the same Message stay
// race-free.
const transportHeader = "X-Transport"

// Transports is a router over a set of named transports. It selects a transport
// by the X-Transport header on a *gomailer.Message, falling back to the default
// (the first registered) transport when no header is present or the message is
// a header-less RawMessage. It satisfies gomailer.Transport.
type Transports struct {
	transports  map[string]gomailer.Transport
	order       []string // registration order, for stable String()
	defaultName string
}

// NewTransports builds a router from a name->Transport map. order fixes the
// iteration/default order; the first name in order becomes the default. If
// order is nil the map keys are sorted for determinism. It returns
// ErrLogic-classified error when no transports are configured.
func NewTransports(transports map[string]gomailer.Transport, order []string) (*Transports, error) {
	if len(transports) == 0 {
		return nil, fmt.Errorf("%w: Transports must have at least one transport configured", gomailer.ErrLogic)
	}

	if order == nil {
		order = make([]string, 0, len(transports))
		for name := range transports {
			order = append(order, name)
		}
		sort.Strings(order)
	}

	// Keep only names that actually exist, preserving the provided order.
	cleaned := make([]string, 0, len(transports))
	seen := make(map[string]bool, len(transports))
	for _, name := range order {
		if _, ok := transports[name]; ok && !seen[name] {
			cleaned = append(cleaned, name)
			seen[name] = true
		}
	}
	// Append any map entries missing from order (sorted, for determinism).
	if len(cleaned) < len(transports) {
		var extra []string
		for name := range transports {
			if !seen[name] {
				extra = append(extra, name)
			}
		}
		sort.Strings(extra)
		cleaned = append(cleaned, extra...)
	}

	return &Transports{
		transports:  transports,
		order:       cleaned,
		defaultName: cleaned[0],
	}, nil
}

// Send routes msg to the transport named by its X-Transport header, or to the
// default transport when the header is absent or the message carries no headers
// (a plain RawMessage). The routing header is stripped from a cloned Message
// before delivery. An unknown transport name is an ErrInvalidArgument-classified
// error.
func (t *Transports) Send(ctx context.Context, msg gomailer.RawMessage, envelope *gomailer.Envelope) (*gomailer.SentMessage, error) {
	// Only a *gomailer.Message carries headers; a RawMessage routes to default.
	m, ok := msg.(*gomailer.Message)
	if !ok {
		return t.transports[t.defaultName].Send(ctx, msg, envelope)
	}

	name, has := m.Header(transportHeader)
	if !has {
		return t.transports[t.defaultName].Send(ctx, m.Clone(), envelope)
	}

	target, exists := t.transports[name]
	if !exists {
		return nil, fmt.Errorf("%w: the %q transport does not exist (available transports: %q)",
			gomailer.ErrInvalidArgument, name, strings.Join(t.order, `", "`))
	}

	routed := m.Clone()
	routed.RemoveHeader(transportHeader)
	return target.Send(ctx, routed, envelope)
}

// String returns the router identity, e.g. "[main,backup]".
func (t *Transports) String() string {
	return "[" + strings.Join(t.order, ",") + "]"
}

// Close closes every named transport that implements io.Closer, joining any
// errors. Transports are closed in registration order for determinism.
func (t *Transports) Close() error {
	ordered := make([]gomailer.Transport, 0, len(t.transports))
	for _, name := range t.order {
		ordered = append(ordered, t.transports[name])
	}
	return closeAll(ordered)
}
