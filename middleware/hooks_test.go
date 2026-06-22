package middleware_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/middleware"
)

// newTestMessage builds a minimal, valid *gomailer.Message for hook tests.
func newTestMessage(t *testing.T) *gomailer.Message {
	t.Helper()
	return gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("from@example.com", "")).
		SetTo(gomailer.MustAddress("to@example.com", "")).
		SetSubject("hi").
		SetText([]byte("body"))
}

// TestBeforeSend_RejectSkipsSendAndReportsSuccess verifies that a BeforeSend hook
// returning ErrReject yields (nil, nil) and never calls the wrapped transport.
func TestBeforeSend_RejectSkipsSendAndReportsSuccess(t *testing.T) {
	leaf := &fakeTransport{name: "leaf", sm: &gomailer.SentMessage{}}
	wrapped := middleware.Wrap(leaf, middleware.BeforeSend(
		func(context.Context, *gomailer.Message, *gomailer.Envelope) error {
			return middleware.ErrReject
		},
	))

	sm, err := wrapped.Send(context.Background(), newTestMessage(t), nil)
	if err != nil {
		t.Fatalf("reject should report success, got err: %v", err)
	}
	if sm != nil {
		t.Fatalf("reject should yield nil SentMessage, got %v", sm)
	}
	if leaf.calls != 0 {
		t.Fatalf("wrapped transport called %d times on reject, want 0", leaf.calls)
	}
}

// TestBeforeSend_WrappedRejectStillSkips verifies errors.Is detection: an error
// that wraps ErrReject is treated as a reject.
func TestBeforeSend_WrappedRejectStillSkips(t *testing.T) {
	leaf := &fakeTransport{name: "leaf", sm: &gomailer.SentMessage{}}
	wrapped := middleware.Wrap(leaf, middleware.BeforeSend(
		func(context.Context, *gomailer.Message, *gomailer.Envelope) error {
			return fmt.Errorf("policy denied: %w", middleware.ErrReject)
		},
	))

	sm, err := wrapped.Send(context.Background(), newTestMessage(t), nil)
	if err != nil {
		t.Fatalf("wrapped reject should report success, got err: %v", err)
	}
	if sm != nil {
		t.Fatalf("wrapped reject should yield nil SentMessage, got %v", sm)
	}
	if leaf.calls != 0 {
		t.Fatalf("wrapped transport called %d times on wrapped reject, want 0", leaf.calls)
	}
}

// TestBeforeSend_OtherErrorAborts verifies a non-reject error aborts the send,
// propagates unchanged (errors.Is keeps working) and skips the transport.
func TestBeforeSend_OtherErrorAborts(t *testing.T) {
	sentinel := errors.New("boom")
	leaf := &fakeTransport{name: "leaf", sm: &gomailer.SentMessage{}}
	wrapped := middleware.Wrap(leaf, middleware.BeforeSend(
		func(context.Context, *gomailer.Message, *gomailer.Envelope) error {
			return sentinel
		},
	))

	sm, err := wrapped.Send(context.Background(), newTestMessage(t), nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped sentinel", err)
	}
	if sm != nil {
		t.Fatalf("aborted send should yield nil SentMessage, got %v", sm)
	}
	if leaf.calls != 0 {
		t.Fatalf("wrapped transport called %d times on abort, want 0", leaf.calls)
	}
}

