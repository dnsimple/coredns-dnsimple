package dnsimple

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/file"
	"github.com/coredns/coredns/plugin/pkg/fall"
	"github.com/coredns/coredns/plugin/pkg/upstream"
	"github.com/coredns/coredns/request"
	"github.com/dnsimple/dnsimple-go/dnsimple"

	"github.com/miekg/dns"
)

func retryable(maxRetries int, cb func() error) error {
	for i := 1; i <= 1+maxRetries; i++ {
		err := cb()
		if err == nil {
			break
		}
		if i == 1+maxRetries {
			return err
		}
		log.Warningf("[attempt %d] %v", i, err)
		// Exponential backoff.
		time.Sleep((1 << i) * time.Second)
	}
	return nil
}

type DNSimpleApiCaller func(path string, body []byte) error

func createDNSimpleApiCaller(baseUrl string, accessToken string, userAgent string) DNSimpleApiCaller {
	return func(path string, body []byte) error {
		url := fmt.Sprintf("%s%s", baseUrl, path)
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
		if err != nil {
			return err
		}
		req.Header.Set("authorization", fmt.Sprintf("Bearer %s", accessToken))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("user-agent", userAgent)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			body, err := io.ReadAll(res.Body)
			if err == nil {
				return fmt.Errorf("bad status code of %d: %s", res.StatusCode, string(body))
			}
			return fmt.Errorf("bad status code of %d (response could not be read)", res.StatusCode)
		}
		return nil
	}
}

// DNSimple is a plugin that returns RR from DNSimple.
type DNSimple struct {
	Next plugin.Handler
	Fall fall.F

	// Each zone name contains a trailing dot.
	zoneNames   []string
	client     dnsimpleService
	accountId   string
	apiCaller   DNSimpleApiCaller
	identifier  string
	upstream    *upstream.Upstream
	refresh     time.Duration
	maxRetries  int

	lock  sync.RWMutex
	zones zones
}

type zone struct {
	id int64
	// This contains the trailing dot.
	name   string
	pools  map[string][]string
	region string
	zone   *file.Zone
}

type zones map[string][]*zone

func New(ctx context.Context, client dnsimpleService, keys map[string][]string, opts Options) (*DNSimple, error) {
	zones := make(map[string][]*zone, len(keys))
	zoneNames := make([]string, 0, len(keys))

	for zoneName, hostedZoneRegions := range keys {
		// Check if the zone exists.
		// Our API does not expect the zone name to end with a dot.
		res, err := client.getZone(ctx, opts.accountId, zoneName)
		if err != nil {
			return nil, err
		}
		if _, ok := zones[zoneName]; !ok {
			zoneNames = append(zoneNames, zoneName)
		}
		for _, hostedZoneRegion := range hostedZoneRegions {
			zones[zoneName] = append(zones[zoneName], &zone{id: res.ID, name: zoneName, region: hostedZoneRegion, zone: file.NewZone(zoneName, "")})
		}
	}
	return &DNSimple{
		accountId:  opts.accountId,
		apiCaller:   opts.apiCaller,
		client:     client,
		identifier: opts.identifier,
		maxRetries: opts.maxRetries,
		refresh:    opts.refresh,
		upstream:   upstream.New(),
		zoneNames:  zoneNames,
		zones:      zones,
	}, nil
}

func (h *DNSimple) Run(ctx context.Context) error {
	if err := h.updateZones(ctx); err != nil {
		return err
	}
	go func() {
		timer := time.NewTimer(h.refresh)
		defer timer.Stop()
		for {
			timer.Reset(h.refresh)
			select {
			case <-ctx.Done():
				log.Debugf("breaking out of update loop for %v: %v", h.zoneNames, ctx.Err())
				return
			case <-timer.C:
				// Don't log error if ctx expired.
				if err := h.updateZones(ctx); err != nil && ctx.Err() == nil {
					log.Errorf("failed to update zones %v: %v", h.zoneNames, err)
				}
			}
		}
	}()
	return nil
}

func (h *DNSimple) callApi(path string, body []byte) error {
	return h.apiCaller(
		path,
		body,
	)
}

