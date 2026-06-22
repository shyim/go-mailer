package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	gomailer "github.com/shyim/go-mailer"
)

// fakeTransport records how many times it was sent to and can be told to fail
// with a transport-level error to exercise dead-tracking and failover.
type fakeTransport struct {
	name  string
	calls int
	fail  bool
}

func (f *fakeTransport) Send(context.Context, gomailer.RawMessage, *gomailer.Envelope) (*gomailer.SentMessage, error) {
	f.calls++
	if f.fail {
		return nil, gomailer.NewTransportError("boom from " + f.name)
	}
	return &gomailer.SentMessage{}, nil
}
func (f *fakeTransport) String() string { return f.name }

func newMsg(t *testing.T) gomailer.RawMessage {
	t.Helper()
	return gomailer.NewRawMessage([]byte("Subject: hi\r\n\r\nbody"))
}

func TestRoundRobinRotates(t *testing.T) {
	a := &fakeTransport{name: "a"}
	b := &fakeTransport{name: "b"}
	rr, err := NewRoundRobinTransport([]gomailer.Transport{a, b}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic start at index 0.
	rr.SetInitialCursor(func(int) int { return 0 })

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := rr.Send(ctx, newMsg(t), nil); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if a.calls != 2 || b.calls != 2 {
		t.Errorf("rotation uneven: a=%d b=%d, want 2 each", a.calls, b.calls)
	}
}

func TestRoundRobinSkipsDeadTransport(t *testing.T) {
	a := &fakeTransport{name: "a", fail: true}
	b := &fakeTransport{name: "b"}
	rr, _ := NewRoundRobinTransport([]gomailer.Transport{a, b}, time.Minute)
	rr.SetInitialCursor(func(int) int { return 0 })

	// First send: a fails (marked dead), failover to b succeeds.
	if _, err := rr.Send(context.Background(), newMsg(t), nil); err != nil {
		t.Fatalf("expected failover to b, got %v", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("after first send a=%d b=%d, want 1/1", a.calls, b.calls)
	}
	// Second send: a is dead, goes straight to b.
	if _, err := rr.Send(context.Background(), newMsg(t), nil); err != nil {
		t.Fatal(err)
	}
	if a.calls != 1 {
		t.Errorf("dead transport a was retried too early: calls=%d", a.calls)
	}
	if b.calls != 2 {
		t.Errorf("b should have taken both sends: calls=%d", b.calls)
	}
}

func TestRoundRobinAllDead(t *testing.T) {
	a := &fakeTransport{name: "a", fail: true}
	b := &fakeTransport{name: "b", fail: true}
	rr, _ := NewRoundRobinTransport([]gomailer.Transport{a, b}, time.Minute)
	rr.SetInitialCursor(func(int) int { return 0 })

	_, err := rr.Send(context.Background(), newMsg(t), nil)
	if err == nil {
		t.Fatal("expected error when all transports fail")
	}
	if !errors.Is(err, gomailer.ErrTransport) {
		t.Errorf("aggregate error should wrap ErrTransport: %v", err)
	}
	var te *gomailer.TransportError
	if errors.As(err, &te) {
		if te.Debug() == "" {
			t.Error("aggregate error should carry a debug transcript")
		}
	}
}

type nonComparableTransport struct {
	name string
	buf  []byte
}

func (n nonComparableTransport) Send(context.Context, gomailer.RawMessage, *gomailer.Envelope) (*gomailer.SentMessage, error) {
	return nil, gomailer.NewTransportError("boom from " + n.name)
}
func (n nonComparableTransport) String() string { return n.name }

func TestRoundRobinNonComparableTransportDoesNotPanic(t *testing.T) {
	rr, err := NewRoundRobinTransport([]gomailer.Transport{
		nonComparableTransport{name: "a", buf: []byte("not comparable")},
		&fakeTransport{name: "b"},
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rr.SetInitialCursor(func(int) int { return 0 })
	if _, err := rr.Send(context.Background(), newMsg(t), nil); err != nil {
		t.Fatalf("expected failover to comparable second transport, got %v", err)
	}
}

func TestRoundRobinRetryPeriodElapsed(t *testing.T) {
	a := &fakeTransport{name: "a", fail: true}
	b := &fakeTransport{name: "b"}
	rr, _ := NewRoundRobinTransport([]gomailer.Transport{a, b}, 30*time.Second)
	rr.SetInitialCursor(func(int) int { return 0 })

	// Inject a controllable clock.
	now := time.Unix(1000, 0)
	rr.now = func() time.Time { return now }

	// a fails and is marked dead at t=1000.
	rr.Send(context.Background(), newMsg(t), nil)
	if a.calls != 1 {
		t.Fatalf("a.calls=%d", a.calls)
	}

	// Advance past the retry period; a should be retried.
	now = now.Add(31 * time.Second)
	a.fail = false
	rr.Send(context.Background(), newMsg(t), nil)
	if a.calls != 2 {
		t.Errorf("a should be retried after retry period: calls=%d", a.calls)
	}
}

func TestFailoverSticksUntilDead(t *testing.T) {
	a := &fakeTransport{name: "a"}
	b := &fakeTransport{name: "b"}
	fo, err := NewFailoverTransport([]gomailer.Transport{a, b}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// Three successful sends all go to a (sticky), never to b.
	for i := 0; i < 3; i++ {
		if _, err := fo.Send(ctx, newMsg(t), nil); err != nil {
			t.Fatal(err)
		}
	}
	if a.calls != 3 || b.calls != 0 {
		t.Errorf("failover not sticky: a=%d b=%d, want 3/0", a.calls, b.calls)
	}

	// a starts failing; failover moves to b and sticks there.
	a.fail = true
	for i := 0; i < 2; i++ {
		if _, err := fo.Send(ctx, newMsg(t), nil); err != nil {
			t.Fatalf("failover to b failed: %v", err)
		}
	}
	// a is tried once more (the failing send that marks it dead), then b sticks.
	if b.calls != 2 {
		t.Errorf("b should take both post-failover sends: calls=%d", b.calls)
	}
}

func TestEmptyTransportsRejected(t *testing.T) {
	if _, err := NewRoundRobinTransport(nil, time.Minute); err == nil {
		t.Error("RoundRobin with no transports should error")
	}
	if _, err := NewFailoverTransport(nil, time.Minute); err == nil {
		t.Error("Failover with no transports should error")
	}
}

func TestTransportsRouterByHeader(t *testing.T) {
	main := &fakeTransport{name: "main"}
	backup := &fakeTransport{name: "backup"}
	router, err := NewTransports(map[string]gomailer.Transport{
		"main":   main,
		"backup": backup,
	}, []string{"main", "backup"})
	if err != nil {
		t.Fatal(err)
	}

	// No header -> default (first in order = main).
	plain := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("s@a.io", "")).
		SetTo(gomailer.MustAddress("r@b.io", ""))
	if _, err := router.Send(context.Background(), plain, nil); err != nil {
		t.Fatal(err)
	}
	if main.calls != 1 || backup.calls != 0 {
		t.Errorf("default routing: main=%d backup=%d, want 1/0", main.calls, backup.calls)
	}

	// X-Transport header -> routes to backup, header is stripped before send.
	routed := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("s@a.io", "")).
		SetTo(gomailer.MustAddress("r@b.io", "")).
		SetHeader("X-Transport", "backup")
	if _, err := router.Send(context.Background(), routed, nil); err != nil {
		t.Fatal(err)
	}
	if backup.calls != 1 {
		t.Errorf("header routing: backup=%d, want 1", backup.calls)
	}
	if v, has := routed.Header("X-Transport"); !has || v != "backup" {
		t.Errorf("caller X-Transport header should be preserved on send-local clone, got %q has=%v", v, has)
	}
}

type mutatingTransport struct{ name string }

func (m mutatingTransport) Send(_ context.Context, msg gomailer.RawMessage, _ *gomailer.Envelope) (*gomailer.SentMessage, error) {
	if message, ok := msg.(*gomailer.Message); ok {
		message.SetHeader("X-Mutated", "yes")
	}
	return &gomailer.SentMessage{}, nil
}
func (m mutatingTransport) String() string { return m.name }

func TestTransportsRouterDefaultPathClonesCallerMessage(t *testing.T) {
	router, err := NewTransports(map[string]gomailer.Transport{"main": mutatingTransport{name: "main"}}, []string{"main"})
	if err != nil {
		t.Fatal(err)
	}
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("s@a.io", "")).
		SetTo(gomailer.MustAddress("r@b.io", ""))
	if _, err := router.Send(context.Background(), msg, nil); err != nil {
		t.Fatal(err)
	}
	if _, has := msg.Header("X-Mutated"); has {
		t.Fatal("default router path allowed target transport to mutate caller message")
	}
}

func TestTransportsRouterUnknownName(t *testing.T) {
	main := &fakeTransport{name: "main"}
	router, _ := NewTransports(map[string]gomailer.Transport{"main": main}, []string{"main"})
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("s@a.io", "")).
		SetTo(gomailer.MustAddress("r@b.io", "")).
		SetHeader("X-Transport", "ghost")
	_, err := router.Send(context.Background(), msg, nil)
	if !errors.Is(err, gomailer.ErrInvalidArgument) {
		t.Fatalf("unknown transport name should error with ErrInvalidArgument, got %v", err)
	}
}

func TestTransportsRouterEmptyHeaderIsInvalidAndStripped(t *testing.T) {
	main := &fakeTransport{name: "main"}
	router, _ := NewTransports(map[string]gomailer.Transport{"main": main}, []string{"main"})
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("s@a.io", "")).
		SetTo(gomailer.MustAddress("r@b.io", "")).
		SetHeader("X-Transport", "")
	_, err := router.Send(context.Background(), msg, nil)
	if !errors.Is(err, gomailer.ErrInvalidArgument) {
		t.Fatalf("empty X-Transport should error with ErrInvalidArgument, got %v", err)
	}
	if main.calls != 0 {
		t.Fatalf("empty X-Transport should not fall back to default, calls=%d", main.calls)
	}
	if v, has := msg.Header("X-Transport"); !has || v != "" {
		t.Fatalf("caller X-Transport header should be preserved on routing error, got %q has=%v", v, has)
	}
}

