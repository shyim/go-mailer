// Package smtp implements an ESMTP transport. It drives the SMTP/ESMTP
// conversation by hand over a net.Conn (EHLO/HELO, STARTTLS, AUTH
// PLAIN/LOGIN/CRAM-MD5, MAIL FROM, RCPT TO, DATA with dot-stuffing, QUIT),
// asserts response codes, and plugs into the send pipeline through
// gomailer.BaseTransport.DoSend.
package smtp

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/idna"

	gomailer "github.com/shyim/go-mailer"
)

// defaultRestartThreshold is the number of messages sent before the transport
// reconnects, refreshing a connection some relays drop or throttle once it has
// handled many messages.
const defaultRestartThreshold = 100

// defaultPingThreshold is the number of seconds of idleness after which the
// server is pinged (NOOP) before sending the next message.
const defaultPingThreshold = 100

// dialTimeout bounds the TCP/TLS connection establishment only.
const dialTimeout = 30 * time.Second

// defaultIOTimeout is the fallback per-operation read/write deadline applied to
// the socket when the caller's context carries no deadline. Without it, a
// blackholed or stalled relay would block Send forever (no read ever returns),
// hanging the calling goroutine and any worker pool behind it. Override with
// SetTimeout (or the "timeout" DSN option).
const defaultIOTimeout = 30 * time.Second

// Authenticator performs an ESMTP AUTH exchange for a single mechanism.
type Authenticator interface {
	// Keyword returns the AUTH mechanism keyword (e.g. "PLAIN").
	Keyword() string
	// Authenticate runs the mechanism against the connection.
	Authenticate(t *Transport) error
}

// Transport sends emails over SMTP/ESMTP. It embeds gomailer.BaseTransport and
// supplies a DoSend hook that performs the protocol conversation. The zero
// value is not usable; construct it with NewTransport. A Transport keeps the
// underlying connection open across sends for reuse; call Close to disconnect.
type Transport struct {
	gomailer.BaseTransport

	host string
	port int
	tls  bool // true => implicit TLS on connect (smtps)

	username           string
	password           string
	autoTLS            bool
	requireTLS         bool
	allowPlaintextAuth bool

	localDomain string
	sourceIP    string
	tlsConfig   *tls.Config

	authenticators []Authenticator

	restartThreshold      int
	restartThresholdSleep int
	restartCounter        int
	pingThreshold         int
	ioTimeout             time.Duration

	mu              sync.Mutex
	conn            net.Conn
	text            *bufio.ReadWriter
	started         bool
	usingTLS        bool
	ctxHasDeadline  bool // whether the in-flight send's context carries a deadline
	capabilities    map[string][]string
	lastMessageTime time.Time
	debug           strings.Builder
}

// NewTransport builds an ESMTP transport for host:port. When tls is true the
// connection is wrapped in implicit TLS on connect (smtps). A zero port is
// resolved to 465 (TLS) or 25 (plain). The default authenticator order is
// CRAM-MD5, LOGIN, PLAIN.
func NewTransport(host string, port int, tlsOnConnect bool) *Transport {
	if port == 0 {
		if tlsOnConnect {
			port = 465
		} else {
			port = 25
		}
	}
	t := &Transport{
		host:             host,
		port:             port,
		tls:              tlsOnConnect,
		autoTLS:          true,
		localDomain:      "[127.0.0.1]",
		restartThreshold: defaultRestartThreshold,
		pingThreshold:    defaultPingThreshold,
		ioTimeout:        defaultIOTimeout,
		capabilities:     map[string][]string{},
		// Prefer PLAIN/LOGIN (used over an established TLS channel by default)
		// over the legacy CRAM-MD5 (HMAC-MD5, requires recoverable server-side
		// password storage). Override the order with SetAuthenticators.
		authenticators: []Authenticator{
			PlainAuthenticator{},
			LoginAuthenticator{},
			CramMD5Authenticator{},
		},
	}
	t.DoSend = t.doSend
	t.Name = t.computeName()
	return t
}

// SetUsername sets the AUTH username (empty disables authentication).
func (t *Transport) SetUsername(u string) *Transport { t.username = u; return t }

// Username returns the AUTH username.
func (t *Transport) Username() string { return t.username }

// SetPassword sets the AUTH password/secret.
func (t *Transport) SetPassword(p string) *Transport { t.password = p; return t }

// Password returns the AUTH password/secret.
func (t *Transport) Password() string { return t.password }

// SetAutoTLS toggles opportunistic STARTTLS when the server advertises it.
func (t *Transport) SetAutoTLS(v bool) *Transport { t.autoTLS = v; return t }

// SetRequireTLS requires that TLS (implicit or STARTTLS) be in use before mail.
func (t *Transport) SetRequireTLS(v bool) *Transport { t.requireTLS = v; return t }

