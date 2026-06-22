package gomailer

import (
	"fmt"
	"strings"
)

// Envelope holds the SMTP-level sender and recipients, independent of the
// message headers, and validates them before they reach the transport.
type Envelope struct {
	sender     Address
	recipients []Address
}

// NewEnvelope builds an Envelope, validating that the sender's local-part is
// ASCII-only and that there is at least one recipient.
func NewEnvelope(sender Address, recipients []Address) (*Envelope, error) {
	e := &Envelope{}
	if err := e.SetSender(sender); err != nil {
		return nil, err
	}
	if err := e.SetRecipients(recipients); err != nil {
		return nil, err
	}
	return e, nil
}

// EnvelopeFromMessage derives an Envelope from a *Message: the sender is the
// first From address (or Sender), recipients are To+Cc+Bcc. Returns an error
// for a RawMessage (which carries no parsed addressing) or when validation
// fails.
func EnvelopeFromMessage(msg RawMessage) (*Envelope, error) {
	if isNilRawMessage(msg) {
		return nil, fmt.Errorf("%w: message must not be nil", ErrInvalidArgument)
	}
	m, ok := msg.(*Message)
	if !ok {
		return nil, fmt.Errorf("%w: cannot send a RawMessage without an explicit Envelope", ErrLogic)
	}

	sender, ok := senderFromMessageHeaders(m)
	if !ok {
		return nil, fmt.Errorf("%w: unable to determine the sender of the message", ErrLogic)
	}

	return NewEnvelope(sender, m.Recipients())
}

func senderFromMessageHeaders(m *Message) (Address, bool) {
	for _, header := range []string{"Sender", "Return-Path"} {
		if v, ok := m.Header(header); ok && strings.TrimSpace(v) != "" {
			addr, err := ParseAddress(v)
			if err == nil {
				return addr, true
			}
		}
	}
	from := m.From()
	if len(from) == 0 {
		return Address{}, false
	}
	return from[0], true
}

// Sender returns the envelope sender (the bounce/MAIL FROM address).
func (e *Envelope) Sender() Address {
	return e.sender
}

// Recipients returns a copy of the envelope recipients (RCPT TO addresses).
func (e *Envelope) Recipients() []Address {
	out := make([]Address, len(e.recipients))
	copy(out, e.recipients)
	return out
}

// SetSender validates and sets the sender (ASCII local-part required).
//
// To ensure deliverability of bounce emails independent of UTF-8 capabilities
// of SMTP servers, the local-part of the sender must be ASCII-only.
func (e *Envelope) SetSender(sender Address) error {
	if !sender.valid() {
		return fmt.Errorf("%w: invalid sender address %q", ErrInvalidArgument, sender.Email())
	}
	if sender.HasUnicodeLocalPart() {
		return fmt.Errorf("%w: invalid sender %q: non-ASCII characters not supported in local-part of email", ErrInvalidArgument, sender.Email())
	}
	e.sender = sender
	return nil
}

// SetRecipients validates and sets the recipients (must be non-empty).
func (e *Envelope) SetRecipients(recipients []Address) error {
	if len(recipients) == 0 {
		return fmt.Errorf("%w: an envelope must have at least one recipient", ErrInvalidArgument)
	}
	e.recipients = make([]Address, len(recipients))
	for i, recipient := range recipients {
		if !recipient.valid() {
			return fmt.Errorf("%w: invalid recipient address %q", ErrInvalidArgument, recipient.Email())
		}
		// Strip the display name: at the SMTP level only the bare addr-spec
		// is used for RCPT TO, so an explicit envelope stores addresses only.
		e.recipients[i] = Address{addr: recipient.addr}
		e.recipients[i].addr.Name = ""
	}
	return nil
}

// AnyAddressHasUnicodeLocalPart reports whether any address (sender or
// recipient) needs the SMTPUTF8 extension.
func (e *Envelope) AnyAddressHasUnicodeLocalPart() bool {
	if e.sender.HasUnicodeLocalPart() {
		return true
	}
	for _, r := range e.recipients {
		if r.HasUnicodeLocalPart() {
			return true
		}
	}
	return false
}

// Clone returns a deep copy of the envelope.
func (e *Envelope) Clone() *Envelope {
	c := &Envelope{sender: e.sender}
	c.recipients = make([]Address, len(e.recipients))
	copy(c.recipients, e.recipients)
	return c
}
