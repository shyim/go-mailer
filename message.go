package gomailer

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/idna"
)

// Attachment is a single MIME attachment or inline (related) part.
type Attachment struct {
	Filename    string // filename for the Content-Disposition
	ContentType string // MIME type; sniffed from data/filename if empty
	Data        []byte // raw bytes (base64-encoded on write)
	Inline      bool   // true => Content-Disposition: inline + a Content-ID
	ContentID   string // referenced by HTML via cid:; auto-generated if Inline and empty
}

// Header is a single raw RFC 5322 header line (name + already-decoded value).
type Header struct {
	Name  string
	Value string
}

// Message is a high-level email that knows how to serialize itself to MIME.
// It implements RawMessage by way of WriteTo/Bytes. Builder setters return the
// receiver for chaining. A Message is mutated in place; transports take a copy
// of the serialized bytes, so concurrent sends of the same *Message are safe
// only after it has been fully built.
type Message struct {
	from        []Address
	replyTo     []Address
	to          []Address
	cc          []Address
	bcc         []Address
	subject     string
	date        time.Time
	textBody    []byte
	htmlBody    []byte
	attachments []Attachment
	headers     []Header // extra/custom headers, e.g. X-Transport

	// mu guards the lazily materialized Message-ID and Date so that MessageID()
	// and repeated Bytes()/WriteTo() calls are race-free and byte-identical: the
	// values are computed once on first access and reused thereafter.
	mu          sync.Mutex
	messageID   string // contents of the Message-ID header (without <>); set explicitly or materialized
	sealedDate  time.Time
	sealedMsgID string // materialized Message-ID used for serialization
	sealed      bool   // whether sealedDate/sealedMsgID have been computed
}

// NewMessage returns an empty Message.
func NewMessage() *Message {
	return &Message{}
}

// SetFrom sets the From addresses, replacing any previous value.
func (m *Message) SetFrom(addrs ...Address) *Message {
	m.from = append(m.from[:0:0], addrs...)
	return m
}

// SetTo sets the To addresses, replacing any previous value.
func (m *Message) SetTo(addrs ...Address) *Message {
	m.to = append(m.to[:0:0], addrs...)
	return m
}

// SetCc sets the Cc addresses, replacing any previous value.
func (m *Message) SetCc(addrs ...Address) *Message {
	m.cc = append(m.cc[:0:0], addrs...)
	return m
}

// SetBcc sets the Bcc addresses, replacing any previous value.
func (m *Message) SetBcc(addrs ...Address) *Message {
	m.bcc = append(m.bcc[:0:0], addrs...)
	return m
}

// SetReplyTo sets the Reply-To addresses, replacing any previous value.
func (m *Message) SetReplyTo(addrs ...Address) *Message {
	m.replyTo = append(m.replyTo[:0:0], addrs...)
	return m
}

// SetSubject sets the Subject header.
func (m *Message) SetSubject(s string) *Message {
	m.subject = s
	return m
}

// SetDate sets the Date header value.
func (m *Message) SetDate(t time.Time) *Message {
	m.date = t
	return m
}

// SetText sets the text/plain body.
func (m *Message) SetText(body []byte) *Message {
	m.textBody = body
	return m
}

// SetHTML sets the text/html body.
func (m *Message) SetHTML(body []byte) *Message {
	m.htmlBody = body
	return m
}

// Attach adds an attachment or inline part to the message.
func (m *Message) Attach(a Attachment) *Message {
	if a.Inline && a.ContentID == "" {
		a.ContentID = generateID() + "@gomailer"
	}
	m.attachments = append(m.attachments, a)
	return m
}

