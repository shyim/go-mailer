package ses

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// Option configures an SES Transport built with New or NewWithClient.
type Option func(*config)

// config holds resolved option values.
type config struct {
	region           string
	configurationSet string

	accessKeyID     string
	secretAccessKey string
	sessionToken    string
	hasStaticCreds  bool
}

func newConfig() *config { return &config{} }

// WithRegion sets the AWS region (e.g. "us-east-1"). When unset, the region is
// resolved by the SDK's default chain (AWS_REGION, shared config, ...).
func WithRegion(region string) Option {
	return func(c *config) { c.region = region }
}

// WithConfigurationSet sets the SES configuration set applied to each send
// (used for event publishing, dedicated IPs, etc.). Empty disables it.
func WithConfigurationSet(name string) Option {
	return func(c *config) { c.configurationSet = name }
}

// WithCredentials supplies static AWS credentials. sessionToken may be empty.
// When this option is not used, New falls back to the SDK's default credential
// chain (environment, shared credentials file, IAM role, ...).
func WithCredentials(accessKeyID, secretAccessKey, sessionToken string) Option {
	return func(c *config) {
		c.accessKeyID = accessKeyID
		c.secretAccessKey = secretAccessKey
		c.sessionToken = sessionToken
		c.hasStaticCreds = true
	}
}

// loadAWSConfig builds an aws.Config from the resolved options: an explicit
// region when set, and static credentials when WithCredentials was used;
// otherwise everything is left to the SDK's default chain.
func (c *config) loadAWSConfig(ctx context.Context) (aws.Config, error) {
	var loadOpts []func(*awsconfig.LoadOptions) error
	if c.region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(c.region))
	}
	if c.hasStaticCreds {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(c.accessKeyID, c.secretAccessKey, c.sessionToken),
		))
	}
	return awsconfig.LoadDefaultConfig(ctx, loadOpts...)
}
