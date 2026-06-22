package gomailer

import (
	"fmt"
	"reflect"
)

// SentMessage is the result of a successful send: the serialized bytes that
// went on the wire, the envelope used, the transport-level message id and any
// accumulated debug transcript.
type SentMessage struct {
	raw       []byte
	envelope  *Envelope
	messageID string
	debug     []byte
}

// NewSentMessage serializes msg, ensuring validity, and captures the bytes and
// envelope. It is normally called by BaseTransport, not by user code.
func NewSentMessage(msg RawMessage, envelope *Envelope) (*SentMessage, error) {
	if isNilRawMessage(msg) {
		return nil, fmt.Errorf("%w: message must not be nil", ErrInvalidArgument)
	}
	// A *Message can validate its addressing and supplies a Message-ID header;
	// a bare RawMessage carries neither, so it is serialized directly.
	if m, ok := msg.(*Message); ok {
		m = m.Clone()
		if err := m.EnsureValidity(); err != nil {
			return nil, err
		}
		messageID := m.MessageID()
		raw, err := m.Bytes()
		if err != nil {
			return nil, err
		}
		return &SentMessage{
			raw:       raw,
			envelope:  cloneEnvelope(envelope),
			messageID: messageID,
		}, nil
	}

	raw, err := msg.Bytes()
	if err != nil {
		return nil, err
	}
	return &SentMessage{raw: raw, envelope: cloneEnvelope(envelope)}, nil
}

func isNilRawMessage(msg RawMessage) bool {
	if msg == nil {
		return true
	}
	switch m := msg.(type) {
	case *Message:
		return m == nil
	case *rawBytes:
		return m == nil
	default:
		return isNilReflectValue(reflect.ValueOf(msg))
	}
}

//nolint:govet // explicit reflect.Kind cases keep typed-nil RawMessage handling clear.
func isNilReflectValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Chan:
		return v.IsNil()
	case reflect.Func:
		return v.IsNil()
	case reflect.Interface:
		return v.IsNil()
	case reflect.Map:
		return v.IsNil()
	case reflect.Ptr:
		return v.IsNil()
	case reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// Bytes returns the raw message that was sent.
func (s *SentMessage) Bytes() []byte {
	return append([]byte(nil), s.raw...)
}

// Envelope returns the envelope used to send the message.
func (s *SentMessage) Envelope() *Envelope {
	return cloneEnvelope(s.envelope)
}

func cloneEnvelope(e *Envelope) *Envelope {
	if e == nil {
		return nil
	}
	return e.Clone()
}

// MessageID returns the transport-level message id (set by the transport).
func (s *SentMessage) MessageID() string {
	return s.messageID
}

// SetMessageID sets the transport-level message id.
func (s *SentMessage) SetMessageID(id string) {
	s.messageID = id
}

// Debug returns the accumulated debug transcript.
func (s *SentMessage) Debug() string {
	return string(s.debug)
}

// AppendDebug appends to the debug transcript.
func (s *SentMessage) AppendDebug(b string) {
	s.debug = append(s.debug, b...)
}