// SetAllowPlaintextAuth permits sending AUTH credentials over a connection that
// is not protected by TLS. It defaults to false: by default the transport
// refuses to authenticate in the clear, since an active attacker can strip
// STARTTLS to capture credentials. Enable only for trusted local relays.
func (t *Transport) SetAllowPlaintextAuth(v bool) *Transport { t.allowPlaintextAuth = v; return t }

// SetTimeout sets the fallback per-operation read/write deadline used when a
// send's context carries no deadline of its own (<=0 disables the fallback, at
// the risk of a hung server blocking a send indefinitely). A context deadline,
// when present, always takes precedence over this value.
func (t *Transport) SetTimeout(d time.Duration) *Transport { t.ioTimeout = d; return t }

// SetLocalDomain sets the HELO/EHLO domain. Bare IPv4/IPv6 literals are wrapped
// in brackets per RFC 5321 section 4.1.3.
func (t *Transport) SetLocalDomain(domain string) *Transport {
	if domain != "" && domain[0] != '[' {
		if ip := net.ParseIP(domain); ip != nil {
			if ip.To4() != nil {
				domain = "[" + domain + "]"
			} else {
				domain = "[IPv6:" + domain + "]"
			}
		}
	}
	t.localDomain = domain
	return t
}

// LocalDomain returns the HELO/EHLO domain.
func (t *Transport) LocalDomain() string { return t.localDomain }

// SetSourceIP binds the outgoing connection to the given local IP.
func (t *Transport) SetSourceIP(ip string) *Transport { t.sourceIP = ip; return t }

// SetTLSConfig overrides the TLS configuration used for implicit TLS and
// STARTTLS (e.g. to disable peer verification).
func (t *Transport) SetTLSConfig(c *tls.Config) *Transport { t.tlsConfig = c; return t }

// SetAuthenticators replaces the ordered list of AUTH mechanisms to try.
func (t *Transport) SetAuthenticators(a []Authenticator) *Transport {
	t.authenticators = a
	return t
}

// SetRestartThreshold sets how many messages to send before reconnecting and an
// optional sleep (seconds) between stop and restart. Zero disables restarting.
func (t *Transport) SetRestartThreshold(threshold, sleep int) *Transport {
	t.restartThreshold = threshold
	t.restartThresholdSleep = sleep
	return t
}

// SetPingThreshold sets the idle seconds after which the server is pinged
// (NOOP) before the next message is sent.
func (t *Transport) SetPingThreshold(seconds int) *Transport {
	t.pingThreshold = seconds
	return t
}

func (t *Transport) computeName() string {
	scheme := "smtp"
	if t.tls {
		scheme = "smtps"
	}
	name := fmt.Sprintf("%s://%s", scheme, t.host)
	if (!t.tls && t.port != 25) || (t.tls && t.port != 465) {
		name += ":" + strconv.Itoa(t.port)
	}
	return name
}

// String returns the DSN-like identity of the transport (e.g. "smtp://host").
func (t *Transport) String() string { return t.computeName() }

// Send runs the send pipeline (via BaseTransport) and applies the restart
// threshold. On a transport error it attempts an RSET so the connection can be
// reused for the next message instead of being torn down.
func (t *Transport) Send(ctx context.Context, msg gomailer.RawMessage, envelope *gomailer.Envelope) (*gomailer.SentMessage, error) {
	sm, err := t.BaseTransport.Send(ctx, msg, envelope)
	if err != nil {
		// A local validation failure (ErrInvalidArgument, e.g. SMTPUTF8 needed
		// but unsupported) is not a transport error and did not put the
		// connection in a state needing RSET, so skip it for those.
		if !errors.Is(err, gomailer.ErrInvalidArgument) {
			t.mu.Lock()
			if t.started {
				// best-effort reset; ignore failures (server may be done with us)
				if _, rerr := t.executeCommand("RSET\r\n", []int{250}); rerr != nil {
					t.terminate()
				}
			}
			t.mu.Unlock()
		}
		return nil, err
	}
	t.mu.Lock()
	t.checkRestartThreshold()
	t.mu.Unlock()
	return sm, err
}

