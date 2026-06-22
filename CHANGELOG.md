# Changelog

## v1.0.0 (2026-06-22)

Initial release.

### Features

- Two input modes: YAML routes manifest (`routes.yaml`) or per-file VCL front matter
- Generates routing VCL and Varnish cmdfile from route configs
- Wildcard hostname matching (`*.example.com`)
- TLS certificate management (PEM bundle or cert+key pair)
- Atomic 8-stage hot reload of a running Varnish instance via `-reload`
- Rollback on failure before point of no return
- Route lookup testing via `-test-route`
- systemd service unit in `contrib/`
