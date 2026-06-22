package transport

import (
	"errors"
	"strings"
	"testing"

	gomailer "github.com/shyim/go-mailer"
)

func TestParseDSN(t *testing.T) {
	tests := []struct {
		name     string
		dsn      string
		scheme   string
		host     string
		user     string
		password string
		port     int
		wantErr  bool
	}{
		{name: "full smtp", dsn: "smtp://user:pass@mail.example.com:2525", scheme: "smtp", host: "mail.example.com", user: "user", password: "pass", port: 2525},
		{name: "smtps no port", dsn: "smtps://relay.example.com", scheme: "smtps", host: "relay.example.com", port: 0},
		{name: "null shorthand", dsn: "null://", scheme: "null", host: "null"},
		{name: "null canonical", dsn: "null://null", scheme: "null", host: "null"},
		{name: "urlencoded creds", dsn: "smtp://us%40r:p%40ss@host", scheme: "smtp", host: "host", user: "us@r", password: "p@ss"},
		{name: "no scheme", dsn: "//host", wantErr: true},
		{name: "empty host with authority", dsn: "smtp://", wantErr: true},
		{name: "scheme only no authority", dsn: "smtp:foo", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseDSN(tc.dsn)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got none", tc.dsn)
				}
				if !errors.Is(err, gomailer.ErrInvalidArgument) {
					t.Fatalf("error %v should wrap ErrInvalidArgument", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.Scheme() != tc.scheme {
				t.Errorf("scheme = %q, want %q", d.Scheme(), tc.scheme)
			}
			if d.Host() != tc.host {
				t.Errorf("host = %q, want %q", d.Host(), tc.host)
			}
			if d.User() != tc.user {
				t.Errorf("user = %q, want %q", d.User(), tc.user)
			}
			if d.Password() != tc.password {
				t.Errorf("password = %q, want %q", d.Password(), tc.password)
			}
			if got := d.Port(0); got != tc.port {
				t.Errorf("port = %d, want %d", got, tc.port)
			}
		})
	}
}

func TestDSNOptions(t *testing.T) {
	d, err := ParseDSN("smtp://host?verify_peer=0&auto_tls=true&local_domain=mail.example.com&retry=2&retry=5")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Option("local_domain", ""); got != "mail.example.com" {
		t.Errorf("local_domain = %q", got)
	}
	if got := d.Option("missing", "fallback"); got != "fallback" {
		t.Errorf("default not returned: %q", got)
	}
	// Last value wins for repeated keys (parse_str semantics).
	if got := d.Option("retry", ""); got != "5" {
		t.Errorf("retry = %q, want last value 5", got)
	}
	if d.BoolOption("verify_peer", true) {
		t.Errorf("verify_peer=0 should be false")
	}
	if !d.BoolOption("auto_tls", false) {
		t.Errorf("auto_tls=true should be true")
	}
	if !d.BoolOption("absent", true) {
		t.Errorf("absent bool option should fall back to default true")
	}
}

func TestPortDefault(t *testing.T) {
	d, _ := ParseDSN("smtp://host")
	if got := d.Port(25); got != 25 {
		t.Errorf("Port default = %d, want 25", got)
	}
	d2, _ := ParseDSN("smtp://host:587")
	if got := d2.Port(25); got != 587 {
		t.Errorf("explicit Port = %d, want 587", got)
	}
}

func TestParseDSNRejectsOutOfRangePort(t *testing.T) {
	for _, dsn := range []string{"smtp://host:65536", "smtp://host:99999"} {
		if _, err := ParseDSN(dsn); !errors.Is(err, gomailer.ErrInvalidArgument) {
			t.Fatalf("ParseDSN(%q) error = %v, want ErrInvalidArgument", dsn, err)
		}
	}
	if d, err := ParseDSN("smtp://host:65535"); err != nil || d.Port(0) != 65535 {
		t.Fatalf("65535 should be accepted, d=%v err=%v", d, err)
	}
}

// A malformed DSN must not echo the raw string (which carries the password)
// into the error message. (#5)
func TestParseDSNDoesNotLeakCredentials(t *testing.T) {
	// A control byte in the userinfo makes url.Parse fail.
	_, err := ParseDSN("smtp://user:sup3rsecret\x7f@host:25")
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), "sup3rsecret") {
		t.Errorf("DSN parse error leaked the password: %v", err)
	}
	if !errors.Is(err, gomailer.ErrInvalidArgument) {
		t.Errorf("error should classify as ErrInvalidArgument: %v", err)
	}
}
