package smtp

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport"
)

// init registers the SMTP factory with the default registry so that
// blank-importing this package makes the "smtp" and "smtps" schemes resolvable
// through transport.DefaultRegistry / transport.FromDSN, following the
// database/sql driver registration pattern.
func init() {
	transport.RegisterDefaultFactory(func() transport.Factory { return NewEsmtpFactory() })
}

// EsmtpFactory builds an SMTP Transport from a DSN, supporting the "smtp" and
// "smtps" schemes.
type EsmtpFactory struct{}

// NewEsmtpFactory returns an EsmtpFactory.
func NewEsmtpFactory() *EsmtpFactory { return &EsmtpFactory{} }

// Supports reports whether the DSN scheme is "smtp" or "smtps".
func (f *EsmtpFactory) Supports(d *transport.DSN) bool {
	switch d.Scheme() {
	case "smtp", "smtps":
		return true
	default:
		return false
	}
}

// SupportedSchemes lists the schemes this factory handles, letting the registry
// include them in the unsupported-scheme error message.
func (f *EsmtpFactory) SupportedSchemes() []string {
	return []string{"smtp", "smtps"}
}

// Create builds the configured Transport from the DSN, honoring auto_tls,
// require_tls, verify_peer, source_ip, local_domain, max_per_second,
// restart_threshold(_sleep), ping_threshold and the user/password credentials.
func (f *EsmtpFactory) Create(d *transport.DSN, _ transport.Deps) (gomailer.Transport, error) {
	if !f.Supports(d) {
		return nil, fmt.Errorf("%w: %q (supported by smtp: %q)", gomailer.ErrUnsupportedScheme, d.Scheme(), "smtp, smtps")
	}

	// auto_tls: empty value means "leave enabled"; otherwise parse as bool.
	autoTLS := d.Option("auto_tls", "") == "" || d.BoolOption("auto_tls", true)

	host := d.Host()
	port := d.Port(0)

	var tlsOnConnect bool
	switch d.Scheme() {
	case "smtps":
		tlsOnConnect = true
	default:
		// With auto_tls enabled, decide TLS on a per-connection basis: an
		// explicit port 465, or a remote host with no explicit port, selects
		// implicit TLS. localhost keeps plain port 25 for local relays.
		tlsOnConnect = autoTLS && (port == 465 || (port == 0 && isRemoteHost(host)))
	}

	t := NewTransport(host, port, tlsOnConnect)
	t.SetAutoTLS(autoTLS)
	t.SetRequireTLS(d.BoolOption("require_tls", false))

	if ip := d.Option("source_ip", ""); ip != "" {
		t.SetSourceIP(ip)
	}

	// Build a TLS config when any TLS-affecting option is set. We leave
	// ServerName empty so effectiveTLSConfig injects the host at use time.
	var tlsCfg *tls.Config
	getTLS := func() *tls.Config {
		if tlsCfg == nil {
			tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		return tlsCfg
	}

	// verify_peer: an explicit falsey (non-empty) value disables verification
	// of both the certificate chain and the hostname.
	if v := d.Option("verify_peer", ""); v != "" && !d.BoolOption("verify_peer", true) {
		getTLS().InsecureSkipVerify = true
	}

	// peer_fingerprint: pin the leaf certificate's SHA-256 fingerprint so the
	// connection only succeeds against a known certificate.
	if fp := d.Option("peer_fingerprint", ""); fp != "" {
		want, err := normalizeFingerprint(fp)
		if err != nil {
			return nil, err
		}
		getTLS().VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			for _, raw := range rawCerts {
				sum := sha256.Sum256(raw)
				if hex.EncodeToString(sum[:]) == want {
					return nil
				}
			}
			return fmt.Errorf("%w: peer certificate fingerprint does not match %q", gomailer.ErrTransport, fp)
		}
	}

	if tlsCfg != nil {
		t.SetTLSConfig(tlsCfg)
	}

	if u := d.User(); u != "" {
		t.SetUsername(u)
	}
	if p := d.Password(); p != "" {
		t.SetPassword(p)
	}

	if ld := d.Option("local_domain", ""); ld != "" {
		t.SetLocalDomain(ld)
	}

	if mps := d.Option("max_per_second", ""); mps != "" {
		rate, err := strconv.ParseFloat(mps, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid max_per_second %q: %w", gomailer.ErrInvalidArgument, mps, err)
		}
		t.SetMaxPerSecond(rate)
	}

	if rt := d.Option("restart_threshold", ""); rt != "" {
		threshold, err := strconv.Atoi(rt)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid restart_threshold %q: %w", gomailer.ErrInvalidArgument, rt, err)
		}
		sleep := 0
		if s := d.Option("restart_threshold_sleep", ""); s != "" {
			sleep, err = strconv.Atoi(s)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid restart_threshold_sleep %q: %w", gomailer.ErrInvalidArgument, s, err)
			}
		}
		t.SetRestartThreshold(threshold, sleep)
	}

	if pt := d.Option("ping_threshold", ""); pt != "" {
		seconds, err := strconv.Atoi(pt)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid ping_threshold %q: %w", gomailer.ErrInvalidArgument, pt, err)
		}
		t.SetPingThreshold(seconds)
	}

	return t, nil
}

func isRemoteHost(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return false
	default:
		return true
	}
}

// normalizeFingerprint lowercases a peer_fingerprint and strips common
// separators (colons, spaces), returning an error if it is not a valid
// SHA-256 hex digest (64 hex characters).
func normalizeFingerprint(fp string) (string, error) {
	cleaned := strings.ToLower(fp)
	cleaned = strings.ReplaceAll(cleaned, ":", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")
	if _, err := hex.DecodeString(cleaned); err != nil || len(cleaned) != sha256.Size*2 {
		return "", fmt.Errorf("%w: invalid peer_fingerprint %q (expected a SHA-256 hex digest)", gomailer.ErrInvalidArgument, fp)
	}
	return cleaned, nil
}
