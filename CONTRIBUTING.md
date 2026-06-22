# Contributing

## Prerequisites

- Go (version from `go.mod`)
- Varnish 7.6 (required for integration tests)

Install Varnish on Debian/Ubuntu:

```sh
curl -fsSL https://packagecloud.io/varnishcache/varnish76/gpgkey | \
  gpg --dearmor | sudo tee /usr/share/keyrings/varnish.gpg > /dev/null
echo "deb [signed-by=/usr/share/keyrings/varnish.gpg] \
  https://packagecloud.io/varnishcache/varnish76/ubuntu/ \
  $(lsb_release -cs) main" | \
  sudo tee /etc/apt/sources.list.d/varnish.list
sudo apt-get update -qq && sudo apt-get install -y varnish
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