// doSend is the BaseTransport.DoSend hook performing the SMTP conversation.
func (t *Transport) doSend(ctx context.Context, sm *gomailer.SentMessage) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Discard any transcript left over from a previous (possibly failed) send so
	// this message's Debug() only reflects its own conversation, and t.debug
	// cannot grow unbounded across repeated failed starts.
	t.debug.Reset()

	_, t.ctxHasDeadline = ctx.Deadline()

	cleanupContext := t.bindCurrentConnectionContext(ctx)
	defer func() { cleanupContext() }()

	if !t.lastMessageTime.IsZero() && time.Since(t.lastMessageTime) > time.Duration(t.pingThreshold)*time.Second {
		t.ping()
	}

	if err := t.start(ctx); err != nil {
		return err
	}

	cleanupContext()
	cleanupContext = t.bindCurrentConnectionContext(ctx)

	env := sm.Envelope()
	utf8 := env.AnyAddressHasUnicodeLocalPart()

	if err := t.mailFrom(env.Sender().Email(), utf8); err != nil {
		t.appendErrDebug(err)
		t.lastMessageTime = time.Time{}
		return t.contextErr(ctx, err)
	}
	for _, rcpt := range env.Recipients() {
		if err := t.rcptTo(rcpt.Email()); err != nil {
			t.appendErrDebug(err)
			t.lastMessageTime = time.Time{}
			return t.contextErr(ctx, err)
		}
	}

	if _, err := t.executeCommand("DATA\r\n", []int{354}); err != nil {
		t.appendErrDebug(err)
		t.lastMessageTime = time.Time{}
		return t.contextErr(ctx, err)
	}

	endsWithCRLF, err := t.writeData(sm.Bytes())
	if err != nil {
		t.appendErrDebug(err)
		t.lastMessageTime = time.Time{}
		return t.contextErr(ctx, err)
	}

	terminator := ".\r\n"
	if !endsWithCRLF {
		terminator = "\r\n.\r\n"
	}

	resp, err := t.executeCommand(terminator, []int{250})
	if err != nil {
		t.appendErrDebug(err)
		t.lastMessageTime = time.Time{}
		return t.contextErr(ctx, err)
	}
	sm.AppendDebug(t.drainDebug())
	t.lastMessageTime = time.Now()

	if id := parseMessageID(resp); id != "" {
		sm.SetMessageID(id)
	}
	return nil
}

// writeData writes the message body to the DATA stream, performing CRLF
// normalization and dot-stuffing ("\r\n." => "\r\n.."), then flushes.
func (t *Transport) writeData(data []byte) (bool, error) {
	stuffed := dotStuff(normalizeCRLF(data))
	endsWithCRLF := bytesHasSuffix(stuffed, []byte("\r\n"))
	if _, err := t.text.Write(stuffed); err != nil {
		// teardown so a stale connection is not reused
		t.terminate()
		return false, gomailer.NewTransportError("unable to write message data: " + err.Error())
	}
	if err := t.text.Flush(); err != nil {
		t.terminate()
		return false, gomailer.NewTransportError("unable to flush message data: " + err.Error())
	}
	return endsWithCRLF, nil
}

func bytesHasSuffix(data, suffix []byte) bool {
	if len(data) < len(suffix) {
		return false
	}
	offset := len(data) - len(suffix)
	for i := range suffix {
		if data[offset+i] != suffix[i] {
			return false
		}
	}
	return true
}

// start opens the connection (if needed), reads the greeting, sends EHLO/HELO,
// performs STARTTLS and AUTH. Must be called with the mutex held.
func (t *Transport) start(ctx context.Context) error {
	if t.started {
		return nil
	}
	if err := t.connect(ctx); err != nil {
		return err
	}
	cleanupContext := t.bindCurrentConnectionContext(ctx)
	defer cleanupContext()

	resp, rerr := t.readResponse()
	if rerr != nil {
		t.terminate()
		return t.contextErr(ctx, rerr)
	}
	if _, err := t.assertResponse(resp); err != nil {
		t.terminate()
		return t.contextErr(ctx, err)
	}
	if err := t.helo(ctx); err != nil {
		t.terminate()
		return t.contextErr(ctx, err)
	}
	t.started = true
	t.lastMessageTime = time.Time{}
	return nil
}

// connect dials the TCP socket and, for smtps, wraps it in implicit TLS.
func (t *Transport) connect(ctx context.Context) error {
	addr := net.JoinHostPort(t.host, strconv.Itoa(t.port))
	dialer := &net.Dialer{Timeout: dialTimeout}
	if t.sourceIP != "" {
		ip := strings.Trim(t.sourceIP, "[]")
		dialer.LocalAddr = &net.TCPAddr{IP: net.ParseIP(ip)}
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return gomailer.NewTransportError(fmt.Sprintf("connection could not be established with host %q: %s", addr, err))
	}
	if t.tls {
		cleanupContext := bindConnectionContext(ctx, conn, t.ioTimeout)
		defer cleanupContext()
		tconn := tls.Client(conn, t.effectiveTLSConfig())
		if err := tconn.Handshake(); err != nil {
			_ = conn.Close()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return gomailer.NewTransportError("TLS handshake failed: " + err.Error())
		}
		conn = tconn
		t.usingTLS = true
	} else {
		t.usingTLS = false
	}
	t.conn = conn
	t.text = bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	return nil
}

func (t *Transport) effectiveTLSConfig() *tls.Config {
	if t.tlsConfig != nil {
		c := t.tlsConfig.Clone()
		if c.ServerName == "" {
			c.ServerName = t.host
		}
		return c
	}
	return &tls.Config{ServerName: t.host, MinVersion: tls.VersionTLS12}
}