// TestBeforeSend_MutateMessageAndEnvelope verifies the hook can mutate the
// message and the (auto-derived) envelope, and that the mutated envelope reaches
// the wrapped transport.
func TestBeforeSend_MutateMessageAndEnvelope(t *testing.T) {
	var gotEnv *gomailer.Envelope
	leaf := &fakeTransport{name: "leaf", sm: &gomailer.SentMessage{}}
	leaf.onSend = func(env *gomailer.Envelope) { gotEnv = env }

	newSender := gomailer.MustAddress("rewritten@example.com", "")
	wrapped := middleware.Wrap(leaf, middleware.BeforeSend(
		func(_ context.Context, msg *gomailer.Message, env *gomailer.Envelope) error {
			if msg == nil {
				t.Error("expected a mutable *Message, got nil")
				return nil
			}
			msg.SetSubject("rewritten subject")
			return env.SetSender(newSender)
		},
	))

	msg := newTestMessage(t)
	if _, err := wrapped.Send(context.Background(), msg, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if leaf.calls != 1 {
		t.Fatalf("wrapped transport called %d times, want 1", leaf.calls)
	}
	if gotEnv == nil {
		t.Fatal("transport received nil envelope")
	}
	if got := gotEnv.Sender().Email(); got != "rewritten@example.com" {
		t.Errorf("envelope sender = %q, want rewritten@example.com", got)
	}
	// Subject mutation is applied to the send-local clone forwarded to the leaf,
	// not to the caller's retained Message.
	forwarded, ok := leaf.lastMsg.(*gomailer.Message)
	if !ok {
		t.Fatalf("leaf saw %T, want *gomailer.Message", leaf.lastMsg)
	}
	raw, err := forwarded.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if !bytes.Contains(raw, []byte("rewritten subject")) {
		t.Error("mutated subject not present in forwarded serialized message")
	}
	originalRaw, err := msg.Bytes()
	if err != nil {
		t.Fatalf("original Bytes: %v", err)
	}
	if bytes.Contains(originalRaw, []byte("rewritten subject")) {
		t.Error("caller message was mutated; BeforeSend should isolate the send-local clone")
	}
}

// TestBeforeSend_RawMessageGuard verifies that a pre-serialized RawMessage (which
// carries no addressing) yields a nil *Message to the hook, while the envelope is
// always non-nil and mutable.
func TestBeforeSend_RawMessageGuard(t *testing.T) {
	leaf := &fakeTransport{name: "leaf", sm: &gomailer.SentMessage{}}

	var sawNilMessage, sawEnvelope bool
	wrapped := middleware.Wrap(leaf, middleware.BeforeSend(
		func(_ context.Context, msg *gomailer.Message, env *gomailer.Envelope) error {
			sawNilMessage = msg == nil
			sawEnvelope = env != nil
			return nil
		},
	))

	raw := gomailer.NewRawMessage([]byte("Subject: pre-serialized\r\n\r\nbody"))
	env, err := gomailer.NewEnvelope(
		gomailer.MustAddress("from@example.com", ""),
		[]gomailer.Address{gomailer.MustAddress("to@example.com", "")},
	)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	if _, err := wrapped.Send(context.Background(), raw, env); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !sawNilMessage {
		t.Error("expected nil *Message for a RawMessage, hook saw a non-nil message")
	}
	if !sawEnvelope {
		t.Error("expected a non-nil envelope in the hook")
	}
	if leaf.calls != 1 {
		t.Fatalf("wrapped transport called %d times, want 1", leaf.calls)
	}
}

// TestBeforeSend_NilFnIsIdentity verifies a nil fn returns the same transport.
func TestBeforeSend_NilFnIsIdentity(t *testing.T) {
	leaf := &fakeTransport{name: "leaf"}
	if got := middleware.Wrap(leaf, middleware.BeforeSend(nil)); got != gomailer.Transport(leaf) {
		t.Fatal("BeforeSend(nil) should be an identity middleware")
	}
}

// TestAfterSend_ObservesSuccess verifies AfterSend receives (sm, nil) on success.
func TestAfterSend_ObservesSuccess(t *testing.T) {
	wantSM := &gomailer.SentMessage{}
	wantSM.SetMessageID("<id@host>")
	leaf := &fakeTransport{name: "leaf", sm: wantSM}

	var gotSM *gomailer.SentMessage
	var gotErr error
	var calls int
	wrapped := middleware.Wrap(leaf, middleware.AfterSend(
		func(_ context.Context, sm *gomailer.SentMessage, err error) {
			calls++
			gotSM = sm
			gotErr = err
		},
	))

	sm, err := wrapped.Send(context.Background(), newTestMessage(t), nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sm != wantSM {
		t.Fatal("SentMessage not passed through unchanged")
	}
	if calls != 1 {
		t.Fatalf("AfterSend called %d times, want 1", calls)
	}
	if gotSM != wantSM || gotErr != nil {
		t.Fatalf("AfterSend observed (%v, %v), want (sm, nil)", gotSM, gotErr)
	}
}

// TestAfterSend_ObservesFailureUnchanged verifies AfterSend receives (nil, err)
// on failure and that the error is propagated unchanged (classification intact).
func TestAfterSend_ObservesFailureUnchanged(t *testing.T) {
	sendErr := gomailer.NewTransportError("delivery failed")
	leaf := &fakeTransport{name: "leaf", err: sendErr}

	var gotSM *gomailer.SentMessage
	var gotErr error
	wrapped := middleware.Wrap(leaf, middleware.AfterSend(
		func(_ context.Context, sm *gomailer.SentMessage, err error) {
			gotSM = sm
			gotErr = err
		},
	))

	sm, err := wrapped.Send(context.Background(), newTestMessage(t), nil)
	if sm != nil {
		t.Fatalf("SentMessage = %v, want nil on failure", sm)
	}
	if !errors.Is(err, gomailer.ErrTransport) {
		t.Fatalf("error no longer satisfies ErrTransport: %v", err)
	}
	if gotSM != nil {
		t.Errorf("AfterSend observed sm = %v, want nil", gotSM)
	}
	if !errors.Is(gotErr, sendErr) {
		t.Errorf("AfterSend observed err = %v, want sendErr", gotErr)
	}
}

// TestAfterSend_NilFnIsIdentity verifies a nil fn returns the same transport.
func TestAfterSend_NilFnIsIdentity(t *testing.T) {
	leaf := &fakeTransport{name: "leaf"}
	if got := middleware.Wrap(leaf, middleware.AfterSend(nil)); got != gomailer.Transport(leaf) {
		t.Fatal("AfterSend(nil) should be an identity middleware")
	}
}
