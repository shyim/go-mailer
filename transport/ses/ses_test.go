package ses

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	gomailer "github.com/shyim/go-mailer"
)

// fakeSES records the last SendEmail input and returns a canned result/error.
type fakeSES struct {
	lastInput *sesv2.SendEmailInput
	calls     int
	messageID string
	err       error
}

func (f *fakeSES) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.calls++
	f.lastInput = in
	if f.err != nil {
		return nil, f.err
	}
	return &sesv2.SendEmailOutput{MessageId: aws.String(f.messageID)}, nil
}

func testMessage(t *testing.T) *gomailer.Message {
	t.Helper()
	return gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("sender@example.com", "Sender")).
		SetTo(gomailer.MustAddress("rcpt@example.org", "Recipient")).
		SetSubject("Hello").
		SetText([]byte("Body text.")).
		SetHTML([]byte("<p>Body text.</p>"))
}

func TestSendSubmitsRawMIME(t *testing.T) {
	fake := &fakeSES{messageID: "0100018abc-msgid"}
	tr := NewWithClient(fake, WithRegion("us-east-1"))

	mailer := gomailer.NewMailer(tr)
	if err := mailer.Send(context.Background(), testMessage(t), nil); err != nil {
		t.Fatalf("send: %v", err)
	}

	if fake.calls != 1 {
		t.Fatalf("SendEmail called %d times, want 1", fake.calls)
	}
	in := fake.lastInput
	if in.Content == nil || in.Content.Raw == nil {
		t.Fatal("expected raw MIME content")
	}
	raw := string(in.Content.Raw.Data)
	if !strings.Contains(raw, "Subject: Hello") {
		t.Errorf("raw MIME missing Subject header:\n%s", raw)
	}
	if !strings.Contains(raw, "<p>Body text.</p>") {
		t.Errorf("raw MIME missing HTML body")
	}
	if aws.ToString(in.FromEmailAddress) != "sender@example.com" {
		t.Errorf("FromEmailAddress = %q", aws.ToString(in.FromEmailAddress))
	}
	if got := in.Destination.ToAddresses; len(got) != 1 || got[0] != "rcpt@example.org" {
		t.Errorf("Destination.ToAddresses = %v, want [rcpt@example.org]", got)
	}
}

func TestSendSetsMessageID(t *testing.T) {
	fake := &fakeSES{messageID: "ses-message-id-123"}
	tr := NewWithClient(fake)

	sm, err := tr.Send(context.Background(), testMessage(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if sm.MessageID() != "ses-message-id-123" {
		t.Errorf("MessageID = %q, want the SES message id", sm.MessageID())
	}
}

func TestSendWrapsError(t *testing.T) {
	fake := &fakeSES{err: errors.New("AccessDenied")}
	tr := NewWithClient(fake)

	_, err := tr.Send(context.Background(), testMessage(t), nil)
	if err == nil {
		t.Fatal("expected an error from SendEmail")
	}
	if !errors.Is(err, gomailer.ErrTransport) {
		t.Errorf("error should classify as ErrTransport: %v", err)
	}
	var te *gomailer.TransportError
	if errors.As(err, &te) {
		if !strings.Contains(te.Cause.Error(), "AccessDenied") {
			t.Errorf("underlying cause not preserved: %v", te.Cause)
		}
	} else {
		t.Error("error is not a *TransportError")
	}
}

func TestConfigurationSetPassedThrough(t *testing.T) {
	fake := &fakeSES{messageID: "id"}
	tr := NewWithClient(fake, WithConfigurationSet("my-config-set"))

	if _, err := tr.Send(context.Background(), testMessage(t), nil); err != nil {
		t.Fatal(err)
	}
	if got := aws.ToString(fake.lastInput.ConfigurationSetName); got != "my-config-set" {
		t.Errorf("ConfigurationSetName = %q, want my-config-set", got)
	}
}

func TestNoConfigurationSetByDefault(t *testing.T) {
	fake := &fakeSES{messageID: "id"}
	tr := NewWithClient(fake)
	if _, err := tr.Send(context.Background(), testMessage(t), nil); err != nil {
		t.Fatal(err)
	}
	if fake.lastInput.ConfigurationSetName != nil {
		t.Errorf("ConfigurationSetName should be nil when unset, got %q", aws.ToString(fake.lastInput.ConfigurationSetName))
	}
}

func TestString(t *testing.T) {
	if got := NewWithClient(&fakeSES{}, WithRegion("eu-west-1")).String(); got != "ses://eu-west-1" {
		t.Errorf("String() = %q, want ses://eu-west-1", got)
	}
	if got := NewWithClient(&fakeSES{}).String(); got != "ses://" {
		t.Errorf("String() = %q, want ses://", got)
	}
}
