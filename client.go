package dnsimple

import (
	"context"
	"encoding/json"
	"fmt"
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
	listZoneRecords(ctx context.Context, accountID string, zoneName string, maxRetries int) ([]dnsimple.ZoneRecord, error)
	updateZoneStatus(accountID string, apiCaller APICaller, maxRetries int, status updateZoneStatusRequest) (err error)
}

// getZone is a wrapper method around `dnsimple.Client.Zones.GetZone`
// it checks if a zone exists in DNSimple.
func (c dnsimpleClient) getZone(ctx context.Context, accountID string, zoneName string) (*dnsimple.Zone, error) {
	response, err := c.Zones.GetZone(ctx, accountID, zoneName)
	if err != nil {
		return nil, err
	}
	return response.Data, nil
}

// listZoneRecords is a wrapper for `dnsimple.Client.Zones.ListRecords`.
// It fetches and returns all record sets for a zone handling pagination.
func (c dnsimpleClient) listZoneRecords(ctx context.Context, accountID string, zoneName string, maxRetries int) ([]dnsimple.ZoneRecord, error) {
	var rs []dnsimple.ZoneRecord

	listOptions := &dnsimple.ZoneRecordListOptions{}
	listOptions.PerPage = dnsimple.Int(100)

	// Fetch all records for the zone.
	for {
		var response *dnsimple.ZoneRecordsResponse
		listErr := retryable(maxRetries, func() (listErr error) {
			// Our API does not expect the zone name to end with a dot.
			response, listErr = c.Zones.ListRecords(ctx, accountID, strings.TrimSuffix(zoneName, "."), listOptions)
			return
		})
		if listErr != nil {
			return nil, listErr
		}
		rs = append(rs, response.Data...)
		if response.Pagination.CurrentPage >= response.Pagination.TotalPages {
			break
		}
		listOptions.Page = dnsimple.Int(response.Pagination.CurrentPage + 1)
	}

	return rs, nil
}

func (c dnsimpleClient) updateZoneStatus(accountID string, apiCaller APICaller, maxRetries int, status updateZoneStatusRequest) (err error) {
	statusJSON, err := json.Marshal(status)
	if err != nil {
		panic(fmt.Errorf("failed to serialize status request: %w", err))
	}
	sendStatusError := retryable(maxRetries, func() (err error) {
		err = apiCaller(
			fmt.Sprintf("/v2/%s/platform/statuses", accountID),
			statusJSON,
		)
		return
	})
	if sendStatusError != nil {
		return sendStatusError
	}
	return nil
}
