# Contributing

## Prerequisites

- Go (version from `go.mod`)
- Varnish 7.6 (required for integration tests)

Install Varnish Cache on Debian/Ubuntu:

```sh
curl -Ls https://packages.varnish-software.com/varnish/bootstrap-deb.sh | sudo bash
sudo apt-get install -y varnish varnish-dev
```

Or Varnish Enterprise:

```sh
curl -s https://packagecloud.io/install/repositories/varnishplus/60-enterprise/script.deb.sh | sudo INSTALL= bash
sudo apt-get install -y varnish-plus varnish-plus-dev
```

## Running tests

```sh
go test -race ./...
```

## Submitting changes

- Open an issue before large changes to align on approach
- Keep PRs focused; one logical change per PR
- All tests must pass; add tests for new behavior
- Commit messages: short imperative subject line, optional body explaining why
