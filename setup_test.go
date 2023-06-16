package dnsimple

import (
	"context"
	"testing"

	"github.com/coredns/caddy"
)

func TestSetupDNSimple(t *testing.T) {
	f = func(ctx context.Context, opt Options) (dnsimpleZone, error) {
		return fakeDNSimpleClient{}, nil
	}

	tests := []struct {
		body          string
		expectedError bool
	}{
		{`dnsimple`, true}, // TODO: skip and warn when no zones are defined
		{`dnsimple :`, true},
		{`dnsimple ::`, true},
		{`dnsimple example.com`, false},
		{`dnsimple example.com:AMS { }`, false},
		{`dnsimple example.com { 
			wat
		}`, true},
	}

	for _, test := range tests {
		c := caddy.NewTestController("dns", test.body)
		if err := setup(c); (err == nil) == test.expectedError {
			t.Errorf("Unexpected errors: %v", err)
		}
	}
}