func TestTransportsRouterPreservesCallerHeaderOnFailure(t *testing.T) {
	main := &fakeTransport{name: "main", fail: true}
	router, _ := NewTransports(map[string]gomailer.Transport{"main": main}, []string{"main"})
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("s@a.io", "")).
		SetTo(gomailer.MustAddress("r@b.io", "")).
		SetHeader("X-Transport", "main")
	if _, err := router.Send(context.Background(), msg, nil); err == nil {
		t.Fatal("expected send failure")
	}
	if v, has := msg.Header("X-Transport"); !has || v != "main" {
		t.Errorf("X-Transport should be preserved on failure, got %q has=%v", v, has)
	}
}

func TestTransportsRouterConcurrentSharedMessage(t *testing.T) {
	router, err := NewTransports(map[string]gomailer.Transport{
		"main":   NewNullTransport(),
		"backup": NewNullTransport(),
	}, []string{"main", "backup"})
	if err != nil {
		t.Fatal(err)
	}
	msg := gomailer.NewMessage().
		SetFrom(gomailer.MustAddress("s@a.io", "")).
		SetTo(gomailer.MustAddress("r@b.io", "")).
		SetHeader("X-Transport", "backup")

	done := make(chan error, 8)
	for i := 0; i < cap(done); i++ {
		go func() {
			_, err := router.Send(context.Background(), msg, nil)
			done <- err
		}()
	}
	for i := 0; i < cap(done); i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// closableTransport records Close calls and can carry an SMTP-style code.
type closableTransport struct {
	name   string
	code   int
	closed int
}

func (c *closableTransport) Send(context.Context, gomailer.RawMessage, *gomailer.Envelope) (*gomailer.SentMessage, error) {
	te := gomailer.NewTransportError("rejected by " + c.name)
	te.Code = c.code
	return nil, te
}
func (c *closableTransport) String() string { return c.name }
func (c *closableTransport) Close() error   { c.closed++; return nil }

// The aggregate error from a composite must let errors.As reach an underlying
// *TransportError carrying the per-transport SMTP Code. (#8)
func TestFailoverPreservesUnderlyingSMTPCode(t *testing.T) {
	a := &closableTransport{name: "a", code: 451}
	b := &closableTransport{name: "b", code: 550}
	fo, err := NewFailoverTransport([]gomailer.Transport{a, b}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fo.Send(context.Background(), newMsg(t), nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	var te *gomailer.TransportError
	if !errors.As(err, &te) {
		t.Fatalf("errors.As did not reach a *TransportError: %v", err)
	}
	if te.Code == 0 {
		t.Errorf("underlying SMTP code was lost across failover aggregation (got 0)")
	}
}

// Close on a composite must fan out to every child implementing io.Closer. (#9)
func TestRoundRobinCloseFansOut(t *testing.T) {
	a := &closableTransport{name: "a"}
	b := &closableTransport{name: "b"}
	rr, err := NewRoundRobinTransport([]gomailer.Transport{a, b}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := rr.Close(); err != nil {
		t.Fatal(err)
	}
	if a.closed != 1 || b.closed != 1 {
		t.Errorf("Close did not reach every child: a=%d b=%d, want 1/1", a.closed, b.closed)
	}
}

func TestTransportsRouterCloseFansOut(t *testing.T) {
	a := &closableTransport{name: "a"}
	b := &closableTransport{name: "b"}
	router, err := NewTransports(map[string]gomailer.Transport{"a": a, "b": b}, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	if a.closed != 1 || b.closed != 1 {
		t.Errorf("router Close did not reach every child: a=%d b=%d", a.closed, b.closed)
	}
}
