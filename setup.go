package dnsimple

import (
	"context"
	"fmt"
	"net"
	"net/http"
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
	"golang.org/x/oauth2"
)

var log = clog.NewWithPlugin("dnsimple")

func init() { plugin.Register("dnsimple", setup) }

const defaultUserAgent = "coredns-plugin-dnsimple"

type Options struct {
	accountID         string
	accessToken       string
	apiCaller         APICaller
	customDNSResolver string
	clientDNSResolver string
	identifier        string
	maxRetries        int
	refresh           time.Duration
	customHTTPDialer  func(ctx context.Context, network, address string) (net.Conn, error)
}

// exposed for testing
var newDnsimpleService = func(ctx context.Context, options Options, accessToken string, baseUrl string) (dnsimpleService, error) {
	httpClient := dnsimple.StaticTokenHTTPClient(ctx, accessToken)

	if options.clientDNSResolver != "" {
		if httpClient.Transport.(*oauth2.Transport).Base != nil {
			httpClient.Transport.(*oauth2.Transport).Base.(*http.Transport).DialContext = options.customHTTPDialer
		} else {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.DialContext = options.customHTTPDialer
			httpClient.Transport.(*oauth2.Transport).Base = transport
		}
	}
	client := dnsimple.NewClient(httpClient)
	client.BaseURL = baseUrl
	client.SetUserAgent(defaultUserAgent + "/" + PluginVersion)
	return dnsimpleClient{client}, nil
}

func setup(c *caddy.Controller) error {
	for c.Next() {
		keyPairs := map[string]struct{}{}
		keys := map[string][]string{}

		opts := Options{}
		var accessToken string
		var baseURL string
		var fall fall.F

		args := c.RemainingArgs()

		for i := 0; i < len(args); i++ {
			parts := strings.SplitN(args[i], ":", 2)
			if len(parts) < 1 {
				return plugin.Error("dnsimple", c.Errf("invalid zone %q", args[i]))
			}
			zone, region := parts[0], "global"
			if len(parts) > 1 && parts[1] != "" {
				region = parts[1]
			}
			if zone == "" || region == "" {
				return plugin.Error("dnsimple", c.Errf("invalid zone %q", args[i]))
			}
			if _, ok := keyPairs[args[i]]; ok {
				return plugin.Error("dnsimple", c.Errf("conflict zone %q", args[i]))
			}

			keyPairs[args[i]] = struct{}{}
			keys[zone] = append(keys[zone], region)
		}

		if len(keys) == 0 {
			return plugin.Error("dnsimple", c.Errf("no zone(s) specified"))
		}

		for c.NextBlock() {
			switch c.Val() {
			case "access_token":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				if c.Val() != "" {
					log.Warning("consider using alternative ways of providing credentials, such as environment variables")
				}
				opts.accessToken = c.Val()
			case "account_id":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				opts.accountID = c.Val()
			case "base_url":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				baseURL = c.Val()
			case "custom_dns_resolver":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				opts.customDNSResolver = c.Val()
			case "client_dns_resolver":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				opts.clientDNSResolver = c.Val()
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
			default:
				return plugin.Error("dnsimple", c.Errf("unknown property %q", c.Val()))
			}
		}

		// Set default values.
		if accessToken == "" {
			// Keep this environment variable name consistent across all our integrations (e.g. SDKs, Terraform provider).
			accessToken = os.Getenv("DNSIMPLE_TOKEN")
		}
		// Still blank, return error
		if accessToken == "" {
			return plugin.Error("dnsimple", c.Err("access token must be provided via the Corefile or DNSIMPLE_TOKEN environment variable"))
		}

		if opts.accountID == "" {
			opts.accountID = os.Getenv("DNSIMPLE_ACCOUNT_ID")
		}
		// Still blank, return error
		if opts.accountID == "" {
			return plugin.Error("dnsimple", c.Err("account ID must be provided via the Corefile or DNSIMPLE_TOKEN environment variable"))
		}

		if baseURL == "" {
			// Default to production
			baseURL = "https://api.dnsimple.com"
		}

		if opts.identifier == "" {
			opts.identifier = "default"
		}

		if opts.refresh < 60 {
			// Default update frequency of 1 minute.
			opts.refresh = time.Duration(1) * time.Minute
		}

		if opts.clientDNSResolver == "" {
			dialer := &net.Dialer{
				Resolver: &net.Resolver{
					PreferGo: true,
					Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
						d := net.Dialer{
							Timeout: time.Second * 5,
						}
						return d.DialContext(ctx, network, opts.clientDNSResolver)
					},
				},
			}
			opts.customHTTPDialer = func(ctx context.Context, network, address string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, address)
			}
		}

		opts.apiCaller = createDNSimpleAPICaller(opts, baseURL, accessToken, defaultUserAgent+"/"+PluginVersion)

		ctx, cancel := context.WithCancel(context.Background())
		client, err := newDnsimpleService(ctx, opts, accessToken, baseURL)
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
