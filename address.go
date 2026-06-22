package gomailer

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Address represents an RFC 5322 mailbox (an email address plus an optional
// display name). It wraps the stdlib net/mail.Address.
type Address struct {
	addr mail.Address
}

// NewAddress builds an Address from an email and an optional display name,
// returning an error if the email cannot be parsed.
func NewAddress(email, name string) (Address, error) {
	email = strings.TrimSpace(email)
	// Validate the addr-spec by parsing it on its own.
	parsed, err := mail.ParseAddress(email)
	if err != nil {
		return Address{}, fmt.Errorf("%w: invalid email address %q: %w", ErrInvalidArgument, email, err)
	}
	addr := Address{addr: mail.Address{Name: name, Address: parsed.Address}}
	if !addr.valid() {
		return Address{}, fmt.Errorf("%w: invalid email address %q", ErrInvalidArgument, email)
	}
	return addr, nil
}

// MustAddress is like NewAddress but panics on error; intended for constants.
func MustAddress(email, name string) Address {
	a, err := NewAddress(email, name)
	if err != nil {
		panic(err)
	}
	return a
}

// ParseAddress parses a single RFC 5322 address such as "Bob <bob@a.io>".
func ParseAddress(s string) (Address, error) {
	parsed, err := mail.ParseAddress(s)
	if err != nil {
		return Address{}, fmt.Errorf("%w: invalid address %q: %w", ErrInvalidArgument, s, err)
	}
	addr := Address{addr: *parsed}
	if !addr.valid() {
		return Address{}, fmt.Errorf("%w: invalid address %q", ErrInvalidArgument, s)
	}
	return addr, nil
}

// ParseAddressList parses a comma-separated list of RFC 5322 addresses.
func ParseAddressList(s string) ([]Address, error) {
	parsed, err := mail.ParseAddressList(s)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid address list %q: %w", ErrInvalidArgument, s, err)
	}
	out := make([]Address, len(parsed))
	for i, p := range parsed {
		out[i] = Address{addr: *p}
		if !out[i].valid() {
			return nil, fmt.Errorf("%w: invalid address list %q", ErrInvalidArgument, s)
		}
	}
	return out, nil
}

// Email returns the bare addr-spec ("local@domain") without the display name.
func (a Address) Email() string {
	return a.addr.Address
}

// EncodedEmail returns Email with an internationalized domain converted to
// IDNA/punycode for SMTP/sendmail envelope commands. The local part is left
// untouched; SMTPUTF8 capability checks decide whether non-ASCII local-parts are
// allowed.
func (a Address) EncodedEmail() (string, error) {
	return encodeAddrSpecDomain(a.Email())
}

func encodeAddrSpecDomain(address string) (string, error) {
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
		return "", fmt.Errorf("%w: invalid address domain %q: %w", ErrInvalidArgument, domain, err)
	}
	return local + "@" + ascii, nil
}

// Name returns the display name (may be empty).
func (a Address) Name() string {
	return a.addr.Name
}

// String returns the encoded "Name <local@domain>" form (RFC 2047 for the name).
func (a Address) String() string {
	if a.Name() == "" {
		return a.Email()
	}
	return a.addr.String()
}

// headerAddrSpec returns the addr-spec with an internationalized domain
// punycoded so it is 7-bit clean in an RFC 5322 header. The local part is left
// as-is (a non-ASCII local part requires SMTPUTF8 end to end and is the caller's
// concern). If the domain cannot be IDNA-encoded the raw addr-spec is returned
// as a best effort — header rendering has no error channel, and a domain that
// fails here would already have failed envelope encoding.
func (a Address) headerAddrSpec() string {
	if encoded, err := encodeAddrSpecDomain(a.Email()); err == nil {
		return encoded
	}
	return a.Email()
}

// headerString renders the address for a structured RFC 5322 header
// (From/To/Cc/Reply-To): the addr-spec with a punycoded domain, plus the
// RFC 2047 encoded display name when present. It keeps header addressing 7-bit
// clean and consistent with the punycoded envelope.
func (a Address) headerString() string {
	if a.Name() == "" {
		return a.headerAddrSpec()
	}
	named := mail.Address{Name: a.Name(), Address: a.headerAddrSpec()}
	return named.String()
}

func (a Address) valid() bool {
	email := strings.TrimSpace(a.Email())
	if email == "" || email != a.Email() || strings.ContainsAny(email, "\r\n") {
		return false
	}
	at := strings.LastIndexByte(email, '@')
	if at <= 0 || at >= len(email)-1 {
		return false
	}
	return validLocalPart(email[:at])
}

func validLocalPart(local string) bool {
	if local == "" || !utf8.ValidString(local) ||
		strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") ||
		strings.Contains(local, "..") {
		return false
	}
	for i := 0; i < len(local); i++ {
		c := local[i]
		if c >= 0x80 {
			continue
		}
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '/', '=', '?', '^', '_', '`', '{', '|', '}', '~', '.':
			continue
		default:
			return false
		}
	}
	return true
}

// HasUnicodeLocalPart reports whether the local-part contains non-ASCII bytes,
// which decides whether the SMTPUTF8 extension is required for delivery.
func (a Address) HasUnicodeLocalPart() bool {
	local := a.addr.Address
	if i := strings.LastIndexByte(local, '@'); i >= 0 {
		local = local[:i]
	}
	for i := 0; i < len(local); i++ {
		if local[i] >= 0x80 {
			return true
		}
	}
	return false
}
