package sendmail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport"
)

func TestNewSendmailTransportValidation(t *testing.T) {
	if _, err := NewSendmailTransport("/usr/sbin/sendmail -bs"); !errors.Is(err, gomailer.ErrInvalidArgument) {
		t.Errorf("-bs mode should be rejected, got %v", err)
	}
	if _, err := NewSendmailTransport("/usr/sbin/sendmail"); !errors.Is(err, gomailer.ErrInvalidArgument) {
		t.Errorf("command without -t should be rejected, got %v", err)
	}
	if _, err := NewSendmailTransport("/usr/sbin/sendmail -test -i"); !errors.Is(err, gomailer.ErrInvalidArgument) {
		t.Errorf("substring -test should not satisfy -t validation, got %v", err)
	}
	tr, err := NewSendmailTransport("")
	if err != nil {
		t.Fatalf("default command should be valid: %v", err)
	}
	if tr.String() != "smtp://sendmail" {
		t.Errorf("String() = %q, want smtp://sendmail", tr.String())
	}
}

func TestFactoryDoesNotAdvertiseSendmailSMTPMode(t *testing.T) {
	d, err := transport.ParseDSN("sendmail+smtp://default")
	if err != nil {
		t.Fatal(err)
	}
	f := Factory{}
	if f.Supports(d) {
		t.Fatal("sendmail+smtp should not be advertised until -bs mode is implemented")
	}
	if _, err := f.Create(d, transport.Deps{}); !errors.Is(err, gomailer.ErrUnsupportedScheme) {
		t.Fatalf("Create(sendmail+smtp) error = %v, want ErrUnsupportedScheme", err)
	}
}

// TestSendmailPipesMessage points the transport at a tiny shell script standing
// in for the sendmail binary, which writes everything it reads on stdin to a
// file. This is the local-binary analogue of the SMTP fake-server test.
func TestSendmailPipesMessage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake sendmail is POSIX-only")
	}
	dir := t.TempDir()
	captured := filepath.Join(dir, "captured.eml")
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "fakesendmail.sh")

	// The script records its args, then copies stdin into the capture file.
	content := "#!/bin/sh\n" +
		"echo \"$@\" > " + argsFile + "\n" +
		"cat > " + captured + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	// "-t" lets the transport keep recipients out of argv; -i avoids dot escaping.
	tr, err := NewSendmailTransport(script + " -t -i")
	if err != nil {
		t.Fatal(err)
	}

	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("sender@example.com", "")).
		SetTo(gomailer.MustAddress("rcpt@example.org", "")).
		SetSubject("Sendmail Test").
		SetText([]byte("piped body"))

	if _, serr := tr.Send(context.Background(), msg, nil); serr != nil {
		t.Fatalf("send: %v", serr)
	}

	data, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("nothing captured: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "Subject: Sendmail Test") {
		t.Errorf("captured message missing Subject:\n%s", got)
	}
	if !strings.Contains(got, "piped body") {
		t.Errorf("captured message missing body:\n%s", got)
	}
	// Stdin should have LF endings, not CRLF (sendmail expectation).
	if strings.Contains(got, "\r\n") {
		t.Errorf("sendmail stdin should use LF, found CRLF:\n%q", got)
	}

	// With explicit recipients, the transport drops -t and passes them in argv.
	args, err := os.ReadFile(argsFile)
	if err == nil {
		if !strings.Contains(string(args), "rcpt@example.org") {
			t.Errorf("recipient should be passed on the command line: %q", string(args))
		}
		if strings.Contains(string(args), "-t") {
			t.Errorf("explicit recipients should suppress -t: %q", string(args))
		}
	}
}

func TestSendmailFlagDetectionUsesExactArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake sendmail is POSIX-only")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "fakesendmail.sh")
	content := "#!/bin/sh\n" +
		"printf '%s\n' \"$@\" > " + argsFile + "\n" +
		"cat >/dev/null\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	tr, err := NewSendmailTransport(script + " -t -i -foo")
	if err != nil {
		t.Fatal(err)
	}
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("sender@example.com", "")).
		SetTo(gomailer.MustAddress("rcpt@example.org", "")).
		SetText([]byte("body"))
	if _, serr := tr.Send(context.Background(), msg, nil); serr != nil {
		t.Fatal(serr)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(args)
	if !strings.Contains(got, "-foo\n") {
		t.Fatalf("configured -foo argument was not preserved:\n%s", got)
	}
	if !strings.Contains(got, "-fsender@example.com\n") {
		t.Fatalf("-foo was treated as an existing -f sender; args:\n%s", got)
	}
}

func TestSendmailIDNAEncodesEnvelopeArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake sendmail is POSIX-only")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "fakesendmail.sh")
	content := "#!/bin/sh\n" +
		"printf '%s\n' \"$@\" > " + argsFile + "\n" +
		"cat >/dev/null\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	tr, err := NewSendmailTransport(script + " -t -i")
	if err != nil {
		t.Fatal(err)
	}
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("sender@dømi.fo", "")).
		SetTo(gomailer.MustAddress("rcpt@dømi.fo", "")).
		SetText([]byte("body"))
	if _, sendErr := tr.Send(context.Background(), msg, nil); sendErr != nil {
		t.Fatal(sendErr)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(args)
	for _, want := range []string{"-fsender@xn--dmi-0na.fo", "rcpt@xn--dmi-0na.fo"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sendmail args missing %q:\n%s", want, got)
		}
	}
}
