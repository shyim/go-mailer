package ses

import (
	"testing"

	"github.com/shyim/go-mailer/transport"
)

func TestFactorySupports(t *testing.T) {
	d, err := transport.ParseDSN("ses://default?region=us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if !(Factory{}).Supports(d) {
		t.Error("factory should support the ses scheme")
	}

	other, _ := transport.ParseDSN("smtp://host")
	if (Factory{}).Supports(other) {
		t.Error("factory should not support the smtp scheme")
	}
}

func TestFactorySupportedSchemes(t *testing.T) {
	got := (Factory{}).SupportedSchemes()
	if len(got) != 1 || got[0] != "ses" {
		t.Errorf("SupportedSchemes() = %v, want [ses]", got)
	}
}

// TestDSNOptionsParsedIntoConfig verifies the factory translates DSN
// credentials/options into the corresponding config, independent of AWS config
// loading (which New performs and which needs no network when a region and
// static credentials are supplied).
func TestDSNOptionsParsedIntoConfig(t *testing.T) {
	d, err := transport.ParseDSN("ses://AKIAEXAMPLE:secret@default?region=eu-central-1&configuration_set=prod")
	if err != nil {
		t.Fatal(err)
	}

	// Build the option slice the factory would build, then resolve it.
	cfg := newConfig()
	for _, opt := range factoryOptions(d) {
		opt(cfg)
	}

	if cfg.region != "eu-central-1" {
		t.Errorf("region = %q, want eu-central-1", cfg.region)
	}
	if cfg.configurationSet != "prod" {
		t.Errorf("configurationSet = %q, want prod", cfg.configurationSet)
	}
	if !cfg.hasStaticCreds || cfg.accessKeyID != "AKIAEXAMPLE" || cfg.secretAccessKey != "secret" {
		t.Errorf("static creds not parsed: %+v", cfg)
	}
}
