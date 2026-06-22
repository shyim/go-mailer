package gomailer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/mail"
	"strings"
	"testing"
)

func TestNewSentMessageMessageIDMatchesWireHeader(t *testing.T) {
	msg := testMessage(t)
	env, err := EnvelopeFromMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := NewSentMessage(msg, env)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(sm.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	wireID := strings.Trim(parsed.Header.Get("Message-ID"), "<>")
	if wireID == "" || wireID != sm.MessageID() {
		t.Fatalf("wire Message-ID=%q SentMessage.MessageID=%q", wireID, sm.MessageID())
	}
}

func TestSentMessageBytesAreCopySafe(t *testing.T) {
	msg := testMessage(t)
	env, err := EnvelopeFromMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := NewSentMessage(msg, env)
	if err != nil {
		t.Fatal(err)
	}
	first := sm.Bytes()
	first[0] = 'X'
	second := sm.Bytes()
	if second[0] == 'X' {
		t.Fatal("SentMessage.Bytes returned mutable internal storage")
	}
}

func TestBaseTransportHonorsCanceledContextBeforeDelivery(t *testing.T) {
	called := false
	bt := &BaseTransport{Name: "test://", DoSend: func(context.Context, *SentMessage) error {
		called = true
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bt.Send(ctx, testMessage(t), nil); err == nil {
		t.Fatal("expected canceled context error")
	}
	if called {
		t.Fatal("DoSend was called for an already-canceled context")
	}
}

func TestBaseTransportRejectsTypedNilMessage(t *testing.T) {
	bt := &BaseTransport{Name: "test://", DoSend: func(context.Context, *SentMessage) error { return nil }}
	var msg *Message
	if _, err := bt.Send(context.Background(), msg, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("typed nil message error = %v, want ErrInvalidArgument", err)
	}
}

func TestSetHeaderRejectsCRLFInjection(t *testing.T) {
	msg := testMessage(t).SetHeader("X-Good\r\nBcc", "victim@example.com")
	raw, err := msg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Bcc: victim@example.com")) {
		t.Fatalf("serialized message contains injected Bcc header:\n%s", raw)
	}
}

func TestSetHeaderDoesNotEmitGeneratedHeaderCollisions(t *testing.T) {
	msg := testMessage(t).
		SetHeader("Bcc", "victim@example.com").
		SetHeader("From", "attacker@example.com").
		SetHeader("Message-ID", "<evil@example.com>")
	raw, err := msg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Header.Get("Bcc"); got != "" {
		t.Fatalf("custom Bcc leaked onto wire: %q", got)
	}
	if strings.Contains(parsed.Header.Get("From"), "attacker@example.com") {
		t.Fatalf("custom From duplicated/generated header: %q", parsed.Header["From"])
	}
	if strings.Contains(parsed.Header.Get("Message-ID"), "evil@example.com") {
		t.Fatalf("custom Message-ID overrode generated header: %q", parsed.Header.Get("Message-ID"))
	}
}

func TestEnvelopeRejectsZeroValueAddresses(t *testing.T) {
	valid := MustAddress("valid@example.com", "")
	if _, err := NewEnvelope(Address{}, []Address{valid}); err == nil {
		t.Fatal("zero-value sender was accepted")
	}
	if _, err := NewEnvelope(valid, []Address{{}}); err == nil {
		t.Fatal("zero-value recipient was accepted")
	}
}

func TestMessageEnsureValidityRejectsInvalidHeaderAddressesWithExplicitEnvelope(t *testing.T) {
	valid := MustAddress("valid@example.com", "")
	env, err := NewEnvelope(valid, []Address{valid})
	if err != nil {
		t.Fatal(err)
	}
	msg := NewMessage().
		SetFrom(Address{}).
		SetTo(valid).
		SetText([]byte("body"))

	bt := &BaseTransport{Name: "test://", DoSend: func(context.Context, *SentMessage) error {
		t.Fatal("DoSend should not be called for invalid message headers")
		return nil
	}}
	if _, err := bt.Send(context.Background(), msg, env); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Send error = %v, want ErrInvalidArgument", err)
	}
}

func TestQuotedLocalPartAddressesAreRejected(t *testing.T) {
	if _, err := NewAddress(`"first last"@example.com`, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("NewAddress quoted local-part error = %v, want ErrInvalidArgument", err)
	}
	if _, err := ParseAddress(`"first last"@example.com`); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ParseAddress quoted local-part error = %v, want ErrInvalidArgument", err)
	}
	if _, err := ParseAddressList(`ok@example.com, "first last"@example.com`); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("ParseAddressList quoted local-part error = %v, want ErrInvalidArgument", err)
	}
}

func TestEnvelopeFromMessageSenderHeaderPriority(t *testing.T) {
	msg := testMessage(t).
		SetHeader("Return-Path", "return@example.com").
		SetHeader("Sender", "sender@example.com")
	env, err := EnvelopeFromMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.Sender().Email(); got != "sender@example.com" {
		t.Fatalf("sender = %q, want Sender header", got)
	}
}

func TestEnvelopeFromMessageMissingSenderIsLogicError(t *testing.T) {
	msg := NewMessage().SetTo(MustAddress("r@example.com", "")).SetText([]byte("body"))
	_, err := EnvelopeFromMessage(msg)
	if !errors.Is(err, ErrLogic) {
		t.Fatalf("error = %v, want ErrLogic", err)
	}
}

func TestNewSentMessageRejectsNilRawMessage(t *testing.T) {
	env, err := NewEnvelope(MustAddress("s@example.com", ""), []Address{MustAddress("r@example.com", "")})
	if err != nil {
		t.Fatal(err)
	}
	var msg RawMessage
	if _, err := NewSentMessage(msg, env); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("nil RawMessage error = %v, want ErrInvalidArgument", err)
	}
	var typedNil *rawBytes
	if _, err := NewSentMessage(typedNil, env); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("typed nil RawMessage error = %v, want ErrInvalidArgument", err)
	}
	var customNil *customRawMessage
	if _, err := NewSentMessage(customNil, env); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("custom typed nil RawMessage error = %v, want ErrInvalidArgument", err)
	}
}

type customRawMessage struct{}

func (*customRawMessage) WriteTo(io.Writer) (int64, error) { return 0, nil }
func (*customRawMessage) Bytes() ([]byte, error)           { return []byte("Subject: x\r\n\r\n"), nil }