// ServeDNS implements the plugin.Handler.ServeDNS.
func (h *DNSimple) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	state := request.Request{W: w, Req: r}
	qname := state.Name()

	zoneName := plugin.Zones(h.zoneNames).Matches(qname)
	if zoneName == "" {
		return plugin.NextOrFailure(h.Name(), h.Next, ctx, w, r)
	}
	zone, ok := h.zones[zoneName]
	if !ok || zone == nil {
		return dns.RcodeServerFailure, nil
	}

	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true
	var result file.Result
	for _, regionalZone := range zone {
		h.lock.RLock()
		msg.Answer, msg.Ns, msg.Extra, result = regionalZone.zone.Lookup(ctx, state, qname)
		h.lock.RUnlock()
		maybeInterceptPoolResponse(regionalZone, &msg.Answer)

		// Take the answer if it's non-empty OR if there is another
		// record type exists for this name (NODATA).
		if len(msg.Answer) != 0 || result == file.NoData {
			break
		}
	}

	if len(msg.Answer) == 0 && result != file.NoData && h.Fall.Through(qname) {
		return plugin.NextOrFailure(h.Name(), h.Next, ctx, w, r)
	}

	switch result {
	case file.Success:
	case file.NoData:
	case file.NameError:
		msg.Rcode = dns.RcodeNameError
	case file.Delegation:
		msg.Authoritative = false
	case file.ServerFailure:
		return dns.RcodeServerFailure, nil
	}

	w.WriteMsg(msg)
	return dns.RcodeSuccess, nil
}

func maybeInterceptPoolResponse(zone *zone, answers *[]dns.RR) {
	if len(*answers) != 1 {
		return
	}
	switch cname := (*answers)[0].(type) {
	case *dns.CNAME:
		if pool, ok := zone.pools[cname.Hdr.Name]; ok {
			qname := cname.Hdr.Name
			ttl := cname.Hdr.Ttl
			idx := rand.Intn(len(pool))
			r := new(dns.CNAME)
			r.Hdr = dns.RR_Header{
				Name:   qname,
				Rrtype: dns.TypeCNAME,
				Class:  dns.ClassINET,
				Ttl:    ttl,
			}
			r.Target = pool[idx] + "."
			*answers = []dns.RR{r}
		}
	}
}

func recordInZoneRegion(recordRegions []string, zoneRegion string) bool {
	for _, v := range recordRegions {
		if v == zoneRegion || v == "global" {
			return true
		}
	}

	return false
}

type updateZoneRecordFailure struct {
	Record dnsimple.ZoneRecord `json:"record"`
	Error  string              `json:"error"`
}

type updateZoneStatusMessage struct {
	Time              string                    `json:"time"`
	CorednsIdentifier string                    `json:"coredns_identifier"`
	Error             string                    `json:"error,omitempty"`
	FailedRecords     []updateZoneRecordFailure `json:"failed_records,omitempty"`
}

type updateZoneStatusRequest struct {
	Resource string                  `json:"resource"`
	State    string                  `json:"state"`
	Title    string                  `json:"title"`
	Data     updateZoneStatusMessage `json:"data"`
}

func updateZoneFromRecords(zoneName string, records []dnsimple.ZoneRecord, zoneRegion string, pools map[string][]string, zone *file.Zone) []updateZoneRecordFailure {
	log.Debugf("updating zone %s with region %s", zoneName, zoneRegion)
	failures := make([]updateZoneRecordFailure, 0)
	for _, rec := range records {
		var fqdn string
		if rec.Name == "" {
			fqdn = zoneName
		} else {
			fqdn = fmt.Sprintf("%s.%s", rec.Name, zoneName)
		}

		if !recordInZoneRegion(rec.Regions, zoneRegion) {
			continue
		}

		if rec.Type == "MX" {
			// MX records have a priority and a content field.
			rec.Content = fmt.Sprintf("%d %s", rec.Priority, rec.Content)
		}

		if rec.Type == "POOL" {
			rec.Type = "CNAME"
			isFirst := false
			if pools[fqdn] == nil {
				pools[fqdn] = make([]string, 0)
				isFirst = true
			}
			pools[fqdn] = append(pools[fqdn], rec.Content)
			if !isFirst {
				// We have already inserted a record for this POOL name, there's no point to adding more. As an interesting side note, the file plugin does not crash on multiple CNAME records with the same name, and will simply respond with all CNAMEs if matched, which doesn't appear to be spec compliant.
				continue
			}
		}

		// Assemble RFC 1035 conforming record to pass into DNS scanner.
		rfc1035 := fmt.Sprintf("%s %d IN %s %s", fqdn, rec.TTL, rec.Type, rec.Content)
		rr, err := dns.NewRR(rfc1035)
		if err != nil {
			log.Errorf("failed to parse resource record: %v", err)
			failures = append(failures, updateZoneRecordFailure{
				Record: rec,
				Error:  fmt.Sprintf("failed to parse resource record: %v", err),
			})
			continue
		}

		log.Debugf("inserting record %s", rfc1035)
		err = zone.Insert(rr)
		if err != nil {
			log.Errorf("failed to insert resource record: %v", err)
			failures = append(failures, updateZoneRecordFailure{
				Record: rec,
				Error:  fmt.Sprintf("failed to insert resource record: %v", err),
			})
			continue
		}
	}
	return failures
}

