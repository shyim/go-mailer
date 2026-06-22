package transport

import (
	"context"
	"errors"
	"strings"
	"testing"

	gomailer "github.com/shyim/go-mailer"
)

// stubTransport is a minimal Transport used to assert resolver wiring without
// touching the network. Its name doubles as an identity in composite String().
type stubTransport struct{ name string }

func (s *stubTransport) Send(context.Context, gomailer.RawMessage, *gomailer.Envelope) (*gomailer.SentMessage, error) {
	return nil, nil
}
func (s *stubTransport) String() string { return s.name }

// stubFactory builds a stubTransport for any DSN whose scheme it recognizes,
// naming the transport "<scheme>://<host>" so composites are easy to assert on.
type stubFactory struct{ schemes []string }

func (f *stubFactory) Supports(d *DSN) bool {
	for _, s := range f.schemes {
		if d.Scheme() == s {
			return true
		}
	}
	return false
}
func (f *stubFactory) SupportedSchemes() []string { return f.schemes }
func (f *stubFactory) Create(d *DSN, _ Deps) (gomailer.Transport, error) {
	return &stubTransport{name: d.Scheme() + "://" + d.Host()}, nil
}

func newTestRegistry() *Registry {
	return NewRegistry(&stubFactory{schemes: []string{"smtp", "smtps"}}, NullFactory{})
}

func TestRegistryFromStringSingle(t *testing.T) {
	r := newTestRegistry()
	tr, err := r.FromString("smtp://a.example.com", Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if tr.String() != "smtp://a.example.com" {
		t.Errorf("got %q", tr.String())
	}
}

func TestRegistryUnsupportedScheme(t *testing.T) {
	r := newTestRegistry()
	_, err := r.FromString("carrierpigeon://nest", Deps{})
	if !errors.Is(err, gomailer.ErrUnsupportedScheme) {
		t.Fatalf("expected ErrUnsupportedScheme, got %v", err)
	}
	// The message should enumerate the schemes the registry knows.
	if !strings.Contains(err.Error(), "smtp") {
		t.Errorf("unsupported-scheme error should list supported schemes: %v", err)
	}
}

func TestRegistryGarbageSuffix(t *testing.T) {
	r := newTestRegistry()
	_, err := r.FromString("smtp://host extra-junk", Deps{})
	if !errors.Is(err, gomailer.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for trailing garbage, got %v", err)
	}
}

func TestRegistryFailoverComposite(t *testing.T) {
	r := newTestRegistry()
	tr, err := r.FromString("failover(smtp://a smtp://b)", Deps{})
	if err != nil {
		t.Fatal(err)
	}
	fo, ok := tr.(*FailoverTransport)
	if !ok {
		t.Fatalf("expected *FailoverTransport, got %T", tr)
	}
	want := "failover(smtp://a smtp://b)"
	if fo.String() != want {
		t.Errorf("String() = %q, want %q", fo.String(), want)
	}
}

func TestRegistryRoundRobinWithRetryPeriod(t *testing.T) {
	r := newTestRegistry()
	tr, err := r.FromString("roundrobin(smtp://a smtp://b)?retry_period=15", Deps{})
	if err != nil {
		t.Fatal(err)
	}
	rr, ok := tr.(*RoundRobinTransport)
	if !ok {
		t.Fatalf("expected *RoundRobinTransport, got %T", tr)
	}
	if rr.retryPeriod.Seconds() != 15 {
		t.Errorf("retryPeriod = %v, want 15s", rr.retryPeriod)
	}
}

func TestRegistryRejectsMalformedRetryPeriod(t *testing.T) {
	r := newTestRegistry()
	for _, dsn := range []string{
		"roundrobin(smtp://a smtp://b)?retry_period=15abc",
		"roundrobin(smtp://a smtp://b)?retry_period=abc",
		"roundrobin(smtp://a smtp://b)?retry_period=",
	} {
		t.Run(dsn, func(t *testing.T) {
			if _, err := r.FromString(dsn, Deps{}); !errors.Is(err, gomailer.ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestRegistryNestedComposite(t *testing.T) {
	r := newTestRegistry()
	tr, err := r.FromString("failover(roundrobin(smtp://a smtp://b) smtps://c)", Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.(*FailoverTransport); !ok {
		t.Fatalf("expected *FailoverTransport at top, got %T", tr)
	}
	want := "failover(roundrobin(smtp://a smtp://b) smtps://c)"
	if tr.String() != want {
		t.Errorf("String() = %q, want %q", tr.String(), want)
	}
}

func TestRegistryUnknownKeyword(t *testing.T) {
	r := newTestRegistry()
	_, err := r.FromString("loadbalance(smtp://a smtp://b)", Deps{})
	if !errors.Is(err, gomailer.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for unknown keyword, got %v", err)
	}
}

func TestRegistryFromStringsRouter(t *testing.T) {
	r := newTestRegistry()
	router, err := r.FromStrings(map[string]string{
		"main":   "smtp://primary",
		"backup": "smtps://secondary",
	}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	// Names are resolved in sorted order, so "backup" is the default.
	if router.defaultName != "backup" {
		t.Errorf("defaultName = %q, want backup (sorted-first)", router.defaultName)
	}
	if len(router.transports) != 2 {
		t.Errorf("router has %d transports, want 2", len(router.transports))
	}
}
