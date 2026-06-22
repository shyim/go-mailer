package transport

import (
	"errors"
	"io"

	gomailer "github.com/shyim/go-mailer"
)

// closeAll closes every transport that implements io.Closer, joining any errors.
// Transports that are not closers (Null, in-memory) are skipped. It is the basis
// for the composite/router Close methods so a production wiring like
// failover(smtp://a smtp://b) behind a Mailer can QUIT and release pooled SMTP
// connections on shutdown instead of leaking file descriptors until the server's
// idle timeout.
func closeAll(transports []gomailer.Transport) error {
	var errs []error
	for _, tr := range transports {
		if c, ok := tr.(io.Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