// Clone returns a deep copy of the message. Routers (RoundRobin/Failover/
// Transports) clone the message before handing it to each underlying transport
// so that header mutations made by one transport (e.g. an added X-Transport-N
// header on a failing transport) do not leak into the retry transport or back
// into the caller's message.
func (m *Message) Clone() *Message {
	// Read the sealed/messageID state under the lock; do NOT struct-copy m (it
	// holds a sync.Mutex that must not be copied). The clone gets a fresh,
	// zero-value mutex.
	m.mu.Lock()
	c := &Message{
		from:        slices.Clone(m.from),
		replyTo:     slices.Clone(m.replyTo),
		to:          slices.Clone(m.to),
		cc:          slices.Clone(m.cc),
		bcc:         slices.Clone(m.bcc),
		subject:     m.subject,
		date:        m.date,
		textBody:    slices.Clone(m.textBody),
		htmlBody:    slices.Clone(m.htmlBody),
		headers:     slices.Clone(m.headers),
		messageID:   m.messageID,
		sealedMsgID: m.sealedMsgID,
		sealedDate:  m.sealedDate,
		sealed:      m.sealed,
	}
	m.mu.Unlock()
	if m.attachments != nil {
		c.attachments = make([]Attachment, len(m.attachments))
		for i, a := range m.attachments {
			a.Data = append(a.Data[:0:0], a.Data...)
			c.attachments[i] = a
		}
	}
	return c
}

// SetHeader adds or replaces a custom header (case-insensitive name match).
func (m *Message) SetHeader(name, value string) *Message {
	// SetHeader is chainable and cannot return an error, so invalid field names
	// or values are ignored here and also rejected defensively during
	// serialization. This prevents CRLF header injection such as
	// `SetHeader("X\r\nBcc", "...")`.
	if !isValidHeaderName(name) || hasCRLF(value) {
		return m
	}
	canon := canonicalHeaderName(name)
	for i := range m.headers {
		if canonicalHeaderName(m.headers[i].Name) == canon {
			m.headers[i].Value = value
			return m
		}
	}
	m.headers = append(m.headers, Header{Name: name, Value: value})
	return m
}

// Header returns the value of a custom header and whether it was set.
func (m *Message) Header(name string) (string, bool) {
	canon := canonicalHeaderName(name)
	for i := range m.headers {
		if canonicalHeaderName(m.headers[i].Name) == canon {
			return m.headers[i].Value, true
		}
	}
	return "", false
}

// RemoveHeader deletes a custom header; used by the Transports router for
// the X-Transport routing header.
func (m *Message) RemoveHeader(name string) {
	canon := canonicalHeaderName(name)
	out := m.headers[:0]
	for _, h := range m.headers {
		if canonicalHeaderName(h.Name) == canon {
			continue
		}
		out = append(out, h)
	}
	m.headers = out
}

// From returns a copy of the From addresses (used to derive the envelope
// sender). The slice is copied so a caller mutating it cannot corrupt the
// message, matching Recipients()/Envelope.Recipients().
func (m *Message) From() []Address {
	return append([]Address(nil), m.from...)
}

// Recipients returns the union of To, Cc and Bcc (used to derive the envelope).
func (m *Message) Recipients() []Address {
	out := make([]Address, 0, len(m.to)+len(m.cc)+len(m.bcc))
	out = append(out, m.to...)
	out = append(out, m.cc...)
	out = append(out, m.bcc...)
	return out
}

// seal materializes the Message-ID and Date exactly once and returns them. An
// explicitly set Message-ID or Date is honored; otherwise a value is generated
// on first call and frozen, so MessageID() and every Bytes()/WriteTo() observe
// the same values. Safe for concurrent use.
func (m *Message) seal() (msgID string, date time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.sealed {
		if m.messageID != "" {
			m.sealedMsgID = m.messageID
		} else {
			m.sealedMsgID = generateMessageID(m.senderDomain())
		}
		if !m.date.IsZero() {
			m.sealedDate = m.date
		} else {
			m.sealedDate = time.Now()
		}
		m.sealed = true
	}
	return m.sealedMsgID, m.sealedDate
}

// MessageID returns the Message-ID header value (without angle brackets),
// materializing one on first call if none was set explicitly. The value is
// stable across calls and matches the serialized message.
func (m *Message) MessageID() string {
	id, _ := m.seal()
	return id
}