// helo issues EHLO (falling back to HELO), then STARTTLS and AUTH as needed.
func (t *Transport) helo(ctx context.Context) error {
	resp, err := t.executeCommand(fmt.Sprintf("EHLO %s\r\n", t.localDomain), []int{250})
	if err != nil {
		// fall back to plain HELO
		if _, herr := t.executeCommand(fmt.Sprintf("HELO %s\r\n", t.localDomain), []int{250}); herr != nil {
			if code(herr) == 0 {
				return t.contextErr(ctx, err)
			}
			return t.contextErr(ctx, herr)
		}
		// HELO yields no ESMTP capabilities; clear any stale map carried over
		// from a previous EHLO session (parseCapabilities only runs on EHLO).
		t.capabilities = map[string][]string{}
		return nil
	}
	t.capabilities = parseCapabilities(resp)

	tlsStarted := t.usingTLS
	if t.autoTLS && !t.usingTLS {
		if _, ok := t.capabilities["STARTTLS"]; ok {
			if _, serr := t.executeCommand("STARTTLS\r\n", []int{220}); serr != nil {
				return t.contextErr(ctx, serr)
			}
			if serr := t.startTLS(ctx); serr != nil {
				return serr
			}
			tlsStarted = true
			resp, err = t.executeCommand(fmt.Sprintf("EHLO %s\r\n", t.localDomain), []int{250})
			if err != nil {
				return t.contextErr(ctx, err)
			}
			t.capabilities = parseCapabilities(resp)
		}
	}

	if !tlsStarted && t.requireTLS {
		return gomailer.NewTransportError("TLS required but neither TLS nor STARTTLS are in use.")
	}

	if modes, ok := t.capabilities["AUTH"]; ok {
		// Refuse to send credentials over a cleartext connection. An active MitM
		// can strip the advertised STARTTLS capability to force AUTH in the
		// clear; without this guard PLAIN/LOGIN would base64 the username and
		// password straight onto the wire. The caller must opt in explicitly
		// (SetAllowPlaintextAuth(true)) to authenticate without TLS.
		if t.username != "" && !tlsStarted && !t.allowPlaintextAuth {
			return gomailer.NewTransportError("refusing to send AUTH credentials over an unencrypted connection (no TLS/STARTTLS); enable TLS or call SetAllowPlaintextAuth(true) to override")
		}
		if err := t.handleAuth(modes); err != nil {
			return t.contextErr(ctx, err)
		}
	}
	return nil
}

// startTLS upgrades the live connection to TLS after a 220 STARTTLS reply.
func (t *Transport) startTLS(ctx context.Context) error {
	cleanupContext := t.bindCurrentConnectionContext(ctx)
	defer cleanupContext()
	tconn := tls.Client(t.conn, t.effectiveTLSConfig())
	if err := tconn.Handshake(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return gomailer.NewTransportError("unable to connect with STARTTLS: " + err.Error())
	}
	t.conn = tconn
	t.usingTLS = true
	t.text = bufio.NewReadWriter(bufio.NewReader(tconn), bufio.NewWriter(tconn))
	return nil
}

// serverSupportsSMTPUTF8 reports whether the server advertised SMTPUTF8.
func (t *Transport) serverSupportsSMTPUTF8() bool {
	_, ok := t.capabilities["SMTPUTF8"]
	return ok
}

// handleAuth tries each configured authenticator whose keyword the server
// advertises, in configured order, until one succeeds.
func (t *Transport) handleAuth(modes []string) error {
	if t.username == "" {
		return nil
	}
	advertised := map[string]bool{}
	for _, m := range modes {
		advertised[strings.ToUpper(m)] = true
	}

	var lastCode int
	var authNames []string
	errs := map[string]string{}

	for _, a := range t.authenticators {
		if !advertised[strings.ToUpper(a.Keyword())] {
			continue
		}
		authNames = append(authNames, a.Keyword())
		if err := a.Authenticate(t); err != nil {
			lastCode = code(err)
			// reset and try the next authenticator
			_, _ = t.executeCommand("RSET\r\n", []int{250})
			errs[a.Keyword()] = err.Error()
			continue
		}
		return nil
	}

	if len(authNames) == 0 {
		c := lastCode
		if c == 0 {
			c = 504
		}
		te := gomailer.NewTransportError(fmt.Sprintf("Failed to find an authenticator supported by the SMTP server, which currently supports: %q.", strings.Join(modes, ", ")))
		te.Code = c
		return te
	}

	msg := fmt.Sprintf("Failed to authenticate on SMTP server with username %q using the following authenticators: %q.", t.username, strings.Join(authNames, ", "))
	for name, e := range errs {
		msg += fmt.Sprintf(" Authenticator %q returned %q.", name, e)
	}
	c := lastCode
	if c == 0 {
		c = 535
	}
	te := gomailer.NewTransportError(msg)
	te.Code = c
	return te
}

