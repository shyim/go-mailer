package ses

import (
	"context"
	"fmt"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport"
)

func init() {
	transport.RegisterDefaultFactory(func() transport.Factory { return Factory{} })
}

// Factory builds an SES Transport from a DSN with the "ses" scheme.
//
// DSN form:
//
//	ses://ACCESS_KEY:SECRET_KEY@default?region=us-east-1
//
// The host is ignored (use "default"). Credentials in the DSN are optional: when
// omitted, the SDK's default credential chain is used. Options:
//
//	region             the AWS region (also resolvable via the default chain)
//	configuration_set  an SES configuration set name
type Factory struct{}

// Create builds an SES Transport for a "ses" DSN, otherwise an error wrapping
// gomailer.ErrUnsupportedScheme. AWS configuration is loaded eagerly, so a
// configuration error (e.g. no region resolvable) is returned here.
func (Factory) Create(d *transport.DSN, _ transport.Deps) (gomailer.Transport, error) {
	if !(Factory{}).Supports(d) {
		return nil, fmt.Errorf("%w: scheme %q is not supported (supported: %q)", gomailer.ErrUnsupportedScheme, d.Scheme(), "ses")
	}

	return New(context.Background(), factoryOptions(d)...)
}

// factoryOptions translates a "ses" DSN into the transport options. It is
// separated from Create so the DSN-to-option mapping can be tested without
// loading AWS configuration.
func factoryOptions(d *transport.DSN) []Option {
	var opts []Option
	if region := d.Option("region", ""); region != "" {
		opts = append(opts, WithRegion(region))
	}
	if cs := d.Option("configuration_set", ""); cs != "" {
		opts = append(opts, WithConfigurationSet(cs))
	}
	if user := d.User(); user != "" {
		// A username in the DSN means static credentials; the password is the
		// secret key. A session token is not expressible in a DSN.
		opts = append(opts, WithCredentials(user, d.Password(), ""))
	}
	return opts
}

// Supports reports whether the DSN scheme is "ses".
func (Factory) Supports(d *transport.DSN) bool {
	return d.Scheme() == "ses"
}

// SupportedSchemes lists the schemes this factory handles, letting the registry
// include them in the unsupported-scheme error message.
func (Factory) SupportedSchemes() []string {
	return []string{"ses"}
}
