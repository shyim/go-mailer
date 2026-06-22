package transport

import (
	"fmt"
	"log"

	gomailer "github.com/shyim/go-mailer"
)

// Deps carries the optional collaborators a Factory may inject into the
// transports it builds. All fields are optional and may be nil.
type Deps struct {
	// Logger receives transport-level diagnostics; nil disables logging.
	Logger *log.Logger
}

// Factory creates a gomailer.Transport from a parsed DSN: Supports reports
// whether the DSN scheme is handled and Create builds the transport (or
// returns an error).
type Factory interface {
	// Create builds a Transport from d using the given deps. It returns an
	// error wrapping gomailer.ErrUnsupportedScheme if it does not handle the
	// scheme, or gomailer.ErrIncompleteDSN if required credentials are missing.
	Create(d *DSN, deps Deps) (gomailer.Transport, error)
	// Supports reports whether this factory handles the DSN's scheme.
	Supports(d *DSN) bool
}

// unsupportedSchemeError builds the error returned when no factory handles a
// scheme. It wraps gomailer.ErrUnsupportedScheme so callers can classify the
// failure via errors.Is.
func unsupportedSchemeError(d *DSN, supported []string) error {
	msg := fmt.Sprintf("the %q scheme is not supported", d.Scheme())
	if len(supported) > 0 {
		msg += fmt.Sprintf("; supported schemes are: %q", supported)
	}
	return fmt.Errorf("%w: %s", gomailer.ErrUnsupportedScheme, msg)
}