// mailFrom issues the MAIL FROM command, adding SMTPUTF8 when needed.
func (t *Transport) mailFrom(address string, smtputf8 bool) error {
	encoded, err := encodeSMTPAddress(address)
	if err != nil {
		return err
	}
	if smtputf8 && !t.serverSupportsSMTPUTF8() {
		// A local validation failure, not a transport error: the address needs
		// SMTPUTF8 but the server does not advertise it, so we reject before
		// touching the wire. Wrapping ErrInvalidArgument lets errors.Is match it,
		// and Send's RSET path skips it since the connection state is untouched.
		return fmt.Errorf("%w: invalid addresses: non-ASCII characters not supported in local-part of email", gomailer.ErrInvalidArgument)
	}
	ext := ""
	if smtputf8 {
		ext = " SMTPUTF8"
	}
	_, err = t.executeCommand(fmt.Sprintf("MAIL FROM:<%s>%s\r\n", encoded, ext), []int{250})
	return err
}

// rcptTo issues the RCPT TO command for a single recipient.
func (t *Transport) rcptTo(address string) error {
	encoded, err := encodeSMTPAddress(address)
	if err != nil {
		return err
	}
	_, err = t.executeCommand(fmt.Sprintf("RCPT TO:<%s>\r\n", encoded), []int{250, 251, 252})
	return err
}

func encodeSMTPAddress(address string) (string, error) {
	at := strings.LastIndexByte(address, '@')
	if at < 0 || at == len(address)-1 {
		return address, nil
	}
	local, domain := address[:at], address[at+1:]
	if strings.HasPrefix(domain, "[") && strings.HasSuffix(domain, "]") {
		return address, nil
	}
	ascii, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return "", fmt.Errorf("%w: invalid address domain %q: %w", gomailer.ErrInvalidArgument, domain, err)
	}
	return local + "@" + ascii, nil
}

// ping sends a NOOP to verify the connection is alive, stopping on failure.
// Must be called with the mutex held.
func (t *Transport) ping() {
	if !t.started {
		return
	}
	if _, err := t.executeCommand("NOOP\r\n", []int{250}); err != nil {
		t.stop()
	}
}

// checkRestartThreshold disconnects after restartThreshold messages so the next
// send opens a fresh connection. Must be called with the mutex held.
//
// It runs after a send has completed, outside that send's context, so it does
// NOT eagerly reconnect here: an eager reconnect would have to use a
// deadline-less context and could block every sender on a hung relay while
// holding the mutex. Instead it tears the connection down (lazy reconnect); the
// next doSend calls start(ctx) with that send's real, cancellable context.
func (t *Transport) checkRestartThreshold() {
	if !t.started || t.restartThreshold == 0 {
		return
	}
	t.restartCounter++
	if t.restartCounter < t.restartThreshold {
		return
	}
	t.stop()
	t.restartCounter = 0
	if t.restartThresholdSleep > 0 {
		time.Sleep(time.Duration(t.restartThresholdSleep) * time.Second)
	}
}

// stop sends QUIT and tears down the connection. Must be called with the mutex
// held.
func (t *Transport) stop() {
	if !t.started {
		return
	}
	_, _ = t.executeCommand("QUIT\r\n", []int{221})
	t.terminate()
}

// terminate closes and forgets the connection. Must be called with the mutex
// held.
func (t *Transport) terminate() {
	if t.conn != nil {
		_ = t.conn.Close()
	}
	t.conn = nil
	t.text = nil
	t.started = false
	t.usingTLS = false
}

// Close disconnects from the SMTP server (QUIT + close). It is safe to call on
// an already-closed transport and to send again afterwards (the connection is
// reopened on demand).
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stop()
	return nil
}

func (t *Transport) bindCurrentConnectionContext(ctx context.Context) func() {
	if t.conn == nil {
		return func() {}
	}
	return bindConnectionContext(ctx, t.conn, t.ioTimeout)
}

// bindConnectionContext applies the effective socket deadline and arranges for
// the connection to be closed if ctx is cancelled. The deadline is the caller's
// context deadline when present; otherwise a fallback of ioTimeout from now so a
// stalled server cannot block a read/write indefinitely. The returned cleanup
// stops the close hook and clears the deadline.
func bindConnectionContext(ctx context.Context, conn net.Conn, ioTimeout time.Duration) func() {
	if conn == nil {
		return func() {}
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else if ioTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	}
	stopClose := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	return func() {
		stopClose()
		_ = conn.SetDeadline(time.Time{})
	}
}

