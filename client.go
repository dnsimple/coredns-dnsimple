package dnsimple

import (
	"context"
	"strings"

	"github.com/dnsimple/dnsimple-go/dnsimple"
)

// dnsimpleClient is a wrapper for `dnsimple.Client`.
type dnsimpleClient struct {
	*dnsimple.Client
}

// dnsimpleService is an interface for dnsimpleClient.
type dnsimpleService interface {
	getZone(ctx context.Context, accountID string, zoneName string) (*dnsimple.Zone, error)
	listZoneRecords(ctx context.Context, accountID string, zoneName string, options *dnsimple.ZoneRecordListOptions, maxRetries int) ([]dnsimple.ZoneRecord, error)
}

// getZone is a wrapper method around `dnsimple.Client.Zones.GetZone`
// it checks if a zone exists in DNSimple.
func (c dnsimpleClient) getZone(ctx context.Context, accountID string, zoneName string) (*dnsimple.Zone, error) {
	response, err := c.Zones.GetZone(ctx, accountID, strings.TrimSuffix(zoneName, "."))
	if err != nil {
		return nil, err
	}
	return response.Data, nil
}

// listZoneRecords is a wrapper for `dnsimple.Client.Zones.ListRecords`.
// It fetches and returns all record sets for a zone handling pagination.
func (c dnsimpleClient) listZoneRecords(ctx context.Context, accountID string, zoneName string, options *dnsimple.ZoneRecordListOptions, maxRetries int) ([]dnsimple.ZoneRecord, error) {
	var rs []dnsimple.ZoneRecord

	// Fetch all records for the zone.
	for {
		var response *dnsimple.ZoneRecordsResponse
		listErr := retryable(maxRetries, func() (listErr error) {
			// Our API does not expect the zone name to end with a dot.
			response, listErr = c.Zones.ListRecords(ctx, accountID, strings.TrimSuffix(zoneName, "."), options)
			return
		})
		if listErr != nil {
			return nil, listErr
		}
		rs = append(rs, response.Data...)
		if response.Pagination.CurrentPage >= response.Pagination.TotalPages {
			break
		}
		options.Page = dnsimple.Int(response.Pagination.CurrentPage + 1)
	}

	return rs, nil
}
