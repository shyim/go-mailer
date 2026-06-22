package smtp_test

import (
	"errors"
	"testing"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport"
	"github.com/shyim/go-mailer/transport/smtp"

	// Blank-import the concrete transport factory packages so the default
	// registry can resolve their schemes. This test lives in the smtp module
	// (rather than the root module) so the root module need not depend back on
	// the transport submodules, which would be a circular module dependency.
	_ "github.com/shyim/go-mailer/transport/sendmail"
	_ "github.com/shyim/go-mailer/transport/smtp"
)

// TestFromDSNResolvesSchemes asserts the default registry resolves an "smtp://"
// DSN into an SMTP transport and "null://" into the null transport, and rejects
// an unknown scheme. It exercises the init-time factory self-registration.
func TestFromDSNResolvesSchemes(t *testing.T) {
	smtpTransport, err := transport.FromDSN("smtp://user:pass@host:25", transport.Deps{})
	if err != nil {
		t.Fatalf("FromDSN(smtp) returned error: %v", err)
	}
	if _, ok := smtpTransport.(*smtp.Transport); !ok {
		t.Fatalf("FromDSN(smtp) = %T, want *smtp.Transport", smtpTransport)
	}

	nullTransport, err := transport.FromDSN("null://", transport.Deps{})
	if err != nil {
		t.Fatalf("FromDSN(null) returned error: %v", err)
	}
	if _, ok := nullTransport.(*transport.NullTransport); !ok {
		t.Fatalf("FromDSN(null) = %T, want *transport.NullTransport", nullTransport)
	}

	if _, err := transport.FromDSN("bogus://host", transport.Deps{}); !errors.Is(err, gomailer.ErrUnsupportedScheme) {
		t.Fatalf("FromDSN(bogus) error = %v, want ErrUnsupportedScheme", err)
	}
}
