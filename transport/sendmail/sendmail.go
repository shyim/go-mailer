// Package sendmail implements a Transport that delivers mail by piping the
// serialized MIME message to a local sendmail-compatible binary (sendmail,
// Postfix, exim, ...).
package sendmail

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	gomailer "github.com/shyim/go-mailer"
)

// DefaultCommand is the command used when none is supplied. It uses the "-t"
// mode (the binary reads recipients from the message headers) plus "-i" to
// disable leading-dot interpretation. This is the mode that works with the
// raw-stdin delivery path implemented here.
//
// The "-bs" flag is deliberately not the default. It selects an interactive
// mode in which the caller must drive an EHLO/MAIL FROM/RCPT TO/DATA SMTP
// conversation over the process pipe. This transport only writes raw MIME to
// the binary's standard input and does not implement an SMTP-over-pipe driver,
// so "-bs" is unsupported (see NewSendmailTransport); piping raw MIME to a
// "-bs" binary would not deliver mail. The default therefore uses "-t -i",
// which the stdin path here delivers correctly out of the box.
const DefaultCommand = "/usr/sbin/sendmail -t -i"

// SendmailTransport delivers messages by spawning a sendmail-compatible binary
// and writing the raw RFC 5322 bytes to its standard input.
//
// Only the "-t" mode is supported: the binary reads recipients from the message
// headers, and the raw message bytes are piped to its standard input. Passing
// "-i" or "-oi" (as the default does) disables leading-dot interpretation.
//
// A "-f<sender>" flag is appended automatically when one is not already
// present, and explicit envelope recipients are appended after a "--"
// separator. When explicit recipients are given, a bare "-t" flag is dropped
// because the recipients on the command line take precedence over the header
// lookup that "-t" performs.
//
// The interactive "-bs" mode is intentionally rejected: it requires driving an
// SMTP conversation over the process pipe, which this transport does not
// implement.
type SendmailTransport struct {
	gomailer.BaseTransport
	command string
}

// NewSendmailTransport builds a SendmailTransport. An empty command falls back
// to DefaultCommand. The command must contain " -t"; the interactive " -bs"
// mode is not supported (it needs an SMTP-over-pipe driver this port lacks).
// Any other flag set returns an error wrapping ErrInvalidArgument.
func NewSendmailTransport(command string) (*SendmailTransport, error) {
	if command == "" {
		command = DefaultCommand
	}
	args := splitCommand(command)
	if hasExactArg(args, "-bs") {
		return nil, fmt.Errorf("%w: the interactive sendmail %q mode is not supported by this transport; use a %q-based command (e.g. %q)", gomailer.ErrInvalidArgument, "-bs", "-t", DefaultCommand)
	}
	if !hasExactArg(args, "-t") {
		return nil, fmt.Errorf("%w: unsupported sendmail command flags %q; the command must include %q", gomailer.ErrInvalidArgument, command, "-t")
	}

	t := &SendmailTransport{command: command}
	t.Name = "smtp://sendmail"
	t.DoSend = t.doSend
	return t, nil
}

// doSend builds the final command line and pipes the message bytes to the
// process standard input.
func (t *SendmailTransport) doSend(ctx context.Context, sm *gomailer.SentMessage) error {
	args := splitCommand(t.command)
	if len(args) == 0 {
		return gomailer.NewTransportError("sendmail: empty command")
	}

	recipients := sm.Envelope().Recipients()
	if len(recipients) > 0 {
		// Explicit recipients override the message-header lookup of "-t" mode.
		args = removeExactArg(args, "-t")
	} else if !hasExactArg(args, "-t") {
		// Without explicit recipients the binary must read them from the
		// message headers, which only "-t" mode does. A normally-derived
		// envelope always has at least one recipient, so this guards a
		// hand-built Envelope passed straight to BaseTransport.Send.
		return gomailer.NewTransportError("sendmail: no envelope recipients were provided and the command does not request \"-t\" to read them from the message headers")
	}

	if !hasSenderArg(args) {
		sender, err := sm.Envelope().Sender().EncodedEmail()
		if err != nil {
			return err
		}
		args = append(args, "-f"+sender)
	}

	// Sendmail expects LF line endings on stdin; normalize from CRLF.
	data := bytes.ReplaceAll(sm.Bytes(), []byte("\r\n"), []byte("\n"))

	// Dot-stuff leading dots unless the binary was told to ignore them.
	if !hasExactArg(args, "-i") && !hasExactArg(args, "-oi") {
		data = bytes.ReplaceAll(data, []byte("\n."), []byte("\n.."))
	}

	if len(recipients) > 0 {
		args = append(args, "--")
		for _, r := range recipients {
			recipient, err := r.EncodedEmail()
			if err != nil {
				return err
			}
			args = append(args, recipient)
		}
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = bytes.NewReader(data)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		te := gomailer.NewTransportError(fmt.Sprintf("process failed running %q: %v", strings.Join(args, " "), err))
		te.Cause = err
		if out.Len() > 0 {
			te.AppendDebug(out.String())
		}
		return te
	}
	if out.Len() > 0 {
		sm.AppendDebug(out.String())
	}
	return nil
}

func hasExactArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func removeExactArg(args []string, target string) []string {
	out := args[:0]
	for _, arg := range args {
		if arg == target {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func hasSenderArg(args []string) bool {
	for _, arg := range args {
		if arg == "-f" || (strings.HasPrefix(arg, "-f") && strings.Contains(arg[len("-f"):], "@")) {
			return true
		}
	}
	return false
}

// splitCommand splits a shell-like command string into arguments, honoring
// single and double quotes so paths or flags containing spaces survive intact.
func splitCommand(s string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	inField := false

	flush := func() {
		if inField {
			args = append(args, cur.String())
			cur.Reset()
			inField = false
		}
	}

	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inField = true
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
			inField = true
		}
	}
	flush()
	return args
}
