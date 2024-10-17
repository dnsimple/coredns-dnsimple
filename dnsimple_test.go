package dnsimple

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/coredns/coredns/plugin/pkg/dnstest"
	"github.com/coredns/coredns/plugin/pkg/fall"
	"github.com/coredns/coredns/plugin/test"
	"github.com/coredns/coredns/request"
	"github.com/dnsimple/dnsimple-go/dnsimple"
	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type fakeDNSimpleClient struct {
	mock.Mock
}

func (m *fakeDNSimpleClient) getZone(ctx context.Context, accountID string, zoneName string) (*dnsimple.Zone, error) {
	return &dnsimple.Zone{
		Name: "example.org",
	}, nil
}

func (m *fakeDNSimpleClient) listZoneRecords(ctx context.Context, accountID string, zoneName string, maxRetries int) ([]dnsimple.ZoneRecord, error) {
	if zoneName == "example.bad" {
		return nil, errors.New("example.bad. zone is bad")
	}

	// CNAME train0.dnsim.pl -> train1.dnsim.pl.
	// CNAME train1.dnsim.pl -> train2.dnsim.pl.
	// ALIAS train2.dnsim.pl -> train3.dnsim.pl.
	// CNAME train3.dnsim.pl -> train4.dnsim.pl.
	// ALIAS train4.dnsim.pl -> example.com.

	fakeZoneRecords := []dnsimple.ZoneRecord{
		{
			Name:    "train0",
			Type:    "CNAME",
			Content: "train1.example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "train1",
			Type:    "CNAME",
			Content: "train2.example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "train2",
			Type:    "ALIAS",
			Content: "train3.example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "train3",
			Type:    "CNAME",
			Content: "train4.example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "train4",
			Type:    "ALIAS",
			Content: "example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "",
			Type:    "A",
			Content: "5.5.5.5",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "",
			Type:    "AAAA",
			Content: "2001:db8:85a3::8a2e:370:7335",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "external-alias",
			Type:    "ALIAS",
			Content: "lvh.me.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "internal-alias",
			Type:    "ALIAS",
			Content: "record.example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "internal-alias-a-only",
			Type:    "ALIAS",
			Content: "a-only.example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "internal-alias-aaaa-only",
			Type:    "ALIAS",
			Content: "aaaa-only.example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "internal-alias-no-data",
			Type:    "ALIAS",
			Content: "does-not-exist.example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "a-only",
			Type:    "A",
			Content: "2.2.2.2",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "record",
			Type:    "A",
			Content: "1.2.3.4",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "record",
			Type:    "A",
			Content: "1.2.3.5",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "aaaa-only",
			Type:    "AAAA",
			Content: "2001:db8:85a3::8a2e:370:7336",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "record",
			Type:    "AAAA",
			Content: "2001:db8:85a3::8a2e:370:7334",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "cname",
			Type:    "CNAME",
			Content: "record.example.org.",
			TTL:     300,
			Regions: []string{"global", "AMS"},
		},
		{
			Name:    "another-region",
			Type:    "A",
			Content: "4.3.2.1",
			TTL:     300,
			Regions: []string{"CDG"},
		},
		{
			Name:    "pool",
			Type:    "POOL",
			Content: "a.pool.example.com",
			TTL:     300,
			Regions: []string{"global"},
		},
		{
			Name:    "pool",
			Type:    "POOL",
			Content: "b.pool.example.com",
			TTL:     300,
			Regions: []string{"global"},
		},
		{
			Name:     "srv",
			Type:     "SRV",
			Content:  "5 5060 sipserver.example.com",
			Priority: 0,
			TTL:      300,
			Regions:  []string{"global"},
		},
		{
			Name:    "url",
			Type:    "URL",
			Content: "https://example.org",
			TTL:     300,
			Regions: []string{"global"},
		},
		{
			Name:    "",
			Type:    "SOA",
			Content: "ns1.dnsimple.com admin.dnsimple.com 1589573370 86400 7200 604800 300",
			TTL:     3600,
			Regions: []string{"global"},
		},
	}

	return fakeZoneRecords, nil
}

func (m *fakeDNSimpleClient) updateZoneStatus(accountID string, apiCaller DNSimpleApiCaller, maxRetries int, status updateZoneStatusRequest) (err error) {
	return nil
}