// senderDomain returns the domain used to anchor a generated Message-ID,
// derived from the first From (or Sender) address. An internationalized domain
// is punycoded so the generated Message-ID stays 7-bit clean and matches the
// punycoded From header.
func (m *Message) senderDomain() string {
	var email string
	if sender, ok := senderFromMessageHeaders(m); ok {
		email = sender.Email()
	} else if len(m.from) > 0 {
		email = m.from[0].Email()
	}
	i := strings.LastIndexByte(email, '@')
	if i < 0 {
		return ""
	}
	domain := email[i+1:]
	if ascii, err := idna.Lookup.ToASCII(domain); err == nil {
		return ascii
	}
	return domain
}

// EnsureValidity reports an error if the message lacks a From or any recipient,
// or carries a malformed address.
func (m *Message) EnsureValidity() error {
	if len(m.to) == 0 && len(m.cc) == 0 && len(m.bcc) == 0 {
		return fmt.Errorf("%w: an email must have a To, Cc, or Bcc header", ErrLogic)
	}
	if len(m.from) == 0 {
		return fmt.Errorf("%w: an email must have a From header", ErrLogic)
	}
	if err := ensureValidAddressList("From", m.from); err != nil {
		return err
	}
	if err := ensureValidAddressList("Reply-To", m.replyTo); err != nil {
		return err
	}
	if err := ensureValidAddressList("To", m.to); err != nil {
		return err
	}
	if err := ensureValidAddressList("Cc", m.cc); err != nil {
		return err
	}
	if err := ensureValidAddressList("Bcc", m.bcc); err != nil {
		return err
	}
	return nil
}

func ensureValidAddressList(header string, addrs []Address) error {
	for _, addr := range addrs {
		if !addr.valid() {
			return fmt.Errorf("%w: invalid %s address %q", ErrInvalidArgument, header, addr.Email())
		}
	}
	return nil
}

// WriteTo serializes the complete RFC 5322 message (headers + MIME body) to w.
// It implements RawMessage.
func (m *Message) WriteTo(w io.Writer) (int64, error) {
	buf, err := m.Bytes()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(buf)
	return int64(n), err
}

