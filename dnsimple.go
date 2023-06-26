package dnsimple

import (
	"context"
	"fmt"
	"math/rand"
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

// DNSimple is a plugin that returns RR from DNSimple.
type DNSimple struct {
	Next plugin.Handler
	Fall fall.F

	// Each zone name contains a trailing dot.
	zoneNames  []string
	client     dnsimpleAPIService
	accountId  string
	identifier string
	upstream   *upstream.Upstream
	refresh    time.Duration
	maxRetries int

	lock  sync.RWMutex
	zones zones
}

type zone struct {
	// This contains the trailing dot.
	name   string
	pools  map[string][]string
	region string
	zone   *file.Zone
}

type zones map[string][]*zone

func New(ctx context.Context, accountId string, client dnsimpleAPIService, identifier string, keys map[string][]string, refresh time.Duration, maxRetries int) (*DNSimple, error) {
	zones := make(map[string][]*zone, len(keys))
	zoneNames := make([]string, 0, len(keys))

	for zoneName, hostedZoneRegions := range keys {
		// Check if the zone exists.
		// Our API does not expect the zone name to end with a dot.
		err := client.zoneExists(ctx, accountId, zoneName)
		if err != nil {
			return nil, err
		}
		if _, ok := zones[zoneName]; !ok {
			zoneNames = append(zoneNames, zoneName)
		}
		for _, hostedZoneRegion := range hostedZoneRegions {
			zones[zoneName] = append(zones[zoneName], &zone{name: zoneName, region: hostedZoneRegion, zone: file.NewZone(zoneName, "")})
		}
	}
	return &DNSimple{
		accountId:  accountId,
		client:     client,
		identifier: identifier,
		refresh:    refresh,
		upstream:   upstream.New(),
		zoneNames:  zoneNames,
		zones:      zones,
		maxRetries: maxRetries,
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

func updateZoneFromRecords(zoneName string, records []dnsimple.ZoneRecord, zoneRegion string, pools map[string][]string, zone *file.Zone) error {
	log.Debugf("updating zone %s with region %s", zoneName, zoneRegion)
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
			return fmt.Errorf("failed to parse resource record: %v", err)
		}

		log.Debugf("inserting record %s", rfc1035)
		zone.Insert(rr)
	}
	return nil
}

func (h *DNSimple) updateZones(ctx context.Context) error {
	log.Debugf("starting update zones for dnsimple with identifier %s", h.identifier)
	errc := make(chan error)
	defer close(errc)
	for zoneName, z := range h.zones {
		go func(zoneName string, z []*zone) {
			var err error
			defer func() {
				errc <- err
			}()

			var response *dnsimple.ZoneRecordsResponse

			options := &dnsimple.ZoneRecordListOptions{}
			options.PerPage = dnsimple.Int(100)

			response, err = h.client.listZoneRecords(ctx, h.accountId, zoneName, options, h.maxRetries)
			if err != nil {
				err = fmt.Errorf("failed to list resource records for %v from dnsimple: %v", zoneName, err)
				return
			}

			for i, regionalZone := range z {
				newZone := file.NewZone(zoneName, "")
				newZone.Upstream = h.upstream
				newPools := make(map[string][]string, 16)

				if err := updateZoneFromRecords(zoneName, response.Data, regionalZone.region, newPools, newZone); err != nil {
					// Maybe unsupported record type. Log and carry on.
					log.Warningf("failed to process resource records: %v", err)
				}

				h.lock.Lock()
				(*z[i]).pools = newPools
				(*z[i]).zone = newZone
				h.lock.Unlock()
			}
		}(zoneName, z)
	}
	// Collect any errors and wait for all updates.
	var errs []string
	for i := 0; i < len(h.zones); i++ {
		err := <-errc
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) != 0 {
		return fmt.Errorf("errors updating zones: %v", errs)
	}
	return nil
}

// Name implements the plugin.Handle interface.
func (re *DNSimple) Name() string { return "dnsimple" }
