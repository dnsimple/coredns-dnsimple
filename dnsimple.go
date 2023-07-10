package dnsimple

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
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
	Fall fall.F
	Next plugin.Handler

	accountId   string
	client      dnsimpleService
	identifier  string
	lock        sync.RWMutex
	maxRetries  int
	refresh     time.Duration
	upstream    *upstream.Upstream
	zoneNames   []string // set using the zone object fqdn
	zones       zones
	dnsResolver *net.Resolver
	apiCaller   DNSimpleApiCaller
}

type zone struct {
	id      int64
	fqdn    string // fqdn containing the trailing dot
	name    string
	aliases map[string][]string
	pools   map[string][]string
	region  string

	zone *file.Zone
}

type zones map[string][]*zone

func New(ctx context.Context, client dnsimpleService, keys map[string][]string, opts Options) (*DNSimple, error) {
	zones := make(map[string][]*zone, len(keys))
	zoneNames := make([]string, 0, len(keys))

	for fqdn, regions := range keys {
		fqdn = dns.Fqdn(fqdn)
		name := strings.TrimSuffix(fqdn, ".")

		// Check if the zone exists.
		// Our API does not expect the zone name to end with a dot.
		res, err := client.getZone(ctx, opts.accountId, name)
		if err != nil {
			return nil, err
		}
		if _, ok := zones[fqdn]; !ok {
			zoneNames = append(zoneNames, fqdn)
		}
		for _, region := range regions {
			zones[fqdn] = append(zones[fqdn], &zone{id: res.ID, fqdn: fqdn, name: name, region: region, zone: file.NewZone(fqdn, "")})
		}
	}
	dnsResolver := net.DefaultResolver
	if opts.customDnsResolver != "" {
		dnsResolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{
					Timeout: time.Second * 10,
				}
				return d.DialContext(ctx, network, opts.customDnsResolver)
			},
		}
	}
	return &DNSimple{
		accountId:   opts.accountId,
		apiCaller:   opts.apiCaller,
		client:      client,
		dnsResolver: dnsResolver,
		identifier:  opts.identifier,
		maxRetries:  opts.maxRetries,
		refresh:     opts.refresh,
		upstream:    upstream.New(),
		zoneNames:   zoneNames,
		zones:       zones,
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
		maybeInterceptAliasResponse(
			h.dnsResolver,
			regionalZone,
			r.Question[0].Qtype,
			&msg.Answer,
			&result,
		)
		maybeInterceptPoolResponse(h.dnsResolver, regionalZone, &msg.Answer)

		// Take the answer if it's non-empty OR if there is another
		// record type that exists for this name (NODATA).
		if len(msg.Answer) != 0 || result == file.NoData || result == file.ServerFailure {
			break
		}
	}

	if len(msg.Answer) == 0 && result != file.NoData && result != file.ServerFailure && h.Fall.Through(qname) {
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

// How ALIAS records work in our plugin:
// Let's take an example zone with the following records:
//
//	A      my.com    255.255.255.255
//	A      my.com    1.1.1.1
//	ALIAS  my.com    a1.com
//	ALIAS  my.com    a2.com
//
// We insert the following records into our `file` plugin zone:
//
//	A      my.com    255.255.255.255
//	A      my.com    1.1.1.1
//	A      my.com    255.255.255.255
//
// The last record is a dummy record representing **all** ALIAS records with this name.
// Its value is arbitrary and doesn't matter, but a value like 255.255.255.255 can help distinguish it when debugging.
// Notice that, in this example, the dummy value matches the first record which is an actual A record; this is to demonstrate that the dummy value can conflict and duplicate with another real A record's value and still work OK. The `file` plugin will accept and return duplicate A (name, value) record pairs.
//
// We also collect all ALIAS record targets for a name into the `aliases[name][]target` map.
// Upon lookup using the `file` plugin, we iterate through all answers and find records matching these criteria:
// - It's an A record.
// - Its value is 255.255.255.255 (our value for ALIAS dummy records).
// - Its name exists in the `aliases[name]` map.
// If so, we need to replace that *one* A record with *zero or more* A/AAAA records by resolving *all* ALIAS targets of that name.
// To handle the case where a real A record also exists with the same name and a value of `255.255.255.255`, we use a hash set to track which names we've already performed the above logic for, and skip it if we see it again.
//
// Why does this work?
// Using the previous examples, consider that when a query comes in for `my.com`, the `file` plugin will return:
//
//	A      my.com    255.255.255.255
//	A      my.com    1.1.1.1
//	A      my.com    255.255.255.255
//
// This is because the `file` plugin, as mentioned already, accepts and returns duplicates.
// We know that if we see an A record with a value of 255.255.255.255 and its name exists in the `aliases` map, there are two possibilities:
// - It's a dummy record for ALIAS, and we need to replace it with all resolved ALIAS target records. For example, if a zone has two ALIAS records for `my.com`, one pointing to `a1.com` and another pointing to `a2.com`, it's expected that resolving `my.com` returns all A/AAAA records for both `a1.com` *and* `a2.com`.
// - It's a real A record whose actual value is 255.255.255.255 and coincidentally exists alongside one or more ALIAS records with the same name. This is handled by the hash set and why it's necessary.
func maybeInterceptAliasResponse(dnsResolver *net.Resolver, zone *zone, qtype uint16, answers *[]dns.RR, result *file.Result) {
	newAnswers := make([]dns.RR, 0)
	alreadyResolvedAliasesFor := make(map[string]bool)
	for _, ans := range *answers {
		switch a := ans.(type) {
		case *dns.A:
			// `zone.aliases[a.Hdr.Name]` => One or more ALIAS records exist for this name.
			// `alreadyResolvedAliasesFor[a.Hdr.Name]` => We haven't already handled ALIAS records for this name; important if there are both A and ALIAS records for the same name.
			// `a.A.Equal(net.IPv4(255, 255, 255, 255))` => This A record represents the special marker for our dummy A records.
			targets, ok := zone.aliases[a.Hdr.Name]
			if !ok {
				break
			}

			if alreadyResolvedAliasesFor[a.Hdr.Name] {
				break
			}

			if !a.A.Equal(net.IPv4(255, 255, 255, 255)) {
				break
			}

			log.Debugf("Resolving ALIAS targets for %s", a.Hdr.Name)

			alreadyResolvedAliasesFor[a.Hdr.Name] = true
			// For each ALIAS record for this name, resolve their targets.
			for _, tgt := range targets {
				var ipType string
				// 1 is A, 28 is AAAA; see https://en.wikipedia.org/wiki/List_of_DNS_record_types.
				// If a user requested A, we should only resolve A, and same for AAAA.
				if qtype == dns.TypeA {
					ipType = "ip4"
				} else {
					ipType = "ip6"
				}
				// Try look up 3 times before giving up. Don't delay for too long as DNS queries should be fast.
				var ips []net.IP
				var err error
				for i := 0; i < 3; i++ {
					ips, err = dnsResolver.LookupIP(context.Background(), ipType, tgt)
					if err != nil {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				// If even one ALIAS fails, we should not simply ignore/skip, and instead fail the entire request.
				if err != nil {
					log.Errorf("Failed to resolve ALIAS target %s: %s", tgt, err)

					*answers = make([]dns.RR, 0)
					*result = file.ServerFailure
					return
				}
				// Transform resolve results into CoreDNS answers.
				for _, res := range ips {
					hdr := dns.RR_Header{
						Name:  a.Hdr.Name,
						Class: dns.ClassINET,
						Ttl:   a.Hdr.Ttl,
					}
					if ip4 := res.To4(); ip4 != nil {
						r := new(dns.A)
						r.Hdr = hdr
						r.Hdr.Rrtype = dns.TypeA
						r.A = ip4
						newAnswers = append(newAnswers, r)
					} else {
						r := new(dns.AAAA)
						r.Hdr = hdr
						r.Hdr.Rrtype = dns.TypeAAAA
						r.AAAA = res
						newAnswers = append(newAnswers, r)
					}
				}
			}
			continue
		}
		newAnswers = append(newAnswers, ans)
	}
	*answers = newAnswers
}

func maybeInterceptPoolResponse(dnsResolver *net.Resolver, zone *zone, answers *[]dns.RR) {
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
	Message  string                  `json:"message"`
	Data     updateZoneStatusMessage `json:"data"`
}

func updateZoneFromRecords(zoneName string, records []dnsimple.ZoneRecord, zoneRegion string, aliases map[string][]string, pools map[string][]string, urlSvcIps []net.IP, zone *file.Zone, dnsResolver *net.Resolver) []updateZoneRecordFailure {
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

		type rawRecord struct {
			typ     string
			content string
		}
		rawRecords := make([]rawRecord, 0)

		if rec.Type == "ALIAS" {
			// See the comment for the `maybeInterceptAliasResponse` function for details on how this works.
			isFirst := false
			if aliases[fqdn] == nil {
				aliases[fqdn] = make([]string, 0)
				isFirst = true
			}
			aliases[fqdn] = append(aliases[fqdn], rec.Content)
			if !isFirst {
				// We have already inserted a record for this ALIAS name, and we must not add any more.
				continue
			}
			// This is a dummy record to represent the dummy record, so we can identify it when we intercept the response from the `file` plugin.
			rawRecords = append(rawRecords, rawRecord{
				typ:     "A",
				content: "255.255.255.255",
			})
		} else if rec.Type == "MX" {
			// MX records have a priority and a content field.
			rawRecords = append(rawRecords, rawRecord{
				typ:     "MX",
				content: fmt.Sprintf("%d %s", rec.Priority, rec.Content),
			})
		} else if rec.Type == "POOL" {
			isFirst := false
			if pools[fqdn] == nil {
				pools[fqdn] = make([]string, 0)
				isFirst = true
			}
			pools[fqdn] = append(pools[fqdn], rec.Content)
			if !isFirst {
				// We have already inserted a record for this POOL name, there's no point to adding more.
				// As an interesting side note, the file plugin does not crash on multiple CNAME records with
				// the same name, and will simply respond with all CNAMEs if matched, which doesn't appear to be spec compliant.
				continue
			}
			rawRecords = append(rawRecords, rawRecord{
				typ:     "CNAME",
				content: rec.Content,
			})
		} else if rec.Type == "SRV" {
			// SRV records have a priority and a content field.
			rawRecords = append(rawRecords, rawRecord{
				typ:     "SRV",
				content: fmt.Sprintf("%d %s", rec.Priority, rec.Content),
			})
		} else if rec.Type == "URL" {
			for _, res := range urlSvcIps {
				typ := "AAAA"
				if res.To4() != nil {
					typ = "A"
				}
				rawRecords = append(rawRecords, rawRecord{
					typ:     typ,
					content: res.String(),
				})
			}
		} else {
			rawRecords = append(rawRecords, rawRecord{
				typ:     rec.Type,
				content: rec.Content,
			})
		}

		for _, raw := range rawRecords {
			// Assemble RFC 1035 conforming record to pass into DNS scanner.
			rfc1035 := fmt.Sprintf("%s %d IN %s %s", fqdn, rec.TTL, raw.typ, raw.content)
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
	}
	return failures
}

func (h *DNSimple) updateZones(ctx context.Context) error {
	log.Debugf("starting update zones for dnsimple with identifier %s", h.identifier)
	var wg sync.WaitGroup

	urlSvcIps, err := net.LookupIP("coredns-url-record-target.dns.solutions")
	if err != nil {
		log.Errorf("failed to fetch URL record target: %v", err)
	}

	for zoneName, z := range h.zones {
		wg.Add(1)
		go func(zoneName string, z []*zone) {
			defer wg.Done()
			var zoneError error = nil

			var zoneRecords []dnsimple.ZoneRecord

			zoneRecords, zoneError = h.client.listZoneRecords(ctx, h.accountId, zoneName, h.maxRetries)

			errorByRecordId := make(map[int64]updateZoneRecordFailure)
			if zoneError == nil {
				for i, regionalZone := range z {
					newZone := file.NewZone(zoneName, "")
					newZone.Upstream = h.upstream
					newAliases := make(map[string][]string)
					newPools := make(map[string][]string)

					// Deduplicate errors by record ID, as otherwise we'll duplicate errors for all records that aren't regional. Note that some regional records may have errors, so we cannot just take the first region's errors only.
					failedRecords := updateZoneFromRecords(zoneName, zoneRecords, regionalZone.region, newAliases, newPools, urlSvcIps, newZone, h.dnsResolver)
					for _, f := range failedRecords {
						errorByRecordId[f.Record.ID] = f
					}

					h.lock.Lock()
					(*z[i]).aliases = newAliases
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
			if zoneError != nil {
				state = "error"
				zoneErrorMessage = zoneError.Error()
			}
			if len(failedRecords) > 0 {
				state = "error"
				zoneErrorMessage = "Failure when synching zone records"
			}
			status := updateZoneStatusRequest{
				Resource: fmt.Sprintf("zone:%d", z[0].id),
				State:    state,
				Title:    fmt.Sprintf("CoreDNS %s %s", h.identifier, state),
				Message:  zoneErrorMessage,
				Data: updateZoneStatusMessage{
					Time:              time.Now().UTC().Format(time.RFC3339),
					CorednsIdentifier: h.identifier,
					Error:             zoneErrorMessage,
					FailedRecords:     failedRecords,
				},
			}
			err := h.client.updateZoneStatus(h.accountId, h.apiCaller, h.maxRetries, status)
			if err != nil {
				log.Errorf("failed to update zone status: %v", err)
			}
		}(zoneName, z)
	}
	wg.Wait()
	return nil
}

// Name implements the plugin.Handle interface.
func (re *DNSimple) Name() string { return "dnsimple" }
