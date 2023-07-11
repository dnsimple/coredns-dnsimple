# Contributing

- [Contributing](#contributing)
  - [Getting Started](#getting-started)
  - [Configuration](#configuration)
  - [Compilation](#compilation)
  - [Testing](#testing)
  - [Running](#running)
  - [Releasing](#releasing)


## Getting Started

Clone the repository [in your workspace](https://golang.org/doc/code.html#Organization) and move into it:

```shell
git clone git@github.com:dnsimple/coredns-dnsimple.git
cd coredns-dnsimple
```

Install standard Go development tooling:

```shell
make install-tools
```

## Configuration

To configure a local CoreDNS server:

```shell
cp Corefile.example Corefile
vim Corefile
```


## Compilation

```shell
make build
```

This will produce a `coredns-dnsimple` binary in the current directory.


## Testing

To run the unit test suite:

```shell
make test
```


## Running

```shell
make start
```


## Releasing

The following instructions uses `$VERSION` as a placeholder, where `$VERSION` is a `MAJOR.MINOR.BUGFIX` release such as `1.2.0`.

1. Set the version in `./version.go`:

    ```go
    PluginVersion = "$VERSION"
    ```

1. Run the test suite and ensure all the tests pass.

1. Finalize the `## main` section in `CHANGELOG.md` assigning the version.

1. Commit and push the changes

    ```shell
    git commit -a -m "Release $VERSION"
    git push origin main
    ```

1. Wait for CI to complete.

1. Release the version.

    ```shell
    make release VERSION=$VERSION
    ```
