package dnsimple

import (
	"context"

	"github.com/dnsimple/dnsimple-go/dnsimple"
)

type fakeDNSimpleClient struct {
	*dnsimple.Client
}

func (c fakeDNSimpleClient) listZoneRecords(ctx context.Context, accountID string, zoneName string, options *dnsimple.ZoneRecordListOptions, maxRetries int) (*dnsimple.ZoneRecordsResponse, error) {
	fakeZoneRecordsResponse := &dnsimple.ZoneRecordsResponse{
		Data: []dnsimple.ZoneRecord{
			{
				ID:   1234,
				Type: "A",
			},
		},
	}

	return fakeZoneRecordsResponse, nil
}

func (c fakeDNSimpleClient) zoneExists(ctx context.Context, accountID string, zoneName string) error {
	return nil
}