func TestDNSimple(t *testing.T) {
	ctx := context.Background()
	fakeClient := new(fakeDNSimpleClient)
	opts := Options{
		apiCaller: func(path string, body []byte) error { return nil },
	}

	r, err := New(ctx, fakeClient, map[string][]string{"example.org.": {"AMS"}}, opts)
	t.Logf("zoneNames: %v", r.zoneNames)
	t.Logf("zones: %v", r.zones)
	if err != nil {
		t.Fatalf("failed to create dnsimple: %v", err)
	}
	r.Fall = fall.Zero
	r.Fall.SetZonesFromArgs([]string{""})
	r.Next = test.HandlerFunc(func(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
		state := request.Request{W: w, Req: r}
		qname := state.Name()
		m := new(dns.Msg)
		rcode := dns.RcodeServerFailure
		if qname == "example.gov." {
			m.SetReply(r)
			rr, err := dns.NewRR("example.gov.  300 IN  A   2.4.6.8")
			if err != nil {
				t.Fatalf("failed to create resource record: %v", err)
			}
			m.Answer = []dns.RR{rr}

			m.Authoritative = true
			rcode = dns.RcodeSuccess
		}

		m.SetRcode(r, rcode)
		w.WriteMsg(m)
		return rcode, nil
	})
	err = r.Run(ctx)
	if err != nil {
		t.Fatalf("failed to initialize dnsimple: %v", err)
	}

	tests := []struct {
		qname        string
		qtype        uint16
		wantRetCode  int
		wantAnswer   []string // ownernames for the records in the additional section.
		wantPool     []string
		wantMsgRCode int
		wantNS       []string
		expectedErr  error
	}{
		// example.org NODATA.
		{
			qname:      "example.org",
			qtype:      dns.TypeTXT,
			wantAnswer: []string{},
			wantNS:     []string{"example.org.	3600	IN	SOA	ns1.dnsimple.com. admin.dnsimple.com. 1589573370 86400 7200 604800 300"},
		},
		// record.example.org A found - success.
		{
			qname: "record.example.org",
			qtype: dns.TypeA,
			wantAnswer: []string{
				"record.example.org.	300	IN	A	1.2.3.4",
				"record.example.org.	300	IN	A	1.2.3.5",
			},
		},
		// record.example.org AAAA found - success.
		{
			qname:      "record.example.org",
			qtype:      dns.TypeAAAA,
			wantAnswer: []string{"record.example.org.	300	IN	AAAA	2001:db8:85a3::8a2e:370:7334"},
		},
		// www.example.org points to example.org CNAME.
		// Query must return both CNAME and A records.
		{
			qname: "cname.example.org",
			qtype: dns.TypeA,
			wantAnswer: []string{
				"cname.example.org.	300	IN	CNAME	record.example.org.",
				"record.example.org.	300	IN	A	1.2.3.4",
				"record.example.org.	300	IN	A	1.2.3.5",
			},
		},
		// Region not configured. Return SOA record.
		{
			qname:        "another-region.example.org",
			qtype:        dns.TypeA,
			wantRetCode:  dns.RcodeSuccess,
			wantMsgRCode: dns.RcodeNameError,
			wantNS:       []string{"example.org.	3600	IN	SOA	ns1.dnsimple.com. admin.dnsimple.com. 1589573370 86400 7200 604800 300"},
		},
		// POOL record.
		{
			qname:       "pool.example.org",
			qtype:       dns.TypeCNAME,
			wantRetCode: dns.RcodeSuccess,
			wantPool: []string{
				"pool.example.org.	300	IN	CNAME	a.pool.example.com.",
				"pool.example.org.	300	IN	CNAME	b.pool.example.com.",
			},
		},
		// SRV record.
		{
			qname: "srv.example.org",
			qtype: dns.TypeSRV,
			wantAnswer: []string{
				"srv.example.org.	300	IN	SRV	0 5 5060 sipserver.example.com.",
			},
		},
		// URL record.
		{
			qname: "url.example.org",
			qtype: dns.TypeA,
			wantAnswer: []string{
				"url.example.org.	300	IN	A	3.131.150.69",
				"url.example.org.	300	IN	A	3.21.30.3",
				"url.example.org.	300	IN	A	3.16.5.4",
			},
		},
		// ALIAS record with internal target. A found - success.
		{
			qname:       "internal-alias.example.org",
			qtype:       dns.TypeA,
			wantRetCode: dns.RcodeSuccess,
			wantAnswer: []string{
				"internal-alias.example.org.	300	IN	A	1.2.3.4",
				"internal-alias.example.org.	300	IN	A	1.2.3.5",
			},
		},
		// ALIAS record with internal target. AAAA found - success.
		{
			qname:       "internal-alias.example.org",
			qtype:       dns.TypeAAAA,
			wantRetCode: dns.RcodeSuccess,
			wantAnswer:  []string{"internal-alias.example.org.	300	IN	AAAA	2001:db8:85a3::8a2e:370:7334"},
		},
		// ALIAS record with internal target. TXT - NODATA.
		{
			qname:       "internal-alias.example.org",
			qtype:       dns.TypeTXT,
			wantRetCode: dns.RcodeSuccess,
			wantAnswer:  []string{},
			wantNS:      []string{"example.org.	3600	IN	SOA	ns1.dnsimple.com. admin.dnsimple.com. 1589573370 86400 7200 604800 300"},
		},
		// ALIAS record with internal target. AAAA - NODATA.
		{
			qname:       "internal-alias-a-only.example.org",
			qtype:       dns.TypeAAAA,
			wantRetCode: dns.RcodeSuccess,
			wantAnswer:  []string{},
			wantNS:      []string{"example.org.	3600	IN	SOA	ns1.dnsimple.com. admin.dnsimple.com. 1589573370 86400 7200 604800 300"},
		},
		// ALIAS record with internal target. A - NODATA.
		{
			qname:       "internal-alias-aaaa-only.example.org",
			qtype:       dns.TypeA,
			wantRetCode: dns.RcodeSuccess,
			wantAnswer:  []string{},
			wantNS:      []string{"example.org.	3600	IN	SOA	ns1.dnsimple.com. admin.dnsimple.com. 1589573370 86400 7200 604800 300"},
		},
		// ALIAS record with internal target. AAAA - NODATA.
		{
			qname:       "internal-alias-no-data.example.org",
			qtype:       dns.TypeAAAA,
			wantRetCode: dns.RcodeSuccess,
			wantAnswer:  []string{},
			wantNS:      []string{"example.org.	3600	IN	SOA	ns1.dnsimple.com. admin.dnsimple.com. 1589573370 86400 7200 604800 300"},
		},
		// ALIAS record with internal target. A - NODATA.
		{
			qname:       "internal-alias-no-data.example.org",
			qtype:       dns.TypeA,
			wantRetCode: dns.RcodeSuccess,
			wantAnswer:  []string{},
			wantNS:      []string{"example.org.	3600	IN	SOA	ns1.dnsimple.com. admin.dnsimple.com. 1589573370 86400 7200 604800 300"},
		},
		// ALIAS record with external target. A found - success.
		{
			qname: "external-alias.example.org",
			qtype: dns.TypeA,
			wantAnswer: []string{
				"external-alias.example.org.	300	IN	A	127.0.0.1",
			},
		},
		// CNAME with ALIAS and CNAME targets inside zone. A found - success.
		{
			qname: "train0.example.org",
			qtype: dns.TypeA,
			wantAnswer: []string{
				"train0.example.org.	300	IN	CNAME	train1.example.org.",
				"train1.example.org.	300	IN	CNAME	train2.example.org.",
				"train2.example.org.	300	IN	A	5.5.5.5",
			},
		},
		// CNAME with ALIAS and CNAME targets inside zone. AAAA found - success.
		{
			qname: "train0.example.org",
			qtype: dns.TypeAAAA,
			wantAnswer: []string{
				"train0.example.org.	300	IN	CNAME	train1.example.org.",
				"train1.example.org.	300	IN	CNAME	train2.example.org.",
				"train2.example.org.	300	IN	AAAA	2001:db8:85a3::8a2e:370:7335",
			},
		},
	}

	for ti, tc := range tests {
		req := new(dns.Msg)
		req.SetQuestion(dns.Fqdn(tc.qname), tc.qtype)

		rec := dnstest.NewRecorder(&test.ResponseWriter{})
		code, err := r.ServeDNS(ctx, rec, req)

		if tc.expectedErr != nil {
			assert.Equal(t, tc.expectedErr, err, "Test %d: Expected error %v, but got %v", ti, tc.expectedErr, err)
		}
		assert.Nilf(t, err, "Test %d: Unexpected error %v", ti, err)

		// Return code 0 means NODATA.
		if !assert.Equal(
			t,
			int(tc.wantRetCode),
			code,
			"Test %d: Expected returned status code %s, but got %s.",
			ti,
			dns.RcodeToString[tc.wantRetCode],
			dns.RcodeToString[code],
		) {
			continue
		}

		// Handle POOL tests.
		if tc.wantPool != nil {
			matchFound := false
			for i, gotAnswer := range rec.Msg.Answer {
				for _, expectedAnswer := range tc.wantPool {
					if gotAnswer.String() == expectedAnswer {
						matchFound = true
						break
					}
				}
				if !matchFound {
					assert.Failf(t, "Test %d: Unexpected answer.\nWant: one of %v\nGot:\n\t%s", tc.wantPool[i], tc.wantPool, gotAnswer)
				}
			}
		} else {
			msgAnswers := make([]string, 0)
			assert.NotNilf(t, rec.Msg, "Test %d: Unexpected Msg was nil.", ti)

			for _, a := range rec.Msg.Answer {
				msgAnswers = append(msgAnswers, a.String())
			}
			assert.ElementsMatchf(
				t,
				msgAnswers,
				tc.wantAnswer,
				"Test for (%s, %s): Unexpected answer.", tc.qname, dns.TypeToString[tc.qtype])
		}

		if !assert.Len(t, rec.Msg.Ns, len(tc.wantNS), "Test for (%s, %s): Unexpected NS number. Want: %d, got: %d", tc.qname, dns.TypeToString[tc.qtype], len(tc.wantNS), len(rec.Msg.Ns)) {
			continue
		}
		for i, ns := range rec.Msg.Ns {
			got, ok := ns.(*dns.SOA)
			assert.True(t, ok, "Test %d: Unexpected NS type. Want: SOA, got: %v", ti, reflect.TypeOf(got))
			assert.Equal(t, tc.wantNS[i], got.String(), "Test %d: Unexpected NS.\nWant: %v\nGot: %v", ti, tc.wantNS[i], got)
		}
	}
}
