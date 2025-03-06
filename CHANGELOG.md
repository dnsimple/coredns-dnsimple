# Changelog

## main

## 1.4.0

- go version bump to 1.23.4
- dependency updates + streamlining docker image release process 

## 1.3.0

- This release bumps the go version to 1.23.
- Dependency updates

## 1.2.0

NOTES:

This release fixes our docker image release process.

## 1.1.0

NOTES:

- This release bumps the go version to 1.21.
- Dependency updates tracking the latest version of CoreDNS.

## 1.0.0

FEATURES:

- The plugin now supports the setting of `client_dns_resolver` to override the DNS resolver used for DNSimple API endpoints. This is useful when running CoreDNS as the DNS resolver for the host. Format is `ADDRESS:PORT`. (#25)

ENHANCEMENTS:

- Enforce the presence of the `access_token` and `account_id` parameters in the configuration whether supplied by environment variables or directly in config, we raise an error if they are found missing. (#24)

## 1.0.0-rc.1

FEATURES:

- The plugin allows for zones managed at DNSimple to be loaded and served by CoreDNS.
- The plugin supports all records types supported by DNSimple. This includes the custom record types such as `ALIAS`, `POOL` and `URL`.

NOTES:

This is the first release of the plugin.
Please read the [README](./README.md) for more information on how to use the plugin and the [CONTRIBUTING](./CONTRIBUTING.md) guide if you want to contribute to the plugin.
