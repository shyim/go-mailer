package ses

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	gomailer "github.com/shyim/go-mailer"
)

// fakeSendOnlySES implements sesAPI but not suppressionAPI.
type fakeSendOnlySES struct{}

func (f *fakeSendOnlySES) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	return nil, nil
}

// fakeFullSES implements both sesAPI and suppressionAPI.
type fakeFullSES struct {
	// SendEmail mocks
	sendCalls  int
	sendErr    error
	lastSendIn *sesv2.SendEmailInput
	sendMsgID  string

	// ListSuppressedDestinations mocks
	listCalls  int
	listErr    error
	listPages  []*sesv2.ListSuppressedDestinationsOutput
	lastListIn *sesv2.ListSuppressedDestinationsInput

	// GetSuppressedDestination mocks
	getCalls  int
	getErr    error
	getOutput *sesv2.GetSuppressedDestinationOutput
	lastGetIn *sesv2.GetSuppressedDestinationInput
}

func (f *fakeFullSES) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.sendCalls++
	f.lastSendIn = in
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return &sesv2.SendEmailOutput{MessageId: aws.String(f.sendMsgID)}, nil
}

func (f *fakeFullSES) ListSuppressedDestinations(_ context.Context, in *sesv2.ListSuppressedDestinationsInput, _ ...func(*sesv2.Options)) (*sesv2.ListSuppressedDestinationsOutput, error) {
	f.listCalls++
	f.lastListIn = in
	if f.listErr != nil {
		return nil, f.listErr
	}
	idx := f.listCalls - 1
	if idx < len(f.listPages) {
		return f.listPages[idx], nil
	}
	return &sesv2.ListSuppressedDestinationsOutput{}, nil
}

func (f *fakeFullSES) GetSuppressedDestination(_ context.Context, in *sesv2.GetSuppressedDestinationInput, _ ...func(*sesv2.Options)) (*sesv2.GetSuppressedDestinationOutput, error) {
	f.getCalls++
	f.lastGetIn = in
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getOutput, nil
}

func TestListSuppressions_NotSupported(t *testing.T) {
	tr := NewWithClient(&fakeSendOnlySES{})
	_, err := tr.ListSuppressions(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, gomailer.ErrTransport) {
		t.Errorf("expected gomailer.ErrTransport, got %v", err)
	}
	if !strings.Contains(err.Error(), "does not support suppression listing") {
		t.Errorf("expected error message about unsupported suppression listing, got %v", err)
	}
}

func TestIsSuppressed_NotSupported(t *testing.T) {
	tr := NewWithClient(&fakeSendOnlySES{})
	_, _, err := tr.IsSuppressed(context.Background(), "test@example.com")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, gomailer.ErrTransport) {
		t.Errorf("expected gomailer.ErrTransport, got %v", err)
	}
	if !strings.Contains(err.Error(), "does not support suppression checks") {
		t.Errorf("expected error message about unsupported suppression checks, got %v", err)
	}
}

func TestListSuppressions_Empty(t *testing.T) {
	fake := &fakeFullSES{
		listPages: []*sesv2.ListSuppressedDestinationsOutput{
			{
				SuppressedDestinationSummaries: []types.SuppressedDestinationSummary{},
			},
		},
	}
	tr := NewWithClient(fake)
	list, err := tr.ListSuppressions(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %v", list)
	}
	if fake.listCalls != 1 {
		t.Errorf("expected 1 call, got %d", fake.listCalls)
	}
}

