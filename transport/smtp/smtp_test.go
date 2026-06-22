package smtp

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport"
)

// fakeSMTPServer is an in-process SMTP server that speaks just enough of the
// protocol to drive a full client conversation. It records the commands and the
// DATA payload it received so tests can assert on the exact bytes the transport
// put on the wire. responses maps a command prefix (uppercased) to the line the
// server should reply with; unmatched commands get "250 OK".
type fakeSMTPServer struct {
	ln        net.Listener
	mu        sync.Mutex
	commands  []string
	data      strings.Builder
	responses map[string]string
}

func newFakeSMTPServer(t *testing.T, responses map[string]string) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTPServer{ln: ln, responses: responses}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTPServer) addr() (string, int) {
	a := s.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

func (s *fakeSMTPServer) reply(cmd string) string {
	up := strings.ToUpper(strings.TrimSpace(cmd))
	for prefix, resp := range s.responses {
		if strings.HasPrefix(up, strings.ToUpper(prefix)) {
			return resp
		}
	}
	return "250 OK"
}

func (s *fakeSMTPServer) serve() {
	conn, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	write := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}

	write("220 fake ESMTP ready")

	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		trimmed := strings.TrimRight(line, "\r\n")

		if inData {
			if trimmed == "." {
				inData = false
				write("250 Ok: queued as ABC123")
				continue
			}
			s.mu.Lock()
			s.data.WriteString(line)
			s.mu.Unlock()
			continue
		}

		s.mu.Lock()
		s.commands = append(s.commands, trimmed)
		s.mu.Unlock()

		up := strings.ToUpper(trimmed)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			// An "EHLO" entry in responses overrides the default greeting; it may
			// carry several CRLF-separated lines (e.g. a multiline AUTH advert).
			if override, ok := s.responses["EHLO"]; ok {
				for _, line := range strings.Split(override, "\r\n") {
					write(line)
				}
			} else {
				write("250-fake greets you")
				write("250 PIPELINING")
			}
		case strings.HasPrefix(up, "DATA"):
			write("354 End data with <CR><LF>.<CR><LF>")
			inData = true
		case strings.HasPrefix(up, "QUIT"):
			write("221 Bye")
			return
		default:
			write(s.reply(trimmed))
		}
	}
}

func (s *fakeSMTPServer) recordedCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.commands))
	copy(out, s.commands)
	return out
}

func (s *fakeSMTPServer) recordedData() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.String()
}

func buildMessage(t *testing.T) gomailer.RawMessage {
	t.Helper()
	return gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("sender@example.com", "Sender")).
		SetTo(gomailer.MustAddress("rcpt@example.org", "Recipient")).
		SetSubject("Hello").
		SetText([]byte("This is the body.\r\nWith two lines."))
}