// contextErr maps a low-level failure to a context error when the context was
// actually cancelled/expired, so callers see context.Canceled/DeadlineExceeded
// rather than an opaque "connection closed". But when err already carries a real
// SMTP response (a *gomailer.TransportError with a non-zero Code), that
// diagnostic is preserved even at the deadline boundary — losing the SMTP code
// and transcript to a bare context error would be strictly worse for the caller.
func (t *Transport) contextErr(ctx context.Context, err error) error {
	var te *gomailer.TransportError
	if errors.As(err, &te) && te.Code != 0 {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return err
}

// ExecuteCommand writes a command and asserts the response codes. It is
// exported so Authenticator implementations can drive the conversation. The
// caller (an authenticator invoked from within doSend) already holds the mutex.
func (t *Transport) ExecuteCommand(command string, codes []int) (string, error) {
	return t.executeCommand(command, codes)
}

// executeCommand writes a command, reads the full multiline response and
// asserts the expected response codes. Must be called with the mutex held.
func (t *Transport) executeCommand(command string, codes []int) (string, error) {
	return t.execute(command, codes, false)
}

// executeCommandRedacted is like executeCommand but masks the command argument
// in the debug transcript, so AUTH credentials (base64 user/pass, CRAM-MD5
// response) are not written to Debug()/error transcripts surfaced to callers.
func (t *Transport) executeCommandRedacted(command string, codes []int) (string, error) {
	return t.execute(command, codes, true)
}

// refreshIOTimeout re-arms the fallback per-operation deadline before a command
// when the caller's context carries no deadline of its own. With a context
// deadline, that absolute deadline is already bound for the whole conversation
// and must not be extended here. ctx is the context bound for the current send;
// it is stored on the transport for the duration of doSend.
func (t *Transport) refreshIOTimeout() {
	if t.conn == nil || t.ioTimeout <= 0 {
		return
	}
	if t.ctxHasDeadline {
		return
	}
	_ = t.conn.SetDeadline(time.Now().Add(t.ioTimeout))
}

// execute is the shared implementation behind executeCommand(Redacted).
func (t *Transport) execute(command string, codes []int, redact bool) (string, error) {
	if t.text == nil {
		return "", gomailer.NewTransportError("connection is not established")
	}
	t.refreshIOTimeout()
	if redact {
		t.recordDebug(">", redactCommand(command))
	} else {
		t.recordDebug(">", command)
	}
	if _, err := t.text.WriteString(command); err != nil {
		t.terminate()
		return "", gomailer.NewTransportError("unable to write bytes on the wire: " + err.Error())
	}
	if err := t.text.Flush(); err != nil {
		t.terminate()
		return "", gomailer.NewTransportError("unable to write bytes on the wire: " + err.Error())
	}
	resp, rerr := t.readResponse()
	if rerr != nil {
		// the connection is dead; tear it down so it is not reused
		t.terminate()
		return resp, rerr
	}
	return t.assertResponse2(resp, codes)
}

// maxResponseSize caps the total bytes buffered for a single SMTP response. RFC
// 5321 limits a reply line to 512 octets and real multiline replies (EHLO) stay
// well under a few KB; a server that streams without a terminating line would
// otherwise drive unbounded memory growth. Past this cap the response is treated
// as a protocol error and the connection is torn down by the caller.
const maxResponseSize = 64 * 1024

// readResponse reads a complete (possibly multiline) SMTP response. A line is a
// continuation when its 4th byte is '-'; the final line has a space there.
//
// Any read error is propagated as a TransportError so the caller tears the
// connection down — including a read error that arrives AFTER some bytes were
// already buffered. A mid-multiline or mid-line truncation must never be
// reported as a successful response: doing so silently drops advertised
// capabilities (e.g. STARTTLS, downgrading opportunistic TLS to cleartext) and
// leaves unread bytes that desync every subsequent command on a reused
// connection. The response is only considered complete when a line whose 4th
// byte is a space (the SMTP terminal-line marker) has been read in full.
func (t *Transport) readResponse() (string, error) {
	var sb strings.Builder
	for {
		line, err := t.text.ReadString('\n')
		if line != "" {
			t.recordDebug("<", line)
			sb.WriteString(line)
		}
		if sb.Len() > maxResponseSize {
			return "", gomailer.NewTransportError("SMTP response exceeded the maximum allowed size")
		}
		if err != nil {
			// A read error is terminal for this connection regardless of how
			// much was buffered: a non-terminated tail is a truncated response,
			// not a complete one.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return "", gomailer.NewTransportError("Connection to the SMTP server timed out: " + err.Error())
			}
			return "", gomailer.NewTransportError("Connection to the SMTP server has been closed unexpectedly: " + err.Error())
		}
		// A complete line ends in CRLF/LF; the response is finished only on a
		// terminal line (4th byte is a space, not a '-' continuation marker).
		if len(line) < 4 || line[3] != '-' {
			break
		}
	}
	return sb.String(), nil
}

