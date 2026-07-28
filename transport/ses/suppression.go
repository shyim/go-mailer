package ses

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	gomailer "github.com/shyim/go-mailer"
)

// suppressionAPI defines the subset of the AWS SES v2 API required for managing
// and querying account-level email suppression lists.
type suppressionAPI interface {
	ListSuppressedDestinations(ctx context.Context, in *sesv2.ListSuppressedDestinationsInput, optFns ...func(*sesv2.Options)) (*sesv2.ListSuppressedDestinationsOutput, error)
	GetSuppressedDestination(ctx context.Context, in *sesv2.GetSuppressedDestinationInput, optFns ...func(*sesv2.Options)) (*sesv2.GetSuppressedDestinationOutput, error)
}

// Compile-time verification that *Transport implements gomailer.SuppressionProvider.
var _ gomailer.SuppressionProvider = (*Transport)(nil)

// ListSuppressions returns the full list of email suppressions from Amazon SES.
// It handles API pagination automatically and collects all entries.
// If the underlying client does not support suppression operations, an error
// wrapped with gomailer.ErrTransport is returned.
func (t *Transport) ListSuppressions(ctx context.Context) ([]gomailer.Suppression, error) {
	sAPI, ok := t.client.(suppressionAPI)
	if !ok {
		return nil, fmt.Errorf("%w: SES client does not support suppression listing", gomailer.ErrTransport)
	}

	var results []gomailer.Suppression
	var nextToken *string

	for {
		in := &sesv2.ListSuppressedDestinationsInput{
			NextToken: nextToken,
		}
		out, err := sAPI.ListSuppressedDestinations(ctx, in)
		if err != nil {
			te := gomailer.NewTransportError("SES ListSuppressedDestinations failed")
			te.Cause = err
			return nil, te
		}

		if out != nil {
			for _, entry := range out.SuppressedDestinationSummaries {
				if entry.EmailAddress == nil {
					continue
				}

				var reason gomailer.SuppressionReason
				switch entry.Reason {
				case types.SuppressionListReasonBounce:
					reason = gomailer.SuppressionReasonBounce
				case types.SuppressionListReasonComplaint:
					reason = gomailer.SuppressionReasonComplaint
				default:
					reason = gomailer.SuppressionReasonUnknown
				}

				var updatedAt time.Time
				if entry.LastUpdateTime != nil {
					updatedAt = *entry.LastUpdateTime
				}

				results = append(results, gomailer.Suppression{
					Email:     *entry.EmailAddress,
					Reason:    reason,
					UpdatedAt: updatedAt,
				})
			}

			if out.NextToken == nil || *out.NextToken == "" {
				break
			}
			nextToken = out.NextToken
		} else {
			break
		}
	}

	return results, nil
}

// IsSuppressed checks whether a specific email address is currently on the Amazon SES suppression list.
// It returns the suppression details and true if found, (nil, false, nil) if not found, or an error
// if the check fails.
//
// If the email address is empty, it returns gomailer.ErrInvalidArgument.
// If the underlying client does not support suppression operations, it returns an error wrapped with
// gomailer.ErrTransport.
func (t *Transport) IsSuppressed(ctx context.Context, email string) (*gomailer.Suppression, bool, error) {
	if email == "" {
		return nil, false, fmt.Errorf("%w: email address must not be empty", gomailer.ErrInvalidArgument)
	}

	sAPI, ok := t.client.(suppressionAPI)
	if !ok {
		return nil, false, fmt.Errorf("%w: SES client does not support suppression checks", gomailer.ErrTransport)
	}

	out, err := sAPI.GetSuppressedDestination(ctx, &sesv2.GetSuppressedDestinationInput{
		EmailAddress: aws.String(email),
	})
	if err != nil {
		var nfe *types.NotFoundException
		if errors.As(err, &nfe) {
			return nil, false, nil
		}
		te := gomailer.NewTransportError("SES GetSuppressedDestination failed")
		te.Cause = err
		return nil, false, te
	}

	if out == nil || out.SuppressedDestination == nil {
		return nil, false, nil
	}

	dest := out.SuppressedDestination
	var emailAddress string
	if dest.EmailAddress != nil {
		emailAddress = *dest.EmailAddress
	}

	var reason gomailer.SuppressionReason
	switch dest.Reason {
	case types.SuppressionListReasonBounce:
		reason = gomailer.SuppressionReasonBounce
	case types.SuppressionListReasonComplaint:
		reason = gomailer.SuppressionReasonComplaint
	default:
		reason = gomailer.SuppressionReasonUnknown
	}

	var updatedAt time.Time
	if dest.LastUpdateTime != nil {
		updatedAt = *dest.LastUpdateTime
	}

	return &gomailer.Suppression{
		Email:     emailAddress,
		Reason:    reason,
		UpdatedAt: updatedAt,
	}, true, nil
}
