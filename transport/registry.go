package transport

import (
	"fmt"
	"sort"
	"strings"
	"time"

	gomailer "github.com/shyim/go-mailer"
)

// Registry holds an ordered set of factories and resolves DSN strings into
// transports. It understands the failover(...) and roundrobin(...) composite
// keywords and the named-transport map consumed by the Transports router.
type Registry struct {
	factories []Factory
}

// NewRegistry returns a Registry backed by the given factories, tried in order.
func NewRegistry(factories ...Factory) *Registry {
	return &Registry{factories: factories}
}

// defaultFactories is the ordered list of factory constructors registered by
// the null/sendmail/smtp packages via RegisterDefaultFactory. Using a
// registration hook (the database/sql driver pattern) lets this package compile
// without importing the concrete transport packages, avoiding an import cycle.
var defaultFactories []func() Factory

// RegisterDefaultFactory adds a factory constructor to the set returned by
// DefaultRegistry. The null, sendmail and smtp packages call it from their
// init functions so that importing them wires them into the default resolver.
func RegisterDefaultFactory(newFactory func() Factory) {
	defaultFactories = append(defaultFactories, newFactory)
}

// DefaultRegistry returns a Registry wired with the factories registered by the
// imported transport packages.
//
// The "null" scheme is always available (NullFactory lives in this package). The
// "sendmail" and "smtp"/"smtps" schemes self-register from the
// init functions of their subpackages, which only run when those packages are
// imported. Because this package cannot import them (they import it, which would
// be an import cycle), callers who want the full contract-promised
// "null+sendmail+smtp" set MUST blank-import the subpackages:
//
//	import (
//		_ "github.com/shyim/go-mailer/transport/sendmail"
//		_ "github.com/shyim/go-mailer/transport/smtp"
//	)
//
// Without those imports DefaultRegistry resolves only "null://" and returns
// gomailer.ErrUnsupportedScheme for sendmail/smtp DSNs.
func DefaultRegistry(deps Deps) *Registry {
	_ = deps // deps are passed per-resolution via Create, not held here.
	factories := make([]Factory, 0, len(defaultFactories))
	for _, newFactory := range defaultFactories {
		factories = append(factories, newFactory())
	}
	return NewRegistry(factories...)
}

// FromString resolves a DSN string into a single Transport. It supports the
// failover(...)/roundrobin(...) wrappers and rejects trailing garbage.
func (r *Registry) FromString(dsn string, deps Deps) (gomailer.Transport, error) {
	t, offset, err := r.parseDSN(dsn, 0, deps)
	if err != nil {
		return nil, err
	}
	if offset != len(dsn) {
		return nil, fmt.Errorf("%w: the mailer DSN has some garbage at the end", gomailer.ErrInvalidArgument)
	}
	return t, nil
}

// FromStrings resolves a name->DSN map into a Transports router. Because a Go
// map has no stable iteration order, the names are processed in sorted order so
// the resulting router's default
// transport (the first name) is deterministic across runs. Callers who need the
// default to match a specific intended first transport should build the
// Transports router directly via NewTransports with an explicit order.
func (r *Registry) FromStrings(named map[string]string, deps Deps) (*Transports, error) {
	order := make([]string, 0, len(named))
	for name := range named {
		order = append(order, name)
	}
	sort.Strings(order)

	transports := make(map[string]gomailer.Transport, len(named))
	for _, name := range order {
		t, err := r.FromString(named[name], deps)
		if err != nil {
			return nil, err
		}
		transports[name] = t
	}
	return NewTransports(transports, order)
}

// fromDSNObject tries each factory in order, returning the first that supports
// the DSN, or an unsupported-scheme error listing the schemes this registry
// can handle.
func (r *Registry) fromDSNObject(d *DSN, deps Deps) (gomailer.Transport, error) {
	for _, f := range r.factories {
		if f.Supports(d) {
			return f.Create(d, deps)
		}
	}
	return nil, unsupportedSchemeError(d, r.supportedSchemes())
}

// schemeLister is implemented by factories that can enumerate their schemes
// (NullFactory, sendmail.Factory and smtp.EsmtpFactory all do), used by the
// registry to build the "supported schemes are: ..." unsupported-scheme message.
type schemeLister interface {
	SupportedSchemes() []string
}

// supportedSchemes collects the schemes handled by all registered factories.
func (r *Registry) supportedSchemes() []string {
	var schemes []string
	for _, f := range r.factories {
		if sl, ok := f.(schemeLister); ok {
			schemes = append(schemes, sl.SupportedSchemes()...)
		}
	}
	return schemes
}