func TestSMTPConversation(t *testing.T) {
	srv := newFakeSMTPServer(t, nil)
	host, port := srv.addr()

	tr := NewTransport(host, port, false)
	tr.SetAutoTLS(false) // no STARTTLS against the fake server
	t.Cleanup(func() { _ = tr.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sm, err := tr.Send(ctx, buildMessage(t), nil)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	cmds := srv.recordedCommands()
	joined := strings.Join(cmds, "\n")
	for _, want := range []string{"EHLO", "MAIL FROM:<sender@example.com>", "RCPT TO:<rcpt@example.org>", "DATA"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected command %q in conversation:\n%s", want, joined)
		}
	}

	data := srv.recordedData()
	if !strings.Contains(data, "Subject: Hello") {
		t.Errorf("DATA payload missing Subject header:\n%s", data)
	}
	if !strings.Contains(data, "This is the body.") {
		t.Errorf("DATA payload missing body:\n%s", data)
	}

	// Message-ID is parsed from the "250 Ok: queued as ABC123" final response.
	if sm.MessageID() != "ABC123" {
		t.Errorf("MessageID = %q, want ABC123", sm.MessageID())
	}
}

func TestSMTPDotStuffing(t *testing.T) {
	srv := newFakeSMTPServer(t, nil)
	host, port := srv.addr()
	tr := NewTransport(host, port, false)
	tr.SetAutoTLS(false)
	t.Cleanup(func() { _ = tr.Close() })

	// A body line beginning with a dot must be dot-stuffed on the wire and
	// un-stuffed by the server back to a single leading dot.
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("s@a.io", "")).
		SetTo(gomailer.MustAddress("r@b.io", "")).
		SetSubject("dots").
		SetText([]byte(".leading dot line\r\nnormal line"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tr.Send(ctx, msg, nil); err != nil {
		t.Fatalf("send: %v", err)
	}

	data := srv.recordedData()
	if !strings.Contains(data, ".leading dot line") {
		t.Errorf("server should have un-stuffed the leading dot back to one dot:\n%q", data)
	}
}

func TestSMTPDotStuffsRawMessageAtStart(t *testing.T) {
	srv := newFakeSMTPServer(t, nil)
	host, port := srv.addr()
	tr := NewTransport(host, port, false)
	tr.SetAutoTLS(false)
	t.Cleanup(func() { _ = tr.Close() })

	env, err := gomailer.NewEnvelope(
		gomailer.MustAddress("s@example.com", ""),
		[]gomailer.Address{gomailer.MustAddress("r@example.com", "")},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tr.Send(ctx, gomailer.NewRawMessage([]byte(".first\r\nsecond\r\n")), env); err != nil {
		t.Fatal(err)
	}
	if got := srv.recordedData(); !strings.HasPrefix(got, "..first\r\n") {
		t.Fatalf("raw DATA was not dot-stuffed at stream start: %q", got)
	}
}

func TestSMTPIDNAEncodesEnvelopeDomains(t *testing.T) {
	srv := newFakeSMTPServer(t, nil)
	host, port := srv.addr()
	tr := NewTransport(host, port, false)
	tr.SetAutoTLS(false)
	t.Cleanup(func() { _ = tr.Close() })

	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("info@dømi.fo", "")).
		SetTo(gomailer.MustAddress("rcpt@dømi.fo", "")).
		SetText([]byte("body"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tr.Send(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(srv.recordedCommands(), "\n")
	for _, want := range []string{"MAIL FROM:<info@xn--dmi-0na.fo>", "RCPT TO:<rcpt@xn--dmi-0na.fo>"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in SMTP commands:\n%s", want, joined)
		}
	}
}

func TestSMTPContextDeadlineAppliesDuringGreeting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr == nil {
			accepted <- conn
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	tr := NewTransport("127.0.0.1", addr.Port, false)
	tr.SetAutoTLS(false)
	t.Cleanup(func() { _ = tr.Close() })
	t.Cleanup(func() {
		select {
		case conn := <-accepted:
			_ = conn.Close()
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = tr.Send(ctx, buildMessage(t), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("send error = %v, want context deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("deadline was not honored promptly; elapsed=%s", elapsed)
	}
}

func TestSMTPDataTerminatorDoesNotAddBlankLine(t *testing.T) {
	srv := newFakeSMTPServer(t, nil)
	host, port := srv.addr()
	tr := NewTransport(host, port, false)
	tr.SetAutoTLS(false)
	t.Cleanup(func() { _ = tr.Close() })

	env, err := gomailer.NewEnvelope(
		gomailer.MustAddress("s@example.com", ""),
		[]gomailer.Address{gomailer.MustAddress("r@example.com", "")},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("Subject: hi\r\n\r\nbody\r\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tr.Send(ctx, gomailer.NewRawMessage(raw), env); err != nil {
		t.Fatal(err)
	}
	if got := srv.recordedData(); got != string(raw) {
		t.Fatalf("DATA payload changed:\ngot  %q\nwant %q", got, string(raw))
	}
}

func TestSMTPExecuteCommandTerminatesAfterWriteFailure(t *testing.T) {
	client, server := net.Pipe()
	_ = server.Close()
	tr := NewTransport("example.com", 25, false)
	tr.conn = client
	tr.started = true
	tr.text = bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client))

	if _, err := tr.executeCommand("NOOP\r\n", []int{250}); err == nil {
		t.Fatal("expected write failure")
	}
	if tr.started || tr.conn != nil || tr.text != nil {
		t.Fatalf("transport should be terminated after write failure; started=%v conn=%v text=%v", tr.started, tr.conn, tr.text)
	}
}

func TestEsmtpFactoryAutoTLSDefaults(t *testing.T) {
	tests := []struct {
		dsn  string
		tls  bool
		port int
	}{
		{"smtp://localhost", false, 25},
		{"smtp://127.0.0.1", false, 25},
		{"smtp://mail.example.com", true, 465},
		{"smtp://mail.example.com:465", true, 465},
		{"smtp://mail.example.com:465?auto_tls=false", false, 465},
		{"smtp://mail.example.com:587", false, 587},
		{"smtps://mail.example.com", true, 465},
	}
	for _, tc := range tests {
		t.Run(tc.dsn, func(t *testing.T) {
			d, err := transport.ParseDSN(tc.dsn)
			if err != nil {
				t.Fatal(err)
			}
			got, err := NewEsmtpFactory().Create(d, transport.Deps{})
			if err != nil {
				t.Fatal(err)
			}
			tr := got.(*Transport)
			if tr.tls != tc.tls || tr.port != tc.port {
				t.Fatalf("tls/port = %v/%d, want %v/%d", tr.tls, tr.port, tc.tls, tc.port)
			}
		})
	}
}

func TestSMTPRejectedRecipient(t *testing.T) {
	// Server rejects RCPT TO with a 550.
	srv := newFakeSMTPServer(t, map[string]string{
		"RCPT TO": "550 No such user here",
	})
	host, port := srv.addr()
	tr := NewTransport(host, port, false)
	tr.SetAutoTLS(false)
	t.Cleanup(func() { _ = tr.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := tr.Send(ctx, buildMessage(t), nil)
	if err == nil {
		t.Fatal("expected error on rejected recipient")
	}
}

func TestSMTPTransportName(t *testing.T) {
	tr := NewTransport("mail.example.com", 25, false)
	if got := tr.String(); !strings.HasPrefix(got, "smtp://mail.example.com") {
		t.Errorf("String() = %q, want smtp://mail.example.com prefix", got)
	}
	tls := NewTransport("mail.example.com", 465, true)
	if got := tls.String(); !strings.HasPrefix(got, "smtps://mail.example.com") {
		t.Errorf("TLS String() = %q, want smtps:// prefix", got)
	}
	tlsPort25 := NewTransport("mail.example.com", 25, true)
	if got := tlsPort25.String(); got != "smtps://mail.example.com:25" {
		t.Errorf("TLS port 25 String() = %q, want smtps://mail.example.com:25", got)
	}
}

// --- Production-readiness regression tests ---

// silentServer accepts a connection, optionally writes a greeting, then goes
// silent forever — modeling a blackholed/stalled relay.
func silentServer(t *testing.T, greeting string) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		if greeting != "" {
			_, _ = conn.Write([]byte(greeting))
		}
		// Hold the connection open and never respond further.
		buf := make([]byte, 256)
		for {
			if _, rerr := conn.Read(buf); rerr != nil {
				return
			}
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

// A hung server must not block Send forever when the caller's context has no
// deadline: the default IO timeout has to kick in. (#2)
func TestSMTPDefaultIOTimeoutOnHungServer(t *testing.T) {
	host, port := silentServer(t, "220 hello\r\n") // greets, then never answers EHLO
	tr := NewTransport(host, port, false)
	tr.SetAutoTLS(false)
	tr.SetTimeout(150 * time.Millisecond) // short fallback deadline
	t.Cleanup(func() { _ = tr.Close() })

	done := make(chan error, 1)
	start := time.Now()
	go func() { _, err := tr.Send(context.Background(), buildMessage(t), nil); done <- err }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error from the hung server, got nil")
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("send took %s; default IO timeout did not bound it", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Send hung past the IO timeout with a context.Background() — the default deadline is not applied")
	}
}

// A truncated/mid-line response (connection drops after partial bytes) must be
// reported as an error, NOT silently accepted as a successful response. (#1)
func TestSMTPTruncatedResponseIsError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		// Greeting, then a truncated EHLO reply with NO terminating CRLF before
		// the connection is closed.
		_, _ = conn.Write([]byte("220 ok\r\n"))
		buf := make([]byte, 256)
		_, _ = conn.Read(buf) // read EHLO
		_, _ = conn.Write([]byte("250-partial line without terminator"))
		_ = conn.Close()
	}()
	a := ln.Addr().(*net.TCPAddr)
	tr := NewTransport("127.0.0.1", a.Port, false)
	tr.SetAutoTLS(false)
	tr.SetTimeout(time.Second)
	t.Cleanup(func() { _ = tr.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := tr.Send(ctx, buildMessage(t), nil); err == nil {
		t.Fatal("a truncated SMTP response was accepted as success; must be a TransportError")
	}
}

// Non-ASCII domains in From/To must be punycoded in the rendered RFC 5322
// headers (not just the envelope), so DATA is 7-bit clean. (#3)
func TestSMTPHeadersPunycodeNonASCIIDomain(t *testing.T) {
	srv := newFakeSMTPServer(t, nil)
	host, port := srv.addr()
	tr := NewTransport(host, port, false)
	tr.SetAutoTLS(false)
	t.Cleanup(func() { _ = tr.Close() })

	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("info@dømi.fo", "Info")).
		SetTo(gomailer.MustAddress("rcpt@dømi.fo", "")).
		SetText([]byte("body"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tr.Send(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	data := srv.recordedData()
	if strings.ContainsRune(data, 'ø') {
		t.Errorf("DATA headers leaked a raw non-ASCII domain octet:\n%s", data)
	}
	if !strings.Contains(data, "xn--dmi-0na.fo") {
		t.Errorf("From/To header domain was not punycoded:\n%s", data)
	}
}

// Credentials must not be sent over a cleartext connection by default. (#4)
func TestSMTPRefusesPlaintextAuth(t *testing.T) {
	srv := newFakeSMTPServer(t, map[string]string{
		"EHLO": "250-fake\r\n250 AUTH PLAIN LOGIN", // advertise AUTH, no STARTTLS
	})
	host, port := srv.addr()
	tr := NewTransport(host, port, false)
	tr.SetAutoTLS(false)
	tr.SetUsername("user")
	tr.SetPassword("secret")
	t.Cleanup(func() { _ = tr.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := tr.Send(ctx, buildMessage(t), nil)
	if err == nil {
		t.Fatal("expected refusal to send AUTH over a cleartext connection")
	}
	// The password must never appear in the conversation recorded by the server.
	if strings.Contains(srv.recordedData(), "secret") {
		t.Error("credential material reached the wire over cleartext")
	}
	for _, cmd := range srv.recordedCommands() {
		if strings.HasPrefix(strings.ToUpper(cmd), "AUTH") {
			t.Errorf("AUTH was attempted over cleartext: %q", cmd)
		}
	}
}
