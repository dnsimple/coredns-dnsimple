package dnsimple

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/coredns/coredns/plugin/pkg/dnstest"
	"github.com/coredns/coredns/plugin/pkg/fall"
	"github.com/coredns/coredns/plugin/test"
	"github.com/coredns/coredns/request"
	"github.com/dnsimple/dnsimple-go/dnsimple"
	"github.com/miekg/dns"
)

type fakeDNSimpleClient struct {
	*dnsimple.Client
}

func (c fakeDNSimpleClient) listZoneRecords(ctx context.Context, accountID string, zoneName string, options *dnsimple.ZoneRecordListOptions, maxRetries int) (*dnsimple.ZoneRecordsResponse, error) {
	if zoneName == "example.bad." {
		return nil, errors.New("example.bad. zone is bad")
	}

	fakeZoneRecordsResponse := &dnsimple.ZoneRecordsResponse{
		Data: []dnsimple.ZoneRecord{
			{
				Name:    "",
				Type:    "A",
				Content: "1.2.3.4",
				TTL:     300,
				Regions: []string{"global", "AMS"},
			},
			{
				Name:    "",
				Type:    "AAAA",
				Content: "2001:db8:85a3::8a2e:370:7334",
				TTL:     300,
				Regions: []string{"global", "AMS"},
			},
			{
				Name:    "www",
				Type:    "CNAME",
				Content: "example.org.",
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
				Name:    "",
				Type:    "SOA",
				Content: "ns1.dnsimple.com admin.dnsimple.com 1589573370 86400 7200 604800 300",
				TTL:     3600,
				Regions: []string{"global"},
			},
		},
	}

	return fakeZoneRecordsResponse, nil
}

func (c fakeDNSimpleClient) zoneExists(ctx context.Context, accountID string, zoneName string) error {
	return nil
}

func TestDNSimple(t *testing.T) {
	ctx := context.Background()

	r, err := New(ctx, "testAccountId", fakeDNSimpleClient{}, "testIdentifier", map[string][]string{"example.bad.": {""}}, time.Duration(1)*time.Minute, 3)
	if err != nil {
		t.Fatalf("failed to create dnsimple: %v", err)
	}
	if err = r.Run(ctx); err == nil {
		t.Fatalf("expected errors for zone bad.")
	}

	r, err = New(ctx, "testAccountId", fakeDNSimpleClient{}, "testIdentifier", map[string][]string{"example.org.": {"AMS"}}, time.Duration(1)*time.Minute, 3)
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
		// 0. example.org A found - success.
		{
			qname:      "example.org",
			qtype:      dns.TypeA,
			wantAnswer: []string{"example.org.	300	IN	A	1.2.3.4"},
		},
		// 1. example.org AAAA found - success.
		{
			qname:      "example.org",
			qtype:      dns.TypeAAAA,
			wantAnswer: []string{"example.org.	300	IN	AAAA	2001:db8:85a3::8a2e:370:7334"},
		},
		// 2. www.example.org points to example.org CNAME.
		// Query must return both CNAME and A records.
		{
			qname: "www.example.org",
			qtype: dns.TypeA,
			wantAnswer: []string{
				"www.example.org.	300	IN	CNAME	example.org.",
				"example.org.	300	IN	A	1.2.3.4",
			},
		},
		// 3. Region not configured. Return SOA record.
		{
			qname:        "another-region.example.org",
			qtype:        dns.TypeA,
			wantRetCode:  dns.RcodeSuccess,
			wantMsgRCode: dns.RcodeNameError,
			wantNS:       []string{"example.org.	3600	IN	SOA	ns1.dnsimple.com. admin.dnsimple.com. 1589573370 86400 7200 604800 300"},
		},
		// 4. POOL record.
		{
			qname:       "pool.example.org",
			qtype:       dns.TypeCNAME,
			wantRetCode: dns.RcodeSuccess,
			wantPool: []string{
				"pool.example.org.	300	IN	CNAME	a.pool.example.com.",
				"pool.example.org.	300	IN	CNAME	b.pool.example.com.",
			},
		},
	}

	for ti, tc := range tests {
		req := new(dns.Msg)
		req.SetQuestion(dns.Fqdn(tc.qname), tc.qtype)

		rec := dnstest.NewRecorder(&test.ResponseWriter{})
		code, err := r.ServeDNS(ctx, rec, req)

		if err != tc.expectedErr {
			t.Fatalf("Test %d: Expected error %v, but got %v", ti, tc.expectedErr, err)
		}
		if code != int(tc.wantRetCode) {
			t.Fatalf("Test %d: Expected returned status code %s, but got %s", ti, dns.RcodeToString[tc.wantRetCode], dns.RcodeToString[code])
		}

		if tc.wantMsgRCode != rec.Msg.Rcode {
			t.Errorf("Test %d: Unexpected msg status code. Want: %s, got: %s", ti, dns.RcodeToString[tc.wantMsgRCode], dns.RcodeToString[rec.Msg.Rcode])
		}

		// Handle POOL tests.
		if tc.wantPool != nil {
			matchFound := false
			for _, gotAnswer := range rec.Msg.Answer {
				for _, expectedAnswer := range tc.wantPool {
					if gotAnswer.String() == expectedAnswer {
						matchFound = true
						break
					}
				}
				if !matchFound {
					t.Errorf("Test %d: Unexpected answer.\nWant: one of %v\nGot:\n\t%s", ti, tc.wantPool, gotAnswer)
				}
			}
		} else if len(tc.wantAnswer) != len(rec.Msg.Answer) {
			t.Errorf("Test %d: Unexpected number of Answers. Want: %d, got: %d", ti, len(tc.wantAnswer), len(rec.Msg.Answer))
		} else {
			for i, gotAnswer := range rec.Msg.Answer {
				if gotAnswer.String() != tc.wantAnswer[i] {
					t.Errorf("Test %d: Unexpected answer.\nWant:\n\t%s\nGot:\n\t%s", ti, tc.wantAnswer[i], gotAnswer)
				}
			}
		}

		if len(tc.wantNS) != len(rec.Msg.Ns) {
			t.Errorf("Test %d: Unexpected NS number. Want: %d, got: %d", ti, len(tc.wantNS), len(rec.Msg.Ns))
		} else {
			for i, ns := range rec.Msg.Ns {
				got, ok := ns.(*dns.SOA)
				if !ok {
					t.Errorf("Test %d: Unexpected NS type. Want: SOA, got: %v", ti, reflect.TypeOf(got))
				}
				if got.String() != tc.wantNS[i] {
					t.Errorf("Test %d: Unexpected NS.\nWant: %v\nGot: %v", ti, tc.wantNS[i], got)
				}
			}
		}
	}
}
