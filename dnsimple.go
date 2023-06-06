package dnsimple

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
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
	zoneNames []string
	client    *dnsimple.Client
	accountId string
	upstream  *upstream.Upstream
	refresh   time.Duration

	lock  sync.RWMutex
	zones Zones
}

type Zone struct {
	// This contains the trailing dot.
	name  string
	zone  *file.Zone
	pools map[string][]string
}

type Zones map[string]*Zone

func New(ctx context.Context, accountId string, client *dnsimple.Client, keys []string, refresh time.Duration) (*DNSimple, error) {
	zones := make(map[string]*Zone, len(keys))
	zoneNames := make([]string, 0, len(keys))
	for _, zoneName := range keys {
		// Check if the zone exists.
		// Our API does not expect the zone name to end with a dot.
		_, err := client.Zones.GetZone(ctx, accountId, strings.TrimSuffix(zoneName, "."))
		if err != nil {
			return nil, err
		}
		if _, ok := zones[zoneName]; !ok {
			zoneNames = append(zoneNames, zoneName)
		}
		zones[zoneName] = &Zone{name: zoneName, zone: file.NewZone(zoneName, "")}
	}
	return &DNSimple{
		accountId: accountId,
		client:    client,
		refresh:   refresh,
		upstream:  upstream.New(),
		zoneNames: zoneNames,
		zones:     zones,
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

func maybeInterceptPoolResponse(zone *Zone, answers *[]dns.RR) {
	var qname string
	var ttl uint32
	var pool []string
	for _, ans := range *answers {
		switch cname := ans.(type) {
		case *dns.CNAME:
			if ent, ok := zone.pools[cname.Hdr.Name]; ok {
				pool = ent
			} else {
				return
			}
			qname = cname.Hdr.Name
			ttl = cname.Hdr.Ttl
		}
	}
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

// ServeDNS implements the plugin.Handler.ServeDNS.
func (h *DNSimple) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	state := request.Request{W: w, Req: r}
	query := state.Name()

	zoneName := plugin.Zones(h.zoneNames).Matches(query)
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
	h.lock.RLock()
	msg.Answer, msg.Ns, msg.Extra, result = zone.zone.Lookup(ctx, state, query)
	h.lock.RUnlock()
	maybeInterceptPoolResponse(zone, &msg.Answer)

	if len(msg.Answer) == 0 && result != file.NoData && h.Fall.Through(query) {
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

func updateZoneFromRecords(records []dnsimple.ZoneRecord, zone *Zone) error {
	for _, rec := range records {
		var fqdn string
		if rec.Name == "" {
			fqdn = zone.name
		} else {
			fqdn = fmt.Sprintf("%s.%s", rec.Name, zone.name)
		}

		if rec.Type == "MX" {
			// MX records have a priority and a content field.
			rec.Content = fmt.Sprintf("%d %s", rec.Priority, rec.Content)
		}

		if rec.Type == "POOL" {
			rec.Type = "CNAME"
			if zone.pools[fqdn] == nil {
				zone.pools[fqdn] = make([]string, 0)
			}
			zone.pools[fqdn] = append(zone.pools[fqdn], rec.Content)
		}

		// Assemble RFC 1035 conforming record to pass into DNS scanner.
		rfc1035 := fmt.Sprintf("%s %d IN %s %s", fqdn, rec.TTL, rec.Type, rec.Content)
		rr, err := dns.NewRR(rfc1035)
		if err != nil {
			return fmt.Errorf("failed to parse resource record: %v", err)
		}

		log.Debugf("inserting record %s", rfc1035)
		zone.zone.Insert(rr)
	}
	return nil
}

func (h *DNSimple) updateZones(ctx context.Context) error {
	errors := make(chan error)
	defer close(errors)
	for _, zone := range h.zones {
		go func(zone *Zone) {
			var err error
			defer func() {
				errors <- err
			}()

			newZone := Zone{name: zone.name, zone: file.NewZone(zone.name, ""), pools: make(map[string][]string, 0)}
			newZone.zone.Upstream = h.upstream

			options := &dnsimple.ZoneRecordListOptions{}
			options.PerPage = dnsimple.Int(100)
			// Fetch all records for the zone.
			for {
				// Our API does not expect the zone name to end with a dot.
				response, listErr := h.client.Zones.ListRecords(ctx, h.accountId, strings.TrimSuffix(zone.name, "."), options)
				if listErr != nil {
					err = fmt.Errorf("failed to list records for zone %s: %v", zone.name, listErr)
					return
				}

				err = updateZoneFromRecords(response.Data, &newZone)
				if err != nil {
					return
				}

				if response.Pagination.CurrentPage >= response.Pagination.TotalPages {
					break
				}
				options.Page = dnsimple.Int(response.Pagination.CurrentPage + 1)
			}
			h.lock.Lock()
			*zone = newZone
			h.lock.Unlock()
		}(zone)
	}
	// Collect any errors and wait for all updates.
	var errMsgs []string
	for i := 0; i < len(h.zones); i++ {
		err := <-errors
		if err != nil {
			errMsgs = append(errMsgs, err.Error())
		}
	}
	if len(errMsgs) != 0 {
		return fmt.Errorf("errors encountered while updating zones: %v", errMsgs)
	}
	return nil
}

// Name implements the plugin.Handle interface.
func (re *DNSimple) Name() string { return "dnsimple" }
