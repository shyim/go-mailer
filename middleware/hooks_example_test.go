package middleware_test

import (
	"context"
	"fmt"
	"strings"

	gomailer "github.com/shyim/go-mailer"
	"github.com/shyim/go-mailer/middleware"
)

// hookTransport is a stand-in leaf transport for the hooks example. Real code
// uses an smtp/sendmail/null transport (or a leaf under RoundRobin/Failover).
type hookTransport struct{}

func (hookTransport) String() string { return "null://" }

func (hookTransport) Send(_ context.Context, _ gomailer.RawMessage, env *gomailer.Envelope) (*gomailer.SentMessage, error) {
	fmt.Println("delivering to:", env.Recipients()[0].Email())
	sm := &gomailer.SentMessage{}
	sm.SetMessageID("<demo@host>")
	return sm, nil
}

// Example_hooks shows the two cross-cutting hooks: BeforeSend can mutate the
// outgoing message/envelope or reject the send (report success without
// delivering), and AfterSend observes the (*SentMessage, error) result.
// Middleware are composed with Wrap; the first argument is the outermost layer.
func Example_hooks() {
	leaf := hookTransport{}

	// AfterSend (outermost) observes the final outcome. BeforeSend (innermost,
	// closest to the leaf) mutates the message, then either rejects or proceeds.
	t := middleware.Wrap(leaf,
		middleware.AfterSend(func(_ context.Context, sm *gomailer.SentMessage, err error) {
			switch {
			case err != nil:
				fmt.Println("observed failure:", err)
			case sm == nil:
				fmt.Println("observed rejected send")
			default:
				fmt.Println("observed sent:", sm.MessageID())
			}
		}),
		middleware.BeforeSend(func(_ context.Context, msg *gomailer.Message, env *gomailer.Envelope) error {
			// Reject (silently skip + report success) anything addressed to the
			// blocked domain.
			if strings.HasSuffix(env.Recipients()[0].Email(), "@blocked.example") {
				return middleware.ErrReject
			}
			// Mutate the outgoing message before delivery.
			if msg != nil {
				msg.SetSubject("[tagged] Hello")
			}
			return nil
		}),
	)

	send := func(to string) {
		msg := gomailer.NewMessage().
			SetFrom(gomailer.MustAddress("from@example.com", "")).
			SetTo(gomailer.MustAddress(to, "")).
			SetSubject("Hello").
			SetText([]byte("body"))

		mailer := gomailer.NewMailer(t)
		if err := mailer.Send(context.Background(), msg, nil); err != nil {
			fmt.Println("send error:", err)
		}
	}

	send("ok@example.com")
	send("spam@blocked.example")

	// Output:
	// delivering to: ok@example.com
	// observed sent: <demo@host>
	// observed rejected send
}