// parseDSN is the recursive-descent parser for composite DSNs. It returns the
// resolved transport and the offset immediately past the consumed portion of dsn.
func (r *Registry) parseDSN(dsn string, offset int, deps Deps) (gomailer.Transport, int, error) {
	keywords := []struct {
		name        string
		constructor func([]gomailer.Transport, time.Duration) (gomailer.Transport, error)
	}{
		{"failover", func(ts []gomailer.Transport, rp time.Duration) (gomailer.Transport, error) {
			return NewFailoverTransport(ts, rp)
		}},
		{"roundrobin", func(ts []gomailer.Transport, rp time.Duration) (gomailer.Transport, error) {
			return NewRoundRobinTransport(ts, rp)
		}},
	}

	for _, kw := range keywords {
		prefix := kw.name + "("
		if !strings.HasPrefix(dsn[offset:], prefix) {
			continue
		}
		// Position at the opening parenthesis. Compute the balanced close without
		// mutating offset first, so a malformed (unbalanced) group yields a clean
		// error instead of a corrupted offset fed into url.Parse.
		open := offset + len(prefix) - 1
		end := matchBalancedParen(dsn, open)
		if end < 0 {
			return nil, 0, fmt.Errorf("%w: the mailer DSN %q has unbalanced parentheses in the %q group", gomailer.ErrInvalidArgument, dsn[offset:], kw.name)
		}

		offset = open
		offset++ // step past '('
		var args []gomailer.Transport
		for {
			arg, next, err := r.parseDSN(dsn, offset, deps)
			if err != nil {
				return nil, 0, err
			}
			args = append(args, arg)
			offset = next
			if offset == len(dsn) {
				break
			}
			offset++ // consume the separator (space or closing ')')
			if dsn[offset-1] == ')' {
				break
			}
		}

		period, consumed, err := parseRetryPeriod(dsn, offset)
		if err != nil {
			return nil, 0, err
		}
		t, err := kw.constructor(args, time.Duration(period)*time.Second)
		if err != nil {
			return nil, 0, err
		}
		return t, offset + consumed, nil
	}

	// A keyword-shaped token that is not a known composite keyword is invalid.
	if name, ok := leadingKeyword(dsn, offset); ok {
		return nil, 0, fmt.Errorf("%w: the %q keyword is not valid (valid ones are \"failover\", \"roundrobin\")", gomailer.ErrInvalidArgument, name)
	}

	// Otherwise consume up to the next space or ')' and resolve a single DSN.
	pos := strings.IndexAny(dsn[offset:], " )")
	if pos < 0 {
		d, err := ParseDSN(dsn[offset:])
		if err != nil {
			return nil, 0, err
		}
		t, err := r.fromDSNObject(d, deps)
		if err != nil {
			return nil, 0, err
		}
		return t, len(dsn), nil
	}

	d, err := ParseDSN(dsn[offset : offset+pos])
	if err != nil {
		return nil, 0, err
	}
	t, err := r.fromDSNObject(d, deps)
	if err != nil {
		return nil, 0, err
	}
	return t, offset + pos, nil
}

// matchBalancedParen returns the index of the closing parenthesis that balances
// the '(' at position open, or -1 if dsn[open] is not '(' or is unbalanced.
func matchBalancedParen(dsn string, open int) int {
	if open >= len(dsn) || dsn[open] != '(' {
		return -1
	}
	depth := 0
	for i := open; i < len(dsn); i++ {
		switch dsn[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseRetryPeriod reads an optional "?retry_period=<n>" suffix. On entry,
// offset points at the byte immediately after the composite's closing ')'.
// When that byte is '?' and the query begins with retry_period, it returns the
// parsed period and the number of bytes to advance past offset (the '?' plus
// the consumed "retry_period=<n>" token). Otherwise it returns (0, 0, nil) and
// the caller leaves offset unchanged.
func parseRetryPeriod(dsn string, offset int) (int, int, error) {
	if offset >= len(dsn) || dsn[offset] != '?' {
		return 0, 0, nil
	}
	const key = "retry_period="
	query := dsn[offset+1:] // skip the '?'
	if !strings.HasPrefix(query, key) {
		return 0, 0, nil
	}
	val := query[len(key):]
	if amp := strings.IndexByte(val, '&'); amp >= 0 {
		val = val[:amp]
	}
	if val == "" {
		return 0, 0, fmt.Errorf("%w: retry_period must be an integer", gomailer.ErrInvalidArgument)
	}
	period := 0
	for _, c := range val {
		if c < '0' || c > '9' {
			return 0, 0, fmt.Errorf("%w: retry_period must be an integer", gomailer.ErrInvalidArgument)
		}
		period = period*10 + int(c-'0')
	}
	return period, len(key+val) + 1, nil // +1 for the leading '?'
}

// leadingKeyword reports whether dsn[offset:] begins with an identifier
// immediately followed by '(' (a keyword-call shape), returning that identifier.
func leadingKeyword(dsn string, offset int) (string, bool) {
	i := offset
	for i < len(dsn) {
		c := dsn[i]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	if i > offset && i < len(dsn) && dsn[i] == '(' {
		return dsn[offset:i], true
	}
	return "", false
}

// FromDSN is the package-level convenience that resolves a single DSN string
// against the default registry into a Transport. Only schemes whose factory
// packages have been imported are available; see DefaultRegistry for the
// required blank imports of transport/sendmail and transport/smtp.
func FromDSN(dsn string, deps Deps) (gomailer.Transport, error) {
	return DefaultRegistry(deps).FromString(dsn, deps)
}

// FromDSNs is the package-level convenience that resolves a name->DSN map
// against the default registry into a Transports router.
func FromDSNs(named map[string]string, deps Deps) (*Transports, error) {
	return DefaultRegistry(deps).FromStrings(named, deps)
}
