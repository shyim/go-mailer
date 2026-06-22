package mailertest

import (
	"bytes"

	gomailer "github.com/shyim/go-mailer"
)

// TestingT is the minimal subset of *testing.T used by the assertion helpers.
// Accepting an interface keeps mailertest free of a direct testing dependency
// and lets callers supply a fake in their own tests.
type TestingT interface {
	Helper()
	Errorf(format string, args ...any)
}

// AssertEmailCount fails t if the transport has not recorded exactly want
// messages sent.
func AssertEmailCount(t TestingT, transport *RecordingTransport, want int) {
	t.Helper()
	if got := transport.Count(); got != want {
		t.Errorf("expected transport %q to have sent %d emails, got %d", transport.String(), want, got)
	}
}

// AssertQueuedEmailCount fails t unless want is 0. The RecordingTransport sends
// synchronously, so no message is ever queued. By design any want > 0 always
// fails: there is no async/queued path to satisfy it, so a test that wants to
// assert a delivered count should use AssertEmailCount instead.
func AssertQueuedEmailCount(t TestingT, transport *RecordingTransport, want int) {
	t.Helper()
	if want != 0 {
		t.Errorf("expected transport %q to have queued %d emails, but it sends synchronously (0 queued)", transport.String(), want)
	}
}

// AssertSent fails t if the transport recorded no messages at all.
func AssertSent(t TestingT, transport *RecordingTransport) {
	t.Helper()
	if transport.Count() == 0 {
		t.Errorf("expected transport %q to have sent at least one email, got none", transport.String())
	}
}

// AssertNotSent fails t if the transport recorded any message.
func AssertNotSent(t TestingT, transport *RecordingTransport) {
	t.Helper()
	if got := transport.Count(); got != 0 {
		t.Errorf("expected transport %q to have sent no email, got %d", transport.String(), got)
	}
}

// AssertEmailContains fails t unless at least one recorded message's wire bytes
// contain needle. It is a convenience for asserting on headers or body text.
func AssertEmailContains(t TestingT, transport *RecordingTransport, needle string) {
	t.Helper()
	for _, sm := range transport.Messages() {
		if bytes.Contains(sm.Bytes(), []byte(needle)) {
			return
		}
	}
	t.Errorf("expected a sent email on transport %q to contain %q, none did", transport.String(), needle)
}

// MessageAt returns the recorded SentMessage at index i and true, or nil and
// false if i is out of range.
func MessageAt(transport *RecordingTransport, i int) (*gomailer.SentMessage, bool) {
	msgs := transport.Messages()
	if i < 0 || i >= len(msgs) {
		return nil, false
	}
	return msgs[i], true
}
