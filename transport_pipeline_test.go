package gomailer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func testMessage(t *testing.T) *Message {
	t.Helper()
	return NewMessage().
		SetFrom(MustAddress("s@a.io", "")).
		SetTo(MustAddress("r@b.io", "")).
		SetSubject("hi").
		SetText([]byte("body"))
}

func TestBaseTransportSend(t *testing.T) {
	delivered := 0
	bt := &BaseTransport{Name: "test://", DoSend: func(context.Context, *SentMessage) error {
		delivered++
		return nil
	}}

	sm, err := bt.Send(context.Background(), testMessage(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if sm == nil {
		t.Fatal("expected a SentMessage")
	}
	if delivered != 1 {
		t.Errorf("doSend called %d times, want 1", delivered)
	}
}

func TestBaseTransportSendWrapsFailure(t *testing.T) {
	bt := &BaseTransport{Name: "test://", DoSend: func(context.Context, *SentMessage) error {
		return NewTransportError("delivery exploded")
	}}

	_, err := bt.Send(context.Background(), testMessage(t), nil)
	if err == nil {
		t.Fatal("expected a delivery error")
	}
	if !errors.Is(err, ErrTransport) {
		t.Errorf("delivery error should wrap ErrTransport: %v", err)
	}
}

func TestThrottling(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in -short")
	}
	// 20 msg/s => 50ms minimum spacing between sends.
	bt := &BaseTransport{Name: "test://", DoSend: func(context.Context, *SentMessage) error { return nil }}
	bt.SetMaxPerSecond(20)

	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := bt.Send(ctx, testMessage(t), nil); err != nil {
			t.Fatal(err)
		}
	}
	// 3 sends at 50ms spacing => at least ~100ms elapsed (first send is free).
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Errorf("throttling too fast: 3 sends took %v, expected >= ~100ms", elapsed)
	}
}

func TestThrottlingDisabledByDefault(t *testing.T) {
	bt := &BaseTransport{Name: "test://", DoSend: func(context.Context, *SentMessage) error { return nil }}
	start := time.Now()
	for i := 0; i < 50; i++ {
		if _, err := bt.Send(context.Background(), testMessage(t), nil); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("no throttle configured but 50 sends took %v", elapsed)
	}
}

func TestThrottlingPacesConcurrentDeliveryStarts(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in -short")
	}
	var mu sync.Mutex
	var starts []time.Time
	bt := &BaseTransport{Name: "test://", DoSend: func(context.Context, *SentMessage) error {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return nil
	}}
	bt.SetMaxPerSecond(20) // 50ms between delivery starts.
	env, err := NewEnvelope(MustAddress("s@a.io", ""), []Address{MustAddress("r@b.io", "")})
	if err != nil {
		t.Fatal(err)
	}
	msg := NewRawMessage([]byte("Subject: hi\r\n\r\nbody"))

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := bt.Send(context.Background(), msg, env); err != nil {
				t.Errorf("Send: %v", err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 3 {
		t.Fatalf("recorded %d starts, want 3", len(starts))
	}
	// Starts are recorded in delivery order because checkThrottling serializes
	// callers before DoSend. Allow scheduler jitter.
	if elapsed := starts[2].Sub(starts[0]); elapsed < 80*time.Millisecond {
		t.Fatalf("concurrent delivery starts were not throttled: %v", elapsed)
	}
}