func TestListSuppressions_PaginationAndMapping(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fake := &fakeFullSES{
		listPages: []*sesv2.ListSuppressedDestinationsOutput{
			{
				SuppressedDestinationSummaries: []types.SuppressedDestinationSummary{
					{
						EmailAddress:   aws.String("bounce@example.com"),
						Reason:         types.SuppressionListReasonBounce,
						LastUpdateTime: &now,
					},
				},
				NextToken: aws.String("token-1"),
			},
			{
				SuppressedDestinationSummaries: []types.SuppressedDestinationSummary{
					{
						EmailAddress:   aws.String("complaint@example.com"),
						Reason:         types.SuppressionListReasonComplaint,
						LastUpdateTime: &now,
					},
					{
						EmailAddress:   aws.String("unknown@example.com"),
						Reason:         types.SuppressionListReason("UNKNOWN_REASON"),
						LastUpdateTime: nil,
					},
				},
				NextToken: nil,
			},
		},
	}
	tr := NewWithClient(fake)
	list, err := tr.ListSuppressions(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(list))
	}
	if fake.listCalls != 2 {
		t.Errorf("expected 2 calls, got %d", fake.listCalls)
	}

	// Verify mapping
	expected := []gomailer.Suppression{
		{
			Email:     "bounce@example.com",
			Reason:    gomailer.SuppressionReasonBounce,
			UpdatedAt: now,
		},
		{
			Email:     "complaint@example.com",
			Reason:    gomailer.SuppressionReasonComplaint,
			UpdatedAt: now,
		},
		{
			Email:     "unknown@example.com",
			Reason:    gomailer.SuppressionReasonUnknown,
			UpdatedAt: time.Time{},
		},
	}

	for i, got := range list {
		exp := expected[i]
		if got.Email != exp.Email {
			t.Errorf("[%d] Email = %q, want %q", i, got.Email, exp.Email)
		}
		if got.Reason != exp.Reason {
			t.Errorf("[%d] Reason = %q, want %q", i, got.Reason, exp.Reason)
		}
		if !got.UpdatedAt.Equal(exp.UpdatedAt) {
			t.Errorf("[%d] UpdatedAt = %v, want %v", i, got.UpdatedAt, exp.UpdatedAt)
		}
	}
}

func TestListSuppressions_Error(t *testing.T) {
	fake := &fakeFullSES{
		listErr: errors.New("ListError"),
	}
	tr := NewWithClient(fake)
	_, err := tr.ListSuppressions(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, gomailer.ErrTransport) {
		t.Errorf("expected gomailer.ErrTransport, got %v", err)
	}
	var te *gomailer.TransportError
	if errors.As(err, &te) {
		if !strings.Contains(te.Cause.Error(), "ListError") {
			t.Errorf("expected underlying cause to be ListError, got %v", te.Cause)
		}
	} else {
		t.Error("error is not a *TransportError")
	}
}

func TestIsSuppressed_EmptyEmail(t *testing.T) {
	tr := NewWithClient(&fakeFullSES{})
	_, _, err := tr.IsSuppressed(context.Background(), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, gomailer.ErrInvalidArgument) {
		t.Errorf("expected gomailer.ErrInvalidArgument, got %v", err)
	}
}

func TestIsSuppressed_Found(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fake := &fakeFullSES{
		getOutput: &sesv2.GetSuppressedDestinationOutput{
			SuppressedDestination: &types.SuppressedDestination{
				EmailAddress:   aws.String("suppressed@example.com"),
				Reason:         types.SuppressionListReasonBounce,
				LastUpdateTime: &now,
			},
		},
	}
	tr := NewWithClient(fake)
	supp, found, err := tr.IsSuppressed(context.Background(), "suppressed@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true, got false")
	}
	if supp == nil {
		t.Fatal("expected non-nil suppression")
	}
	if supp.Email != "suppressed@example.com" {
		t.Errorf("Email = %q, want suppressed@example.com", supp.Email)
	}
	if supp.Reason != gomailer.SuppressionReasonBounce {
		t.Errorf("Reason = %q, want bounce", supp.Reason)
	}
	if !supp.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", supp.UpdatedAt, now)
	}
}

func TestIsSuppressed_NotFound(t *testing.T) {
	fake := &fakeFullSES{
		getErr: &types.NotFoundException{
			Message: aws.String("Destination not found"),
		},
	}
	tr := NewWithClient(fake)
	supp, found, err := tr.IsSuppressed(context.Background(), "notfound@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false, got true")
	}
	if supp != nil {
		t.Fatalf("expected nil suppression, got %v", supp)
	}
}

func TestIsSuppressed_OtherError(t *testing.T) {
	fake := &fakeFullSES{
		getErr: errors.New("SomeGetError"),
	}
	tr := NewWithClient(fake)
	_, _, err := tr.IsSuppressed(context.Background(), "error@example.com")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, gomailer.ErrTransport) {
		t.Errorf("expected gomailer.ErrTransport, got %v", err)
	}
	var te *gomailer.TransportError
	if errors.As(err, &te) {
		if !strings.Contains(te.Cause.Error(), "SomeGetError") {
			t.Errorf("expected underlying cause to be SomeGetError, got %v", te.Cause)
		}
	} else {
		t.Error("error is not a *TransportError")
	}
}
