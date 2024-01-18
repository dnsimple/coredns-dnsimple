# Changelog

## main

FEATURES:

- The plugin now supports the setting of `client_dns_resolver` to override the DNS resolver used for DNSimple API endpoints. This is useful when running CoreDNS as the DNS resolver for the host. Format is `ADDRESS:PORT`. (#25)

## 1.0.0-rc.1

FEATURES:

- The plugin allows for zones managed at DNSimple to be loaded and served by CoreDNS.
- The plugin supports all records types supported by DNSimple. This includes the custom record types such as `ALIAS`, `POOL` and `URL`.

NOTES:

This is the first release of the plugin.
Please read the [README](./README.md) for more information on how to use the plugin and the [CONTRIBUTING](./CONTRIBUTING.md) guide if you want to contribute to the plugin.
