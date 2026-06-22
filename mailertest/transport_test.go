package mailertest

import (
	"context"
	"testing"

	gomailer "github.com/shyim/go-mailer"
)

// fakeT captures assertion failures so the helpers can be tested without
// failing the enclosing *testing.T.
type fakeT struct{ failed bool }

func (f *fakeT) Helper()               {}
func (f *fakeT) Errorf(string, ...any) { f.failed = true }

func msg(t *testing.T, subject string) *gomailer.Message {
	t.Helper()
	return gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("s@a.io", "")).
		SetTo(gomailer.MustAddress("r@b.io", "")).
		SetSubject(subject).
		SetText([]byte("body of " + subject))
}

func TestRecordingTransportRecords(t *testing.T) {
	tr := NewRecordingTransport("")
	if tr.String() != "test://" {
		t.Errorf("default name = %q, want test://", tr.String())
	}

	ctx := context.Background()
	if _, err := tr.Send(ctx, msg(t, "one"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Send(ctx, msg(t, "two"), nil); err != nil {
		t.Fatal(err)
	}

	if tr.Count() != 2 {
		t.Fatalf("Count = %d, want 2", tr.Count())
	}
	if got := len(tr.Messages()); got != 2 {
		t.Errorf("Messages len = %d, want 2", got)
	}
	last, ok := tr.Last()
	if !ok || last == nil {
		t.Fatal("Last returned nothing")
	}

	tr.Reset()
	if tr.Count() != 0 {
		t.Errorf("after Reset Count = %d, want 0", tr.Count())
	}
}

func TestRecordingTransportFailNext(t *testing.T) {
	tr := NewRecordingTransport("")
	tr.FailNext(gomailer.NewTransportError("boom"))
	if _, err := tr.Send(context.Background(), msg(t, "x"), nil); err == nil {
		t.Fatal("expected the injected failure")
	}
	if tr.Count() != 0 {
		t.Errorf("failed send should not be recorded, Count = %d", tr.Count())
	}
	// FailNext is one-shot: the next send succeeds.
	if _, err := tr.Send(context.Background(), msg(t, "y"), nil); err != nil {
		t.Fatalf("second send should succeed: %v", err)
	}
	if tr.Count() != 1 {
		t.Errorf("Count = %d, want 1", tr.Count())
	}
}

func TestAssertHelpers(t *testing.T) {
	tr := NewRecordingTransport("")
	ctx := context.Background()
	tr.Send(ctx, msg(t, "hello"), nil)
	tr.Send(ctx, msg(t, "world"), nil)

	// Passing assertions: a fresh fakeT must stay un-failed.
	for name, fn := range map[string]func(TestingT){
		"count":    func(ft TestingT) { AssertEmailCount(ft, tr, 2) },
		"queued0":  func(ft TestingT) { AssertQueuedEmailCount(ft, tr, 0) },
		"sent":     func(ft TestingT) { AssertSent(ft, tr) },
		"contains": func(ft TestingT) { AssertEmailContains(ft, tr, "body of hello") },
	} {
		ft := &fakeT{}
		fn(ft)
		if ft.failed {
			t.Errorf("assertion %q should have passed but failed", name)
		}
	}

	// Failing assertions: the fakeT must record a failure.
	for name, fn := range map[string]func(TestingT){
		"wrong-count":      func(ft TestingT) { AssertEmailCount(ft, tr, 5) },
		"queued-nonzero":   func(ft TestingT) { AssertQueuedEmailCount(ft, tr, 1) },
		"not-sent-but-was": func(ft TestingT) { AssertNotSent(ft, tr) },
		"missing-content":  func(ft TestingT) { AssertEmailContains(ft, tr, "nonexistent") },
	} {
		ft := &fakeT{}
		fn(ft)
		if !ft.failed {
			t.Errorf("assertion %q should have failed but passed", name)
		}
	}
}

func TestMessageAt(t *testing.T) {
	tr := NewRecordingTransport("")
	tr.Send(context.Background(), msg(t, "first"), nil)
	if _, ok := MessageAt(tr, 0); !ok {
		t.Error("MessageAt(0) should exist")
	}
	if _, ok := MessageAt(tr, 5); ok {
		t.Error("MessageAt(5) should be out of range")
	}
}
