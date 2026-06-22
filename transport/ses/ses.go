// Package ses implements an Amazon SES (Simple Email Service) transport for
// gomailer, using the SES v2 SendEmail API with raw MIME content. The full
// RFC 5322 message produced by gomailer's MIME builder is handed to SES
// verbatim, so attachments, HTML parts, and custom headers are preserved.
//
// The AWS SDK dependency is confined to this module: importing the gomailer
// core does not pull in any AWS code. Import this package to register the
// "ses" DSN scheme with the default registry.
package ses

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	gomailer "github.com/shyim/go-mailer"
)

// sesAPI is the slice of the SES v2 client the transport depends on. Narrowing
// it to one method keeps the transport mockable in tests without reaching AWS.
type sesAPI interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// Transport delivers messages through Amazon SES. It embeds
// gomailer.BaseTransport and supplies a DoSend hook that submits the raw MIME
// message via the SES v2 SendEmail API. Construct it with New or NewWithClient.
type Transport struct {
	gomailer.BaseTransport

	client           sesAPI
	region           string
	configurationSet string
}

// New builds an SES transport, loading AWS configuration via the SDK's default
// chain (environment, shared config/credentials, IAM role, ...) and applying
// the given options. It returns an error if AWS configuration cannot be loaded.
//
//	t, err := ses.New(ctx, ses.WithRegion("us-east-1"))
func New(ctx context.Context, opts ...Option) (*Transport, error) {
	cfg := newConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	awsCfg, err := cfg.loadAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: unable to load AWS config: %w", gomailer.ErrTransport, err)
	}

	client := sesv2.NewFromConfig(awsCfg)
	return newTransport(client, cfg.region, cfg.configurationSet), nil
}

// NewWithClient builds an SES transport over an already-configured SES v2 client
// (or any value implementing the SendEmail method). This is the seam used by
// tests and by callers who construct the AWS client themselves.
func NewWithClient(client sesAPI, opts ...Option) *Transport {
	cfg := newConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return newTransport(client, cfg.region, cfg.configurationSet)
}

func newTransport(client sesAPI, region, configurationSet string) *Transport {
	t := &Transport{
		client:           client,
		region:           region,
		configurationSet: configurationSet,
	}
	t.Name = sesName(region)
	t.DoSend = t.doSend
	return t
}

// sesName is the transport identity ("ses://" or "ses://<region>").
func sesName(region string) string {
	if region == "" {
		return "ses://"
	}
	return "ses://" + region
}

// doSend submits the raw MIME message to SES. The envelope sender and recipients
// drive the SMTP-level FROM/destination; the raw message carries its own
// To/Cc/Bcc headers, so recipients are passed as the SES destination.
func (t *Transport) doSend(ctx context.Context, sm *gomailer.SentMessage) error {
	env := sm.Envelope()

	to := make([]string, 0, len(env.Recipients()))
	for _, r := range env.Recipients() {
		encoded, err := r.EncodedEmail()
		if err != nil {
			return fmt.Errorf("%w: invalid recipient %q: %w", gomailer.ErrTransport, r.Email(), err)
		}
		to = append(to, encoded)
	}

	from, err := env.Sender().EncodedEmail()
	if err != nil {
		return fmt.Errorf("%w: invalid sender %q: %w", gomailer.ErrTransport, env.Sender().Email(), err)
	}

	in := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(from),
		Destination:      &types.Destination{ToAddresses: to},
		Content: &types.EmailContent{
			Raw: &types.RawMessage{Data: sm.Bytes()},
		},
	}
	if t.configurationSet != "" {
		in.ConfigurationSetName = aws.String(t.configurationSet)
	}

	out, err := t.client.SendEmail(ctx, in)
	if err != nil {
		te := gomailer.NewTransportError("SES SendEmail failed")
		te.Cause = err
		return te
	}
	if out != nil && out.MessageId != nil {
		sm.SetMessageID(*out.MessageId)
	}
	return nil
}
