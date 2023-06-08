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
	"golang.org/x/oauth2"
)

var log = clog.NewWithPlugin("dnsimple")

func init() { plugin.Register("dnsimple", setup) }

func setup(c *caddy.Controller) error {
	rand.Seed(time.Now().UTC().UnixNano())

	for c.Next() {
		keyPairs := map[string]struct{}{}
		keys := map[string][]string{}

		var fall fall.F

		// Default update frequency of 1 minute.
		refresh := time.Duration(1) * time.Minute

		args := c.RemainingArgs()

		for i := 0; i < len(args); i++ {
			parts := strings.SplitN(args[i], ":", 2)
			if len(parts) != 2 {
				return plugin.Error("dnsimple", c.Errf("invalid zone %q", args[i]))
			}
			dns, hostedZoneRegion := parts[0], parts[1]
			if dns == "" || hostedZoneRegion == "" {
				return plugin.Error("dnsimple", c.Errf("invalid zone %q", args[i]))
			}
			if _, ok := keyPairs[args[i]]; ok {
				return plugin.Error("dnsimple", c.Errf("conflict zone %q", args[i]))
			}

			keyPairs[args[i]] = struct{}{}
			keys[dns] = append(keys[dns], hostedZoneRegion)
		}

		if len(keys) == 0 {
			return plugin.Error("dnsimple", c.Errf("no zone(s) specified"))
		}

		var (
			accessToken string
			accountId   string
			identifier  string
			sandbox     bool
		)

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
				accountId = c.Val()
			case "fallthrough":
				fall.SetZonesFromArgs(c.RemainingArgs())
			case "identifier":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				identifier = c.Val()
			case "refresh":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				var err error
				refreshStr := c.Val()
				if _, err = strconv.Atoi(refreshStr); err == nil {
					refreshStr = fmt.Sprintf("%ss", c.Val())
				}
				if refresh, err = time.ParseDuration(refreshStr); err != nil {
					return plugin.Error("dnsimple", c.Errf("unable to parse duration: %v", err))
				}
				if refresh <= 60 {
					return plugin.Error("dnsimple", c.Errf("refresh interval must be greater than 60 seconds: %q", refreshStr))
				}
			case "sandbox":
				if !c.NextArg() {
					return plugin.Error("dnsimple", c.ArgErr())
				}
				sandbox, _ = strconv.ParseBool(c.Val())
			default:
				return plugin.Error("dnsimple", c.Errf("unknown property %q", c.Val()))
			}
		}

		if accessToken == "" {
			// Keep this environment variable name consistent across all our integrations (e.g. SDKs, Terraform provider).
			accessToken = os.Getenv("DNSIMPLE_TOKEN")
		}

		if accountId == "" {
			accountId = os.Getenv("DNSIMPLE_ACCOUNT_ID")
		}

		if identifier == "" {
			identifier = "default"
		}

		// TODO This overrides `sandbox false` in the config if "true" but ignores `DNSIMPLE_SANDBOX=false` if `sandbox true`, so the priority behaviour is inconsistent.
		if !sandbox && os.Getenv("DNSIMPLE_SANDBOX") == "true" {
			sandbox = true
		}

		ctx, cancel := context.WithCancel(context.Background())
		client := dnsimple.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken})))
		client.SetUserAgent("coredns-plugin-dnsimple")
		if sandbox {
			client.BaseURL = "https://api.sandbox.dnsimple.com"
		}

		h, err := New(ctx, accountId, client, identifier, keys, refresh)
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