// assertResponse asserts a 220 greeting (used by start).
func (t *Transport) assertResponse(resp string) (string, error) {
	return t.assertResponse2(resp, []int{220})
}

// assertResponse2 validates the leading 3-digit code against the allowed set,
// returning a *gomailer.TransportError carrying the code on mismatch.
func (t *Transport) assertResponse2(resp string, codes []int) (string, error) {
	if len(codes) == 0 {
		return "", fmt.Errorf("%w: you must set the expected response code", gomailer.ErrLogic)
	}
	c := parseCode(resp)
	valid := false
	for _, want := range codes {
		if c == want {
			valid = true
			break
		}
	}
	if !valid || resp == "" {
		want := make([]string, len(codes))
		for i, x := range codes {
			want[i] = strconv.Itoa(x)
		}
		var codeStr string
		if c != 0 {
			codeStr = fmt.Sprintf("code \"%d\"", c)
		} else {
			codeStr = "empty code"
		}
		var msgStr string
		if trimmed := strings.TrimSpace(resp); trimmed != "" {
			msgStr = fmt.Sprintf(", with message %q", trimmed)
		}
		te := gomailer.NewTransportError(fmt.Sprintf("Expected response code %q but got %s%s.", strings.Join(want, "/"), codeStr, msgStr))
		te.Code = c
		return resp, te
	}
	return resp, nil
}

// redactCommand masks credential-bearing AUTH command arguments before they
// reach the debug transcript. "AUTH PLAIN <base64>" keeps the mechanism but
// hides the secret; bare base64 continuation lines (LOGIN user/pass, CRAM-MD5
// response) are masked entirely.
func redactCommand(command string) string {
	trimmed := strings.TrimRight(command, "\r\n")
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "AUTH ") {
		// keep "AUTH <MECH>", mask any credential that follows it
		fields := strings.SplitN(trimmed, " ", 3)
		if len(fields) == 3 {
			return fields[0] + " " + fields[1] + " " + redactedToken + "\r\n"
		}
		return command
	}
	// bare continuation line carrying a credential
	return redactedToken + "\r\n"
}

// redactedToken is the placeholder written in place of AUTH credentials.
const redactedToken = "<redacted>"

// recordDebug appends a timestamped debug transcript line.
func (t *Transport) recordDebug(dir, line string) {
	ts := time.Now().Format("2006-01-02T15:04:05.000000-07:00")
	for _, l := range strings.Split(strings.TrimRight(line, "\r\n"), "\n") {
		fmt.Fprintf(&t.debug, "[%s] %s %s\n", ts, dir, l)
	}
}

// drainDebug returns and clears the accumulated debug transcript.
func (t *Transport) drainDebug() string {
	s := t.debug.String()
	t.debug.Reset()
	return s
}

// appendErrDebug attaches the debug transcript to a *gomailer.TransportError.
func (t *Transport) appendErrDebug(err error) {
	var te *gomailer.TransportError
	if errors.As(err, &te) {
		te.AppendDebug(t.drainDebug())
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// parseCode extracts the leading 3-digit SMTP code from a response, 0 if none.
func parseCode(resp string) int {
	if len(resp) < 3 {
		return 0
	}
	c, err := strconv.Atoi(resp[:3])
	if err != nil {
		return 0
	}
	return c
}

// code extracts the SMTP code from a *gomailer.TransportError (0 otherwise).
func code(err error) int {
	var te *gomailer.TransportError
	if errors.As(err, &te) {
		return te.Code
	}
	return 0
}

// parseCapabilities parses an EHLO response into a map of keyword -> args.
func parseCapabilities(resp string) map[string][]string {
	caps := map[string][]string{}
	lines := strings.Split(strings.TrimSpace(resp), "\r\n")
	if len(lines) <= 1 {
		// EHLO greeting line is dropped; nothing else to parse
		return caps
	}
	for _, line := range lines[1:] {
		// strip leading "NNN " or "NNN-"
		if len(line) < 4 {
			continue
		}
		rest := strings.TrimRight(line[4:], "\r\n")
		rest = strings.TrimLeft(rest, " =")
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			caps[strings.ToUpper(strings.TrimRight(line[4:], "\r\n "))] = nil
			continue
		}
		key := strings.ToUpper(fields[0])
		var args []string
		for _, f := range fields[1:] {
			args = append(args, strings.ToUpper(f))
		}
		caps[key] = args
	}
	return caps
}

// parseMessageID extracts the message id from a 250 "...Ok: queued as ID"
// response so the returned SentMessage can report the server-assigned id.
func parseMessageID(resp string) string {
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "250 ") {
			continue
		}
		// pattern: 250 (\S+ )?Ok:? (queued as |id=)?(ID)
		rest := strings.TrimPrefix(line, "250 ")
		// optional leading token followed by a space
		// find "Ok" case-insensitively
		idx := indexFold(rest, "ok")
		if idx < 0 {
			continue
		}
		after := rest[idx+2:]
		after = strings.TrimPrefix(after, ":")
		after = strings.TrimLeft(after, " ")
		after = trimPrefixFold(after, "queued as ")
		after = trimPrefixFold(after, "id=")
		id := takeIDToken(after)
		if id != "" {
			return id
		}
	}
	return ""
}

