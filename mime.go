package gomailer

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/textproto"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// maxBase64LineLength is the RFC 2045 limit for an encoded line (76 chars).
const maxBase64LineLength = 76

// encodeHeaderWord encodes a header value using RFC 2047 "Q" or "B" encoding
// only when it contains bytes that are not safe to emit verbatim in a header.
// Pure-ASCII printable values are returned unchanged.
func encodeHeaderWord(s string) string {
	if isPrintableASCII(s) && !hasOverlongHeaderToken(s) {
		return s
	}
	if isPrintableASCII(s) {
		return encodeASCIIEncodedWords(s)
	}
	// mime.QEncoding.Encode chooses Q-encoding and folds long values into
	// multiple encoded-words as required by RFC 2047.
	return mime.QEncoding.Encode("utf-8", s)
}

func hasOverlongHeaderToken(s string) bool {
	for _, token := range strings.Fields(s) {
		if len(token) > maxHeaderTokenLength {
			return true
		}
	}
	return len(s) > maxHeaderTokenLength && len(strings.Fields(s)) == 0
}

const maxHeaderTokenLength = 900

func encodeASCIIEncodedWords(s string) string {
	const (
		encodedWordPrefix = "=?utf-8?b?"
		encodedWordSuffix = "?="
		maxEncodedWordLen = 75
	)
	encodedTextLen := maxEncodedWordLen - len(encodedWordPrefix) - len(encodedWordSuffix)
	encodedTextLen -= encodedTextLen % 4
	rawChunkLen := encodedTextLen / 4 * 3
	if rawChunkLen <= 0 {
		rawChunkLen = 45
	}

	words := make([]string, 0, len(s)/rawChunkLen+1)
	for len(s) > 0 {
		n := rawChunkLen
		if n > len(s) {
			n = len(s)
		}
		words = append(words, encodedWordPrefix+base64.StdEncoding.EncodeToString([]byte(s[:n]))+encodedWordSuffix)
		s = s[n:]
	}
	return strings.Join(words, " ")
}

// isPrintableASCII reports whether s consists solely of printable ASCII
// characters (no control bytes other than allowed folding whitespace, no
// high-bit bytes). Such values are safe to place directly in a header.
func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x80 || c < 0x20 {
			// Allow horizontal tab and space; reject other control bytes
			// (including CR/LF, which would otherwise enable header injection).
			if c == '\t' {
				continue
			}
			return false
		}
	}
	return true
}

// encodeAddressList renders a list of addresses for a To/From/Cc style header,
// applying RFC 2047 encoding to display names as needed.
func encodeAddressList(addrs []Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		// headerString punycodes the domain so the rendered header is 7-bit
		// clean and matches the (also punycoded) SMTP envelope. A mailbox with
		// no display name is emitted as the bare addr-spec.
		parts = append(parts, a.headerString())
	}
	return strings.Join(parts, ", ")
}

const (
	// maxHeaderLineLength is the RFC 5322 2.1.1 soft limit: lines SHOULD be no
	// more than 78 octets (excluding the terminating CRLF).
	maxHeaderLineLength = 78
	// hardHeaderLineLength is the RFC 5322 2.1.1 hard limit: lines MUST be no
	// more than 998 octets (excluding the terminating CRLF).
	hardHeaderLineLength = 998
)

// foldHeaderLine writes a single header as "Name: value" terminated by CRLF,
// folding the line at existing whitespace boundaries to stay near the RFC 5322
// 78-octet soft limit. It never splits a token: inserting FWS inside an
// encoded-word or MIME parameter changes the unfolded value. If a single token is
// too long for the 998-octet hard limit, an error is returned.
func foldHeaderLine(w io.Writer, name, value string) error {
	if !isValidHeaderName(name) {
		return fmt.Errorf("%w: invalid header name %q", ErrInvalidArgument, name)
	}
	if hasCRLF(value) {
		return fmt.Errorf("%w: invalid header value for %q: CR/LF is not allowed", ErrInvalidArgument, name)
	}
	// "Name: " is the prefix that occupies the first line.
	prefix := name + ": "

	// curLen tracks the octet length of the current physical line including its
	// prefix. Folds happen only at existing whitespace runs; when folding, the
	// original run is represented by the single FWS byte after CRLF.
	curLen := len(prefix)
	if _, err := io.WriteString(w, prefix); err != nil {
		return err
	}

	for i := 0; i < len(value); {
		// Identify the next run of leading whitespace (the fold point) followed
		// by the next word (a run of non-whitespace).
		wsStart := i
		for i < len(value) && (value[i] == ' ' || value[i] == '\t') {
			i++
		}
		ws := value[wsStart:i]
		wordStart := i
		for i < len(value) && value[i] != ' ' && value[i] != '\t' {
			i++
		}
		word := value[wordStart:i]

		if word == "" {
			if curLen+len(ws) > hardHeaderLineLength {
				return fmt.Errorf("%w: header %q line exceeds %d octets", ErrInvalidArgument, name, hardHeaderLineLength)
			}
			if _, err := io.WriteString(w, ws); err != nil {
				return err
			}
			curLen += len(ws)
			continue
		}

		// We have a whitespace boundary: decide whether to fold here. Fold when
		// the existing whitespace plus the word would push the current line past
		// the soft limit, but only after a non-empty value fragment has been
		// emitted on the current line.
		if wsStart > 0 && curLen+len(ws)+len(word) > maxHeaderLineLength && curLen > len(prefix) {
			// Fold: CRLF + single space, then the word (drop the original WS).
			if _, err := io.WriteString(w, "\r\n "); err != nil {
				return err
			}
			curLen = 1
			ws = ""
		}

		if curLen+len(ws)+len(word) > hardHeaderLineLength {
			return fmt.Errorf("%w: header %q line exceeds %d octets", ErrInvalidArgument, name, hardHeaderLineLength)
		}
		if _, err := io.WriteString(w, ws); err != nil {
			return err
		}
		curLen += len(ws)
		if _, err := io.WriteString(w, word); err != nil {
			return err
		}
		curLen += len(word)
	}

	_, err := io.WriteString(w, "\r\n")
	return err
}

