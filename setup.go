package dnsimple

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/pkg/fall"
	clog "github.com/coredns/coredns/plugin/pkg/log"
	"github.com/dnsimple/dnsimple-go/dnsimple"
)

var log = clog.NewWithPlugin("dnsimple")

func init() { plugin.Register("dnsimple", setup) }

const defaultUserAgent = "coredns-plugin-dnsimple"

type Options struct {
	accountId   string
	apiCaller DNSimpleApiCaller
	identifier  string
	maxRetries  int
	refresh     time.Duration
}

// exposed for testing
var newDnsimpleService = func(ctx context.Context, accessToken string, baseUrl string) (dnsimpleService, error) {
	client := dnsimple.NewClient(dnsimple.StaticTokenHTTPClient(ctx, accessToken))
	client.BaseURL = baseUrl
	client.SetUserAgent(defaultUserAgent)
	return dnsimpleClient{client}, nil
}

func setup(c *caddy.Controller) error {
	rand.Seed(time.Now().UTC().UnixNano())

	for c.Next() {
		keyPairs := map[string]struct{}{}
		keys := map[string][]string{}

		opts := Options{}
		var accessToken string
		var baseUrl string
		var fall fall.F

		args := c.RemainingArgs()

		for i := 0; i < len(args); i++ {
			parts := strings.SplitN(args[i], ":", 2)
			if len(parts) < 1 {
				return plugin.Error("dnsimple", c.Errf("invalid zone %q", args[i]))
			}
			dns, hostedZoneRegion := parts[0], "global"
			if len(parts) > 1 {
				hostedZoneRegion = parts[1]
			}
			if dns == "" || hostedZoneRegion == "" {
				return plugin.Error("dnsimple", c.Errf("invalid zone %q", args[i]))
			}
			if _, ok := keyPairs[args[i]]; ok {
				return plugin.Error("dnsimple", c.Errf("conflict zone %q", args[i]))
			}

			keyPairs[args[i]] = struct{}{}
			keys[dns] = append(keys[dns], hostedZoneRegion)
		}

		// TODO: set to warn when no zones are defined
		if len(keys) == 0 {
			return plugin.Error("dnsimple", c.Errf("no zone(s) specified"))
		}

		for c.NextBlock() {
			switch c.Val() {
			case "access_token":
				v := c.RemainingArgs()
				if len(v) < 2 {
					return plugin.Error("dnsimple", c.Errf("invalid access token: '%v'", v))
				}
				accessToken = v[1]
				// TODO We should clarify why this is bad.
				log.Warning("consider using alternative ways of providing credentials, such as environment variables")
			case "account_id":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				opts.accountId = c.Val()
			case "fallthrough":
				fall.SetZonesFromArgs(c.RemainingArgs())
			case "identifier":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				opts.identifier = c.Val()
			case "max_retries":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				maxRetriesStr := c.Val()
				var err error
				if opts.maxRetries, err = strconv.Atoi(maxRetriesStr); err != nil {
					return plugin.Error("dnsimple", c.Errf("unable to parse max retries: %v", err))
				}
				if opts.maxRetries < 0 {
					return plugin.Error("dnsimple", c.Err("max retries cannot be less than zero"))
				}
			case "refresh":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				var err error
				refreshStr := c.Val()
				if _, err = strconv.Atoi(refreshStr); err == nil {
					refreshStr = fmt.Sprintf("%ss", c.Val())
				}
				if opts.refresh, err = time.ParseDuration(refreshStr); err != nil {
					return plugin.Error("dnsimple", c.Errf("unable to parse duration: %v", err))
				}
				if opts.refresh <= 60 {
					return plugin.Error("dnsimple", c.Errf("refresh interval must be greater than 60 seconds: %q", refreshStr))
				}
			case "base_url":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				baseUrl = c.Val()
			default:
				return plugin.Error("dnsimple", c.Errf("unknown property %q", c.Val()))
			}
		}

		// Set default values.
		if accessToken == "" {
			// Keep this environment variable name consistent across all our integrations (e.g. SDKs, Terraform provider).
			accessToken = os.Getenv("DNSIMPLE_TOKEN")
		}

		if baseUrl == "" {
			// Default to production
			baseUrl = "https://api.dnsimple.com"
		}

		if opts.accountId == "" {
			opts.accountId = os.Getenv("DNSIMPLE_ACCOUNT_ID")
		}

		if opts.identifier == "" {
			opts.identifier = "default"
		}

		if opts.refresh < 60 {
			// Default update frequency of 1 minute.
			opts.refresh = time.Duration(1) * time.Minute
		}

		opts.apiCaller = createDNSimpleApiCaller(baseUrl, accessToken, defaultUserAgent)

		ctx, cancel := context.WithCancel(context.Background())
		client, err := newDnsimpleService(ctx, accessToken, baseUrl)
		if err != nil {
			cancel()
			return err
		}
		h, err := New(ctx, client, keys, opts)
		if err != nil {
			cancel()
			return plugin.Error("dnsimple", c.Errf("failed to create dnsimple plugin: %v", err))
		}
		h.Fall = fall
		if err := h.Run(ctx); err != nil {
			cancel()
			return plugin.Error("dnsimple", c.Errf("failed to initialize dnsimple plugin: %v", err))
		}

		dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
			h.Next = next
			return h
		})
		c.OnShutdown(func() error { cancel(); return nil })
	}
	return nil
}