// Bytes returns the serialized message as a byte slice. Implements RawMessage.
func (m *Message) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if err := m.writeHeaders(&buf); err != nil {
		return nil, err
	}
	if err := m.writeBody(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeHeaders emits the top-level RFC 5322 headers, excluding the MIME
// structural headers (Content-Type / Content-Transfer-Encoding / MIME-Version),
// which are emitted by writeBody so they precede the matching body section.
func (m *Message) writeHeaders(w io.Writer) error {
	// Materialize Message-ID and Date once so repeated serializations are
	// byte-identical and concurrent serializations do not race.
	mid, date := m.seal()
	if err := writeRawHeader(w, "Date", formatDate(date)); err != nil {
		return err
	}
	if err := foldHeaderLine(w, "Message-ID", "<"+mid+">"); err != nil {
		return err
	}
	if m.subject != "" {
		if err := writeRawHeader(w, "Subject", m.subject); err != nil {
			return err
		}
	}
	if len(m.from) > 0 {
		if err := foldHeaderLine(w, "From", encodeAddressList(m.from)); err != nil {
			return err
		}
	}
	if len(m.replyTo) > 0 {
		if err := foldHeaderLine(w, "Reply-To", encodeAddressList(m.replyTo)); err != nil {
			return err
		}
	}
	if len(m.to) > 0 {
		if err := foldHeaderLine(w, "To", encodeAddressList(m.to)); err != nil {
			return err
		}
	}
	if len(m.cc) > 0 {
		if err := foldHeaderLine(w, "Cc", encodeAddressList(m.cc)); err != nil {
			return err
		}
	}
	// Bcc is intentionally not serialized into the wire headers; recipients
	// come from the envelope, keeping Bcc recipients hidden per RFC 5322.
	for _, h := range m.headers {
		if isStructuralHeader(h.Name) {
			// Never let a custom header collide with the MIME structure.
			continue
		}
		if err := writeRawHeader(w, h.Name, h.Value); err != nil {
			return err
		}
	}
	return nil
}

// isStructuralHeader reports whether a header name controls the MIME body
// structure and must therefore be generated rather than copied from custom
// headers.
func isStructuralHeader(name string) bool {
	switch canonicalHeaderName(name) {
	case "Content-Type", "Content-Transfer-Encoding", "Mime-Version",
		"Content-Disposition", "Content-Id", "Date", "Message-Id",
		"Subject", "From", "To", "Cc", "Bcc", "Reply-To":
		return true
	default:
		return false
	}
}

// writeBody emits MIME-Version plus the body section, choosing the simplest
// MIME structure that can represent the configured bodies and attachments.
func (m *Message) writeBody(w io.Writer) error {
	if _, err := io.WriteString(w, "MIME-Version: 1.0\r\n"); err != nil {
		return err
	}

	hasText := len(m.textBody) > 0
	hasHTML := len(m.htmlBody) > 0
	hasAttach := len(m.attachments) > 0

	// No content at all: emit an empty 7bit text/plain body so the message is
	// still a valid MIME document.
	if !hasText && !hasHTML && !hasAttach {
		if _, err := io.WriteString(w, "Content-Type: text/plain; charset=utf-8\r\n"); err != nil {
			return err
		}
		_, err := io.WriteString(w, "Content-Transfer-Encoding: 7bit\r\n\r\n")
		return err
	}

	related, inline := m.partitionAttachments()

	// The "body" is the text/html alternative (or a single text part). If
	// there are inline parts it is wrapped in multipart/related; if there are
	// regular attachments the whole thing is wrapped in multipart/mixed.
	if hasAttach || (len(related) > 0 && (hasText || hasHTML)) {
		return m.writeMixed(w, related, inline)
	}
	// No attachments at all -> body is emitted directly at the top level.
	return m.writeContentBody(w)
}

// partitionAttachments splits attachments into inline (related) parts and
// regular (mixed) attachments.
func (m *Message) partitionAttachments() (inlineParts, regularParts []Attachment) {
	for _, a := range m.attachments {
		if a.Inline {
			inlineParts = append(inlineParts, a)
		} else {
			regularParts = append(regularParts, a)
		}
	}
	return inlineParts, regularParts
}

// writeMixed emits a multipart/mixed (or multipart/related when there are only
// inline parts) container holding the body section plus the attachments.
func (m *Message) writeMixed(w io.Writer, inlineParts, regularParts []Attachment) error {
	// When there are regular attachments we need multipart/mixed at the top.
	// When there are only inline parts (and a body), multipart/related is the
	// top-level container instead.
	if len(regularParts) == 0 && len(inlineParts) > 0 {
		return m.writeRelated(w, inlineParts)
	}

	boundary := generateBoundary()
	if err := writeMultipartHeader(w, "mixed", boundary); err != nil {
		return err
	}

	// First part: the body (possibly itself multipart/related or
	// multipart/alternative).
	if err := openBoundary(w, boundary); err != nil {
		return err
	}
	if len(inlineParts) > 0 {
		if err := m.writeRelated(w, inlineParts); err != nil {
			return err
		}
	} else {
		if err := m.writeContentBody(w); err != nil {
			return err
		}
	}

	// Remaining parts: the regular attachments.
	for i := range regularParts {
		if err := openBoundary(w, boundary); err != nil {
			return err
		}
		if err := writeAttachmentPart(w, regularParts[i]); err != nil {
			return err
		}
	}

	return closeBoundary(w, boundary)
}

// writeRelated emits a multipart/related container with the body part first
// followed by the inline (cid-referenced) parts.
func (m *Message) writeRelated(w io.Writer, inlineParts []Attachment) error {
	boundary := generateBoundary()
	if err := writeMultipartHeader(w, "related", boundary); err != nil {
		return err
	}

	if err := openBoundary(w, boundary); err != nil {
		return err
	}
	if err := m.writeContentBody(w); err != nil {
		return err
	}

	for i := range inlineParts {
		if err := openBoundary(w, boundary); err != nil {
			return err
		}
		if err := writeAttachmentPart(w, inlineParts[i]); err != nil {
			return err
		}
	}

	return closeBoundary(w, boundary)
}

// writeContentBody emits the message "body": either a single text/plain or
// text/html part, or a multipart/alternative wrapping both. The headers are
// written inline so this can be used as the first part of an enclosing
// multipart container as well as the entire body of a simple message.
func (m *Message) writeContentBody(w io.Writer) error {
	hasText := len(m.textBody) > 0
	hasHTML := len(m.htmlBody) > 0

	switch {
	case hasText && hasHTML:
		return m.writeAlternative(w)
	case hasHTML:
		return writeTextPart(w, "text/html", m.htmlBody)
	case hasText:
		return writeTextPart(w, "text/plain", m.textBody)
	default:
		// A body section was requested without any text/html content (only
		// inline parts). Emit an empty text/plain placeholder.
		return writeTextPart(w, "text/plain", nil)
	}
}

// writeAlternative emits a multipart/alternative container with the text/plain
// part first and the text/html part second (least to most faithful), as
// mandated by RFC 2046 for alternative parts.
func (m *Message) writeAlternative(w io.Writer) error {
	boundary := generateBoundary()
	if err := writeMultipartHeader(w, "alternative", boundary); err != nil {
		return err
	}
	if err := openBoundary(w, boundary); err != nil {
		return err
	}
	if err := writeTextPart(w, "text/plain", m.textBody); err != nil {
		return err
	}
	if err := openBoundary(w, boundary); err != nil {
		return err
	}
	if err := writeTextPart(w, "text/html", m.htmlBody); err != nil {
		return err
	}
	return closeBoundary(w, boundary)
}

// writeMultipartHeader writes the Content-Type for a multipart container of the
// given subtype plus the blank line that separates headers from the body.
func writeMultipartHeader(w io.Writer, subtype, boundary string) error {
	line := fmt.Sprintf("Content-Type: multipart/%s; boundary=%q\r\n\r\n", subtype, boundary)
	_, err := io.WriteString(w, line)
	return err
}

// openBoundary writes the delimiter that precedes a part within a multipart
// container.
func openBoundary(w io.Writer, boundary string) error {
	_, err := io.WriteString(w, "--"+boundary+"\r\n")
	return err
}

// closeBoundary writes the closing delimiter that terminates a multipart
// container.
func closeBoundary(w io.Writer, boundary string) error {
	_, err := io.WriteString(w, "--"+boundary+"--\r\n")
	return err
}

// writeTextPart emits a text/* part with a UTF-8 charset and quoted-printable
// transfer encoding, including its part headers and body.
func writeTextPart(w io.Writer, contentType string, body []byte) error {
	header := fmt.Sprintf("Content-Type: %s; charset=utf-8\r\n", contentType) +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n"
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	if err := writeQuotedPrintable(w, body); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}

// writeAttachmentPart emits an attachment or inline part with a base64 transfer
// encoding, a Content-Type derived from the attachment (sniffed when empty), a
// Content-Disposition and, for inline parts, a Content-ID.
func writeAttachmentPart(w io.Writer, a Attachment) error {
	ctype := a.ContentType
	if ctype == "" {
		ctype = sniffContentType(a.Filename, a.Data)
	}
	if hasCRLF(ctype) {
		return fmt.Errorf("%w: invalid attachment content type %q", ErrInvalidArgument, ctype)
	}
	mediaType, mediaParams, parseErr := mime.ParseMediaType(ctype)
	if parseErr != nil {
		return fmt.Errorf("%w: invalid attachment content type %q: %w", ErrInvalidArgument, ctype, parseErr)
	}
	if mediaParams == nil {
		mediaParams = map[string]string{}
	}

	var sb strings.Builder
	if a.Filename != "" {
		params := cloneStringMap(mediaParams)
		params["name"] = a.Filename
		if err := foldHeaderLine(&sb, "Content-Type", formatMediaTypeHeaderValue(mediaType, params)); err != nil {
			return err
		}
	} else {
		if err := foldHeaderLine(&sb, "Content-Type", formatMediaTypeHeaderValue(mediaType, mediaParams)); err != nil {
			return err
		}
	}
	sb.WriteString("Content-Transfer-Encoding: base64\r\n")

	disposition := "attachment"
	if a.Inline {
		disposition = "inline"
		cid := a.ContentID
		if cid == "" {
			cid = generateID() + "@gomailer"
		}
		cid, cidErr := normalizeContentID(cid)
		if cidErr != nil {
			return cidErr
		}
		sb.WriteString("Content-ID: <" + cid + ">\r\n")
	}
	if a.Filename != "" {
		if err := foldHeaderLine(&sb, "Content-Disposition", formatMediaTypeHeaderValue(disposition, map[string]string{"filename": a.Filename})); err != nil {
			return err
		}
	} else {
		sb.WriteString("Content-Disposition: " + disposition + "\r\n")
	}
	sb.WriteString("\r\n")

	if _, err := io.WriteString(w, sb.String()); err != nil {
		return err
	}
	// writeBase64 terminates its final line with CRLF, which provides the blank
	// separation before the next boundary delimiter; no extra CRLF is added here.
	return writeBase64(w, a.Data)
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func normalizeContentID(cid string) (string, error) {
	cid = strings.TrimSpace(cid)
	cid = strings.TrimPrefix(strings.TrimSuffix(cid, ">"), "<")
	if cid == "" || hasCRLF(cid) || strings.ContainsAny(cid, "<> \t") || len("Content-ID: <"+cid+">") > 998 {
		return "", fmt.Errorf("%w: invalid Content-ID %q", ErrInvalidArgument, cid)
	}
	return cid, nil
}

// sniffContentType determines a MIME type for an attachment, preferring the
// extension of the filename and falling back to content sniffing, defaulting
// to application/octet-stream.
func sniffContentType(filename string, data []byte) string {
	if ext := filepath.Ext(filename); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			return ct
		}
	}
	if len(data) > 0 {
		if ct := http.DetectContentType(data); ct != "" {
			return ct
		}
	}
	return "application/octet-stream"
}