// writeRawHeader writes a raw header line, encoding the value with RFC 2047 if
// it is not printable ASCII.
func writeRawHeader(w io.Writer, name, value string) error {
	return foldHeaderLine(w, name, encodeHeaderWord(value))
}

func formatMediaTypeHeaderValue(mediaType string, params map[string]string) string {
	if len(params) == 0 {
		return mediaType
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString(mediaType)
	for _, key := range keys {
		value := params[key]
		if needsRFC2231Continuation(value) {
			for i, chunk := range rfc2231ContinuationChunks(value) {
				if i == 0 {
					chunk = "utf-8''" + chunk
				}
				fmt.Fprintf(&sb, "; %s*%d*=%s", key, i, chunk)
			}
			continue
		}
		single := mime.FormatMediaType(mediaType, map[string]string{key: value})
		if strings.HasPrefix(single, mediaType) {
			sb.WriteString(strings.TrimPrefix(single, mediaType))
			continue
		}
		fmt.Fprintf(&sb, "; %s=%q", key, value)
	}
	return sb.String()
}

func needsRFC2231Continuation(value string) bool {
	return len(value) > 200
}

func rfc2231ContinuationChunks(value string) []string {
	const maxRawChunk = 30
	var chunks []string
	for value != "" {
		n := len(value)
		if n > maxRawChunk {
			n = maxRawChunk
			for n > 0 && !utf8.RuneStart(value[n]) {
				n--
			}
			if n == 0 {
				_, size := utf8.DecodeRuneInString(value)
				n = size
			}
		}
		chunks = append(chunks, percentEncodeRFC2231(value[:n]))
		value = value[n:]
	}
	return chunks
}

func percentEncodeRFC2231(value string) string {
	var sb strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if isRFC2231AttrChar(c) {
			sb.WriteByte(c)
			continue
		}
		fmt.Fprintf(&sb, "%%%02X", c)
	}
	return sb.String()
}

func isRFC2231AttrChar(c byte) bool {
	if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
		return true
	}
	switch c {
	case '!', '#', '$', '&', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

// isValidHeaderName reports whether name is an RFC 5322 field-name token.
// Rejecting CR/LF here prevents header injection via custom headers.
func isValidHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		// RFC 5322 field-name is one or more atext chars. This excludes CTLs,
		// whitespace, colon, angle brackets and other specials.
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '/', '=', '?', '^', '_', '`', '{', '|', '}', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func hasCRLF(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}

// writeQuotedPrintable writes data as a quoted-printable encoded body. It is
// used for text/plain and text/html parts so that 8-bit content survives
// transports that are not 8-bit clean.
func writeQuotedPrintable(w io.Writer, data []byte) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write(data); err != nil {
		return err
	}
	return qp.Close()
}

// writeBase64 writes data as base64 split into 76-character lines terminated
// by CRLF, as required for binary MIME parts (attachments and embedded files).
func writeBase64(w io.Writer, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 0 {
		n := maxBase64LineLength
		if n > len(encoded) {
			n = len(encoded)
		}
		if _, err := io.WriteString(w, encoded[:n]); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return err
		}
		encoded = encoded[n:]
	}
	return nil
}

// generateID returns a globally-unique token suitable for a Message-ID or
// Content-ID local-part: a high-entropy random hex string with a time prefix.
func generateID() string {
	var b [16]byte
	// rand.Read never returns an error for crypto/rand on supported platforms,
	// but we fall back to a time-derived value to stay deterministic on error.
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

// generateMessageID returns a Message-ID value (without angle brackets) anchored
// to the domain of the given sender address, mirroring how mailers derive the
// right-hand side of the identifier from the From/Sender domain.
func generateMessageID(domain string) string {
	if domain == "" {
		domain = "localhost.localdomain"
	}
	return generateID() + "@" + domain
}

// generateBoundary returns a MIME multipart boundary string that is extremely
// unlikely to collide with any encoded content.
func generateBoundary() string {
	return "_=_gomailer_" + generateID() + "_=_"
}

// formatDate renders a time in the RFC 5322 / RFC 2822 date format expected in
// a Date header (e.g. "Mon, 02 Jan 2006 15:04:05 -0700").
func formatDate(t time.Time) string {
	return t.Format(time.RFC1123Z)
}

// canonicalHeaderName normalizes a header name to its textproto canonical form
// (e.g. "content-type" => "Content-Type") for case-insensitive comparison and
// stable output.
func canonicalHeaderName(name string) string {
	return textproto.CanonicalMIMEHeaderKey(name)
}
