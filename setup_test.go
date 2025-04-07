package dnsimple

import (
	"context"
	"testing"

	"github.com/coredns/caddy"
	"github.com/stretchr/testify/assert"
)

func TestSetupDNSimple(t *testing.T) {
	t.Setenv("DNSIMPLE_TOKEN", "token")
	t.Setenv("DNSIMPLE_ACCOUNT_ID", "12345")

	fakeClient := new(fakeDNSimpleClient)
	newDnsimpleService = func(_ context.Context, _ Options, _, baseUrl string) (dnsimpleService, error) {
		return fakeClient, nil
	}

	tests := []struct {
		body          string
		expectedError bool
	}{
		{`dnsimple`, true},
		{`dnsimple :`, true},
		{`dnsimple ::`, true},
		{`dnsimple example.org`, false},
		{`dnsimple example.org:`, false},
		{`dnsimple example.org.`, false},
		{`dnsimple example.org:AMS { }`, false},
		{`dnsimple example.org {
			access_token
		}`, true},
		{`dnsimple example.org {
			account_id 12345
		}`, false},
		{`dnsimple example.org {
			base_url https://api.dnsimple.com/
		}`, false},
		{`dnsimple example.org {
			identifier example
		}`, false},
		{`dnsimple example.org {
			max_retries 10
		}`, false},
		{`dnsimple example.org {
			refresh 1h
		}`, false},
		{`dnsimple example.org {
			refresh 2s
		}`, false},
		{`dnsimple example.org {
			wat
		}`, true},
	}

	for _, test := range tests {
		c := caddy.NewTestController("dns", test.body)
		err := setup(c)

		if test.expectedError {
			assert.Error(t, err, "Expected error for %s, but got none", test.body)
		} else {
			assert.NoError(t, err, "Unexpected error occurred for %s", test.body)
		}
	}
}
