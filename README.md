# dnsimple

## Name

*dnsimple* - enables serving zone data from DNSimple.

## Description

The dnsimple plugin is useful for serving zones defined in DNSimple. This plugin supports all [DNSimple records](https://support.dnsimple.com/articles/supported-dns-records/), including [ALIAS](https://support.dnsimple.com/articles/alias-record/), [URL](https://support.dnsimple.com/articles/url-record/), and [POOL](https://support.dnsimple.com/articles/pool-record/).

## Syntax

```
dnsimple ZONE [ZONE ...] {
    access_token DNSIMPLE_TOKEN
    account_id   DNSIMPLE_ACCOUNT_ID
    fallthrough  [ZONES...]
    max_retries  MAX_RETRIES
    refresh      DURATION
    sandbox      BOOLEAN
}
```

- **ZONE**: The zone names to retrieve from DNSimple.
- `access_token`: The access token to use when calling DNSimple APIs. If it's not provided, the environment variable `DNSIMPLE_TOKEN` will be used.
- `account_id`: The account ID containing the configured zones. If it's not provided, the environment variable `DNSIMPLE_ACCOUNT_ID` will be used.
- `custom_dns_resolver`: Optionally override the DNS resolver to use for resolving ALIAS records. By default, the system resolver is used. See the [ALIAS records](#alias-records) section for more details.
- `fallthrough`: If a query matches the zone(s) but no response message can be generated, the query will be passed to the next plugin in the chain. To restrict passing only for specific zones, list them here; all other zone queries will **not** "fall through".
- `max_retries`: Maximum retry attempts to fetch zones using the DNSimple API. Must be greater than zero. Defaults to 3.
- `refresh`: The interval to refresh zones at. It must be a valid duration, and defaults to `1m`.
- `sandbox`: If this is set to true, the [DNSimple Sandbox API](https://support.dnsimple.com/articles/sandbox/) will be used instead.

## Examples

Enable dnsimple, using environment variables to provide the access token and account ID:

```
example.org {
  dnsimple example.org
}
```

Enable dnsimple with explicit credentials:

```
example.org {
  dnsimple example.org {
    access_token Yshames7AMTNMo7qHLGUkkg06p4rs
    account_id 131072
  }
}
```

Enable dnsimple with multiple zones, and fallthrough for one:

```
. {
  dnsimple example.org example.com {
    fallthrough example.com
  }
}
```

Enable dnsimple and refresh records every 5 minutes and 20 seconds:

```
example.org {
  dnsimple example.org {
    refresh 5m20s
  }
}
```

## ALIAS records

ALIAS records are handled by resolving at query time, using the system DNS resolver or the custom resolver specified by `custom_dns_resolver`. This should work as expected for most use cases, including ALIAS records that target other domains that only exist within your private network, as CoreDNS should be running inside your private network and therefore have the same DNS resolver settings as any other device inside the network.

<details>
<summary>Detailed example</summary>

To help demonstrate how ALIAS records and DNS resolvers interact, consider the following setup:

- Private network with DNS resolver set to a central private DNS server 192.168.0.1 via DHCP.
- Two additional CoreDNS servers at 192.168.0.2 and 192.168.0.3, acting as the authority for `a.local` and `b.local` respectively.
- The central server delegates queries to zones `a.local` and `b.local` to those CoreDNS servers, and forwards all others to the public resolver 1.1.1.1.
- On the `a.local` zone, the records `ALIAS a.local -> b.local` and `CNAME ext.a.local -> external.com` exist.
- On the `b.local` zone, the record `ALIAS b.local -> ext.a.local` exists.

Given a query of `a.local`, the expected flow should be:
- Query of `a.local` from the client to 192.168.0.1, delegated to 192.168.0.2.
- Query of `b.local` from 192.168.0.2 to 192.168.0.1, delegated to 192.168.0.3.
- Query of `ext.a.local` from 192.168.0.3 to 192.168.0.1, delegated to 192.168.0.2.
- Query of `example.com` from 192.168.0.2 to 192.168.0.1, delegated to 1.1.1.1.

If all private DNS servers mentioned in the example are configured to resolve using `192.168.0.1` as mentioned, all queries will have consistent results and this will work as expected.
</details>

## Authentication

An access token is needed in order to call the DNSimple API. To learn more about these and how to generate them, see this [article](https://support.dnsimple.com/articles/api-access-token/).
