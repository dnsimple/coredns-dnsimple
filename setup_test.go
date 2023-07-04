package dnsimple

import (
	"context"
	"testing"

	"github.com/coredns/caddy"
	"github.com/stretchr/testify/assert"
)

func TestSetupDNSimple(t *testing.T) {
	fakeClient := new(fakeDNSimpleClient)
	newDnsimpleService = func(ctx context.Context, accessToken, baseUrl string) (dnsimpleService, error) {
		return fakeClient, nil
	}

	tests := []struct {
		body          string
		expectedError bool
	}{
		{`dnsimple`, true}, // TODO: skip and warn when no zones are defined
		{`dnsimple :`, true},
		{`dnsimple ::`, true},
		{`dnsimple example.org`, false},
		{`dnsimple example.org:AMS { }`, false},
		{`dnsimple example.org {
			wat
		}`, true},
	}

	for _, test := range tests {
		c := caddy.NewTestController("dns", test.body)
		err := setup(c)

		if test.expectedError {
			assert.Error(t, err, "Expected error, but got none")
		} else {
			assert.NoError(t, err, "Unexpected error occurred")
		}
	}
}
