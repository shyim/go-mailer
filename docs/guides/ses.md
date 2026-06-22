# Amazon SES

The `transport/ses` module delivers mail through **Amazon SES** using the SES v2
`SendEmail` API with **raw MIME** content. The full RFC 5322 message built by
gomailer is handed to SES verbatim, so HTML parts, attachments, and custom
headers are preserved exactly — SES only relays your bytes.

The AWS SDK dependency is confined to this module; importing the gomailer core
pulls in no AWS code.

## Install

```sh
go get github.com/shyim/go-mailer/transport/ses
```

## Construct a transport

`ses.New` loads AWS configuration through the SDK's default chain (environment
variables, shared config/credentials, an IAM role on EC2/ECS/Lambda, …) and
accepts functional options:

```go
import (
	"context"

	"github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/transport/ses"
)

func newMailer(ctx context.Context) (*gomailer.Mailer, error) {
	tr, err := ses.New(ctx,
		ses.WithRegion("us-east-1"),
		ses.WithConfigurationSet("my-config-set"), // optional
	)
	if err != nil {
		return nil, err
	}
	return gomailer.NewMailer(tr), nil
}
```

### Credentials

By default the SDK resolves credentials from its standard chain — ideal on AWS
infrastructure, where no credentials need to be supplied in code. To pass static
credentials explicitly:

```go
tr, err := ses.New(ctx,
	ses.WithRegion("us-east-1"),
	ses.WithCredentials(accessKeyID, secretAccessKey, ""), // sessionToken optional
)
```

### Bring your own client

If you already construct an SES v2 client (custom retry policy, endpoint,
middleware), wrap it directly. Any value with a matching `SendEmail` method
works, which is also how the transport is tested without reaching AWS:

```go
client := sesv2.NewFromConfig(awsCfg)
tr := ses.NewWithClient(client, ses.WithRegion("us-east-1"))
```

## Configure with a DSN

The `ses` scheme resolves through the registry once the module is blank-imported:

```go
import (
	"github.com/shyim/go-mailer/transport"
	_ "github.com/shyim/go-mailer/transport/ses" // registers ses://
)

tr, err := transport.FromDSN("ses://default?region=us-east-1", transport.Deps{})
```

The host is ignored (use `default`). Credentials may be embedded; when omitted,
the default chain is used:

```
ses://ACCESS_KEY:SECRET_KEY@default?region=us-east-1&configuration_set=prod
```

| Option | Meaning |
|--------|---------|
| `region` | AWS region (also resolvable via the default chain) |
| `configuration_set` | SES configuration set name applied to each send |

!!! warning "Credentials in a DSN"
    A DSN containing an access key and secret is sensitive — keep it out of logs
    and source control, and prefer the default credential chain on AWS
    infrastructure. A session token cannot be expressed in a DSN; use
    `WithCredentials` for temporary credentials.

## Behavior notes

- **Envelope vs. headers** — the envelope sender becomes the SES
  `FromEmailAddress`, and the envelope recipients become the SES destination.
  The raw message still carries its own `To`/`Cc`/`Bcc` headers, so what the
  recipient sees matches the message you built.
- **Message ID** — on success the transport records the SES message ID on the
  returned `SentMessage` (`sm.MessageID()`).
- **Errors** — an SES failure is wrapped as a `*gomailer.TransportError`
  (classifiable via `errors.Is(err, gomailer.ErrTransport)`) with the underlying
  AWS error preserved as its `Cause`.

See also [DSN configuration](dsn.md) and [Sending mail](sending-mail.md).