// ---------------------------------------------------------------------------
// RawMessage
// ---------------------------------------------------------------------------

// RawMessage is the minimal contract a transport needs to send something:
// the ability to produce its own wire bytes. *Message satisfies it, as does
// the raw-bytes wrapper returned by NewRawMessage.
type RawMessage interface {
	// WriteTo writes the full RFC 5322 message to w.
	WriteTo(w io.Writer) (int64, error)
	// Bytes returns the full RFC 5322 message.
	Bytes() ([]byte, error)
}

// rawBytes is a pre-serialized message. It carries no addressing information,
// so it can only be sent with an explicit Envelope.
type rawBytes struct{ data []byte }

// NewRawMessage wraps already-serialized MIME bytes as a RawMessage. The input
// slice is copied so that later mutations by the caller cannot corrupt the
// wrapped message.
func NewRawMessage(data []byte) RawMessage {
	return &rawBytes{data: append([]byte(nil), data...)}
}

// WriteTo writes the wrapped bytes to w.
func (r *rawBytes) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(r.data)
	return int64(n), err
}

// Bytes returns a copy-safe view of the wrapped bytes; the returned slice is a
// fresh copy, so mutating it cannot corrupt the wrapped message.
func (r *rawBytes) Bytes() ([]byte, error) {
	return append([]byte(nil), r.data...), nil
}