func (h *DNSimple) updateZones(ctx context.Context) error {
	log.Debugf("starting update zones for dnsimple with identifier %s", h.identifier)
	var wg sync.WaitGroup
	for zoneName, z := range h.zones {
		wg.Add(1)
		defer wg.Done()
		go func(zoneName string, z []*zone) {
			var zoneError error = nil

			var zoneRecords []dnsimple.ZoneRecord

			options := &dnsimple.ZoneRecordListOptions{}
			options.PerPage = dnsimple.Int(100)

			zoneRecords, err := h.client.listZoneRecords(ctx, h.accountId, zoneName, options, h.maxRetries)
			if err != nil {
				err = fmt.Errorf("failed to list resource records for %v from dnsimple: %v", zoneName, err)
				return
			}

			errorByRecordId := make(map[int64]updateZoneRecordFailure)
			if zoneError == nil {
				for i, regionalZone := range z {
					newZone := file.NewZone(zoneName, "")
					newZone.Upstream = h.upstream
					newPools := make(map[string][]string, 16)

					// Deduplicate errors by record ID, as otherwise we'll duplicate errors for all records that aren't regional. Note that some regional records may have errors, so we cannot just take the first region's errors only.
					failedRecords := updateZoneFromRecords(zoneName, zoneRecords, regionalZone.region, newPools, newZone)
					for _, f := range failedRecords {
						errorByRecordId[f.Record.ID] = f
					}

					h.lock.Lock()
					(*z[i]).pools = newPools
					(*z[i]).zone = newZone
					h.lock.Unlock()
				}
			}
			failedRecords := make([]updateZoneRecordFailure, 0, 100)
			for _, f := range errorByRecordId {
				failedRecords = append(failedRecords, f)
				if len(failedRecords) >= 100 {
					break
				}
			}

			state := "ok"
			zoneErrorMessage := ""
			if zoneError != nil || len(failedRecords) > 0 {
				state = "error"
				zoneErrorMessage = zoneError.Error()
			}
			status := updateZoneStatusRequest{
				Resource: fmt.Sprintf("zone:%d", z[0].id),
				State:    state,
				Title:    fmt.Sprintf("CoreDNS %s %s", h.identifier, state),
				Data: updateZoneStatusMessage{
					Time:              time.Now().UTC().Format(time.RFC3339),
					CorednsIdentifier: h.identifier,
					Error:             zoneErrorMessage,
					FailedRecords:     failedRecords,
				},
			}
			statusJson, err := json.Marshal(status)
			if err != nil {
				panic(fmt.Errorf("failed to serialise status request: %v", err))
			}
			sendStatusError := retryable(h.maxRetries, func() (err error) {
				err = h.callApi(
					fmt.Sprintf("/v2/%s/platform/statuses", h.accountId),
					statusJson,
				)
				return
			})
			if sendStatusError != nil {
				log.Errorf("failed to send status: %v", sendStatusError)
			}
		}(zoneName, z)
	}
	wg.Wait()
	return nil
}

// Name implements the plugin.Handle interface.
func (re *DNSimple) Name() string { return "dnsimple" }
