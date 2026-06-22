// Package transport parses mailer DSNs and resolves them into concrete
// gomailer.Transport implementations via a registry of factories. It supports
// the failover(...) and roundrobin(...) composite keywords as well as the
// named-transport map used by the router.
package transport

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	gomailer "github.com/shyim/go-mailer"
)

// DSN is a parsed mailer DSN: scheme, host, optional user/password/port and a
// bag of query options.
type DSN struct {
	scheme   string
	host     string
	user     string
	password string
	hasUser  bool
	hasPass  bool
	port     int
	hasPort  bool
	options  map[string]string
}

// ParseDSN parses a single DSN string (no failover/roundrobin wrappers) into a
// DSN. The DSN must contain a scheme and a host; otherwise it returns an error
// wrapping gomailer.ErrInvalidArgument.
func ParseDSN(s string) (*DSN, error) {
	u, err := url.Parse(s)
	if err != nil {
		// Do not wrap or echo the url.Parse error: it embeds the raw DSN, which
		// contains the cleartext user:password and routinely lands in logs and
		// error responses. Return a generic, credential-free message instead.
		return nil, fmt.Errorf("%w: the mailer DSN is invalid", gomailer.ErrInvalidArgument)
	}

	if u.Scheme == "" {
		return nil, fmt.Errorf("%w: the mailer DSN must contain a scheme", gomailer.ErrInvalidArgument)
	}

	host := u.Hostname()
	if host == "" && u.Scheme == "null" {
		// The null transport carries no addressing, so its DSN needs no host.
		// The canonical form is "null://null", but the String() form of a
		// NullTransport is the host-less "null://"; accept that shorthand here so
		// it round-trips back into a NullTransport.
		host = "null"
	}
	if host == "" {
		// An authority-present-but-empty-host input such as "some://" is invalid,
		// but Go's url.Parse accepts it with an empty host. Reject that shape with
		// the generic message, and keep the more specific "must contain a host"
		// hint for the "scheme:path" shape (no "//" authority).
		if strings.Contains(s, "://") {
			return nil, fmt.Errorf("%w: the mailer DSN is invalid", gomailer.ErrInvalidArgument)
		}
		return nil, fmt.Errorf("%w: the mailer DSN must contain a host (use \"default\" by default)", gomailer.ErrInvalidArgument)
	}

	d := &DSN{
		scheme:  u.Scheme,
		host:    host,
		options: map[string]string{},
	}

	if u.User != nil {
		if name := u.User.Username(); name != "" {
			d.user = name
			d.hasUser = true
		}
		if pass, ok := u.User.Password(); ok && pass != "" {
			d.password = pass
			d.hasPass = true
		}
	}

	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("%w: the mailer DSN has an invalid port %q", gomailer.ErrInvalidArgument, p)
		}
		if port < 0 || port > 65535 {
			return nil, fmt.Errorf("%w: the mailer DSN has an invalid port %q", gomailer.ErrInvalidArgument, p)
		}
		d.port = port
		d.hasPort = true
	}

	// Last value wins for repeated scalar keys.
	for key, vals := range u.Query() {
		if len(vals) > 0 {
			d.options[key] = vals[len(vals)-1]
		} else {
			d.options[key] = ""
		}
	}

	return d, nil
}

// Scheme returns the DSN scheme, e.g. "smtp" or "sendmail".
func (d *DSN) Scheme() string { return d.scheme }

// Host returns the DSN host.
func (d *DSN) Host() string { return d.host }

// User returns the decoded user, or "" if unset.
func (d *DSN) User() string { return d.user }

// Password returns the decoded password, or "" if unset.
func (d *DSN) Password() string { return d.password }

// Port returns the DSN port, or def if no port was specified.
func (d *DSN) Port(def int) int {
	if d.hasPort {
		return d.port
	}
	return def
}

// Option returns the string value of a query option, or def if it is unset.
func (d *DSN) Option(key, def string) string {
	if v, ok := d.options[key]; ok {
		return v
	}
	return def
}

// BoolOption returns the boolean value of a query option: "1", "true", "on"
// and "yes" are true; everything else is false. If the option is unset, def is
// returned.
func (d *DSN) BoolOption(key string, def bool) bool {
	v, ok := d.options[key]
	if !ok {
		return def
	}
	return parseBool(v)
}

// parseBool reports whether v is a truthy option value ("1", "true", "on" or
// "yes"); all other values, including the empty string, are false.
func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}
