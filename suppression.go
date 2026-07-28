package gomailer

import (
	"context"
	"time"
)

// SuppressionReason defines the cause of an email address being suppressed by a provider.
type SuppressionReason string

const (
	// SuppressionReasonBounce indicates the address was suppressed due to a hard bounce.
	SuppressionReasonBounce SuppressionReason = "bounce"
	// SuppressionReasonComplaint indicates the address was suppressed due to a recipient complaint.
	SuppressionReasonComplaint SuppressionReason = "complaint"
	// SuppressionReasonUnknown indicates the reason is not recognized or not provided.
	SuppressionReasonUnknown SuppressionReason = ""
)

// String returns the string representation of the SuppressionReason.
func (r SuppressionReason) String() string {
	return string(r)
}

// Suppression holds details about a suppressed email address.
type Suppression struct {
	// Email is the suppressed email address.
	Email string
	// Reason is the cause of suppression.
	Reason SuppressionReason
	// UpdatedAt is when the provider added or last updated this entry.
	// This is time.Time's zero value if the update time is unknown.
	UpdatedAt time.Time
}

// SuppressionProvider is an optional interface that Transports can implement
// to allow applications to retrieve their upstream suppression lists.
//
// To detect whether a Transport supports listing suppressions, perform a type assertion:
//
//	if sp, ok := transport.(gomailer.SuppressionProvider); ok {
//	    suppressions, err := sp.ListSuppressions(ctx)
//	    // ...
//	}
//
// Implementations of this interface should handle provider-side pagination internally
// and return the complete list of suppression entries.
type SuppressionProvider interface {
	// ListSuppressions returns the full list of email suppressions from the provider.
	ListSuppressions(ctx context.Context) ([]Suppression, error)
}
