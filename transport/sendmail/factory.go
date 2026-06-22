package sendmail

import (
	"fmt"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport"
)

func init() {
	transport.RegisterDefaultFactory(func() transport.Factory { return Factory{} })
}

// Factory builds a SendmailTransport from a DSN. It supports the "sendmail"
// scheme and honors the optional "command" option. An interactive -bs
// SMTP-over-process mode is intentionally not implemented; it is therefore
// not advertised as supported.
type Factory struct{}

// Create returns a SendmailTransport for a supported DSN, otherwise an error
// wrapping ErrUnsupportedScheme.
func (Factory) Create(d *transport.DSN, _ transport.Deps) (gomailer.Transport, error) {
	if !(Factory{}).Supports(d) {
		return nil, fmt.Errorf("%w: scheme %q is not supported (supported: %q)", gomailer.ErrUnsupportedScheme, d.Scheme(), "sendmail")
	}
	return NewSendmailTransport(d.Option("command", ""))
}

// Supports reports whether the DSN scheme is "sendmail".
func (Factory) Supports(d *transport.DSN) bool {
	switch d.Scheme() {
	case "sendmail":
		return true
	default:
		return false
	}
}

// SupportedSchemes lists the schemes this factory handles, letting the registry
// include them in the unsupported-scheme error message.
func (Factory) SupportedSchemes() []string {
	return []string{"sendmail"}
}