// takeIDToken consumes the leading [A-Za-z0-9._-]+ token, the run of
// characters that make up a queued message id.
func takeIDToken(s string) string {
	end := 0
	for end < len(s) {
		ch := s[end]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-' {
			end++
			continue
		}
		break
	}
	return s[:end]
}

// indexFold is a case-insensitive strings.Index.
func indexFold(s, substr string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(substr))
}

// trimPrefixFold strips prefix from s case-insensitively if present.
func trimPrefixFold(s, prefix string) string {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):]
	}
	return s
}

// normalizeCRLF converts lone LF and CR to CRLF line endings.
func normalizeCRLF(data []byte) []byte {
	out := make([]byte, 0, len(data)+len(data)/16)
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '\r':
			out = append(out, '\r', '\n')
			if i+1 < len(data) && data[i+1] == '\n' {
				i++
			}
		case '\n':
			out = append(out, '\r', '\n')
		default:
			out = append(out, data[i])
		}
	}
	return out
}

// dotStuff replaces every line-leading dot with two dots. The beginning of the
// DATA stream is also a line boundary, which matters for RawMessage payloads.
func dotStuff(data []byte) []byte {
	out := make([]byte, 0, len(data)+8)
	if len(data) > 0 && data[0] == '.' {
		out = append(out, '.')
	}
	for i := 0; i < len(data); i++ {
		out = append(out, data[i])
		if data[i] == '\n' && i >= 1 && data[i-1] == '\r' && i+1 < len(data) && data[i+1] == '.' {
			out = append(out, '.')
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Authenticators
// ---------------------------------------------------------------------------

// PlainAuthenticator implements AUTH PLAIN (RFC 4954).
type PlainAuthenticator struct{}

// Keyword returns "PLAIN".
func (PlainAuthenticator) Keyword() string { return "PLAIN" }

// Authenticate performs the single-step PLAIN exchange.
func (PlainAuthenticator) Authenticate(t *Transport) error {
	cred := base64.StdEncoding.EncodeToString([]byte(t.username + "\x00" + t.username + "\x00" + t.password))
	_, err := t.executeCommandRedacted(fmt.Sprintf("AUTH PLAIN %s\r\n", cred), []int{235})
	return err
}

// LoginAuthenticator implements AUTH LOGIN (RFC 4954).
type LoginAuthenticator struct{}

// Keyword returns "LOGIN".
func (LoginAuthenticator) Keyword() string { return "LOGIN" }

// Authenticate performs the three-step LOGIN exchange.
func (LoginAuthenticator) Authenticate(t *Transport) error {
	if _, err := t.executeCommand("AUTH LOGIN\r\n", []int{334}); err != nil {
		return err
	}
	user := base64.StdEncoding.EncodeToString([]byte(t.username))
	if _, err := t.executeCommandRedacted(user+"\r\n", []int{334}); err != nil {
		return err
	}
	pass := base64.StdEncoding.EncodeToString([]byte(t.password))
	_, err := t.executeCommandRedacted(pass+"\r\n", []int{235})
	return err
}

// CramMD5Authenticator implements AUTH CRAM-MD5 (RFC 4954/2195).
type CramMD5Authenticator struct{}

// Keyword returns "CRAM-MD5".
func (CramMD5Authenticator) Keyword() string { return "CRAM-MD5" }

// Authenticate performs the CRAM-MD5 challenge/response exchange.
func (CramMD5Authenticator) Authenticate(t *Transport) error {
	if t.password == "" {
		return fmt.Errorf("%w: a non-empty secret is required", gomailer.ErrInvalidArgument)
	}
	resp, err := t.executeCommand("AUTH CRAM-MD5\r\n", []int{334})
	if err != nil {
		return err
	}
	// challenge is base64 after the leading "334 "
	chalEnc := strings.TrimSpace(resp)
	if len(chalEnc) >= 4 {
		chalEnc = strings.TrimSpace(chalEnc[4:])
	}
	challenge, derr := base64.StdEncoding.DecodeString(chalEnc)
	if derr != nil {
		return gomailer.NewTransportError("invalid CRAM-MD5 challenge: " + derr.Error())
	}
	mac := hmac.New(md5.New, []byte(t.password))
	mac.Write(challenge)
	digest := fmt.Sprintf("%x", mac.Sum(nil))
	msg := base64.StdEncoding.EncodeToString([]byte(t.username + " " + digest))
	_, err = t.executeCommandRedacted(msg+"\r\n", []int{235})
	return err
}
